// Package ha verifies the multi-replica design under failure (AUDIT/10
// Phase 20, D-16): PostgreSQL, NATS in operator mode, two API replicas behind
// a load balancer, two Control Managers and several agents, all real
// binaries. Faults are injected by killing processes, restarting PostgreSQL
// and cutting a Control Manager's NATS connection.
//
// It runs only when asked, because it takes several minutes and restarts the
// PostgreSQL container:
//
//	MACHINARCHY_HA=1 go test -v -timeout 40m ./test/ha/
//
// It needs Docker: the harness runs its own PostgreSQL container (image
// MACHINARCHY_HA_PG_IMAGE, default postgres:16-alpine) on a named volume, so
// a restart keeps the data, and removes both at the end.
package ha

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/machinarchy/machinarchy/internal/agentgateway"
	"github.com/machinarchy/machinarchy/internal/testutil/natstest"
	"github.com/machinarchy/machinarchy/internal/testutil/sshtest"
)

func enabled(t *testing.T) {
	t.Helper()
	if os.Getenv("MACHINARCHY_HA") != "1" {
		t.Skip("HA verification runs only with MACHINARCHY_HA=1 (see the package comment)")
	}
}

// ---- binaries and processes ----

func buildAll(t *testing.T) (api, controlManager, agent string) {
	t.Helper()
	agentDir := os.Getenv("MACHINARCHY_AGENT_SOURCE")
	if agentDir == "" {
		agentDir = filepath.Join("..", "..", "..", "machinarchy-agent")
	}
	agentDir, _ = filepath.Abs(agentDir)
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	dir := t.TempDir()
	build := func(srcDir, pkg, name string) string {
		out := filepath.Join(dir, name)
		cmd := exec.Command("go", "build", "-o", out, pkg)
		cmd.Dir = srcDir
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go build %s: %v\n%s", pkg, err, output)
		}
		return out
	}
	return build(root, "./cmd/api", "machinarchy-api"), build(root, "./cmd/controlmanager", "machinarchy-controlmanager"),
		build(agentDir, "./cmd/machinarchy-agent", "machinarchy-agent")
}

type process struct {
	name   string
	binary string
	env    []string
	dir    string

	mu     sync.Mutex
	cmd    *exec.Cmd
	log    string
	runs   int
	exited chan struct{}
	err    error
}

func (p *process) start(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.runs++
	p.log = filepath.Join(p.dir, fmt.Sprintf("%s-%d.log", p.name, p.runs))
	out, err := os.Create(p.log)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(p.binary)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, p.env...)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p.cmd, p.exited, p.err = cmd, make(chan struct{}), nil
	exited := p.exited
	go func() {
		err := cmd.Wait()
		_ = out.Close()
		p.mu.Lock()
		p.err = err
		p.mu.Unlock()
		close(exited)
	}()
}

func (p *process) alive() bool {
	p.mu.Lock()
	exited := p.exited
	p.mu.Unlock()
	select {
	case <-exited:
		return false
	default:
		return true
	}
}

// kill ends the process at once (SIGKILL): a crash, no cleanup.
func (p *process) kill() {
	p.mu.Lock()
	cmd, exited := p.cmd, p.exited
	p.mu.Unlock()
	_ = cmd.Process.Kill()
	<-exited
}

// stop sends SIGTERM and waits; it reports the exit error.
func (p *process) stop(timeout time.Duration) error {
	p.mu.Lock()
	cmd, exited := p.cmd, p.exited
	p.mu.Unlock()
	select {
	case <-exited:
		return p.exitErr()
	default:
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-exited:
		return p.exitErr()
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-exited
		return fmt.Errorf("%s did not stop within %s of SIGTERM", p.name, timeout)
	}
}

func (p *process) exitErr() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *process) logText() string {
	p.mu.Lock()
	path := p.log
	p.mu.Unlock()
	data, _ := os.ReadFile(path)
	return string(data)
}

// logField returns the first value of a JSON log field with the given message.
func (p *process) logField(message, field string) string {
	scanner := bufio.NewScanner(strings.NewReader(p.logText()))
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		var entry map[string]any
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry["msg"] != message {
			continue
		}
		if value, ok := entry[field].(string); ok {
			return value
		}
	}
	return ""
}

// ---- network helpers ----

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// cutProxy forwards TCP to target until it is cut: Cut closes every open
// connection and refuses new ones; Heal accepts again. One per Control
// Manager in front of NATS, to partition a single replica.
type cutProxy struct {
	target   string
	listener net.Listener
	cut      atomic.Bool
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
}

func newCutProxy(t *testing.T, target string) *cutProxy {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &cutProxy{target: target, listener: l, conns: map[net.Conn]struct{}{}}
	t.Cleanup(func() { _ = l.Close(); p.Cut() })
	go func() {
		for {
			client, err := l.Accept()
			if err != nil {
				return
			}
			if p.cut.Load() {
				_ = client.Close()
				continue
			}
			server, err := net.Dial("tcp", target)
			if err != nil {
				_ = client.Close()
				continue
			}
			p.track(client, server)
			go p.pipe(client, server)
			go p.pipe(server, client)
		}
	}()
	return p
}

func (p *cutProxy) URL() string { return "nats://" + p.listener.Addr().String() }

func (p *cutProxy) track(conns ...net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range conns {
		p.conns[c] = struct{}{}
	}
}

func (p *cutProxy) pipe(dst, src net.Conn) {
	_, _ = io.Copy(dst, src)
	_ = dst.Close()
	_ = src.Close()
}

func (p *cutProxy) Cut() {
	p.cut.Store(true)
	p.mu.Lock()
	defer p.mu.Unlock()
	for c := range p.conns {
		_ = c.Close()
	}
	p.conns = map[net.Conn]struct{}{}
}

func (p *cutProxy) Heal() { p.cut.Store(false) }

// balancer spreads API requests over the replicas and fails over to the next
// one when a replica does not answer, like a load balancer with retries.
type balancer struct {
	backends []*url.URL
	next     atomic.Uint64
	server   *http.Server
	addr     string
}

func newBalancer(t *testing.T, ports ...int) *balancer {
	t.Helper()
	b := &balancer{}
	for _, port := range ports {
		u, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
		b.backends = append(b.backends, u)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b.addr = "http://" + l.Addr().String()
	b.server = &http.Server{Handler: http.HandlerFunc(b.serve), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = b.server.Serve(l) }()
	t.Cleanup(func() { _ = b.server.Close() })
	return b
}

func (b *balancer) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	start := b.next.Add(1)
	for i := range len(b.backends) {
		backend := b.backends[(int(start)+i)%len(b.backends)]
		request := r.Clone(r.Context())
		request.Body = io.NopCloser(bytes.NewReader(body))
		request.URL.Scheme, request.URL.Host, request.RequestURI = backend.Scheme, backend.Host, ""
		response, err := http.DefaultTransport.RoundTrip(request)
		if err != nil {
			continue // replica down: try the next one
		}
		defer response.Body.Close()
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
		return
	}
	http.Error(w, "no API replica answered", http.StatusBadGateway)
}

var _ = httputil.ReverseProxy{} // the balancer above is a minimal ReverseProxy with failover

// ---- API client ----

type client struct {
	base  string
	token string
}

func (c *client) do(t *testing.T, method, path string, body, out any) int {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	request, _ := http.NewRequest(method, c.base+path, reader)
	request.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return 0
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if out != nil && len(data) > 0 {
		_ = json.Unmarshal(data, out)
	}
	return response.StatusCode
}

func (c *client) must(t *testing.T, method, path string, body, out any, want int) {
	t.Helper()
	var raw json.RawMessage
	if status := c.do(t, method, path, body, &raw); status != want {
		t.Fatalf("%s %s: %d, want %d: %s", method, path, status, want, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: decode %s: %v", method, path, raw, err)
		}
	}
}

func waitFor(t *testing.T, timeout time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

type operation struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Status struct {
		State   string `json:"state"`
		Targets []struct {
			Spec struct {
				HostID string `json:"hostId"`
			} `json:"spec"`
			State  string `json:"state"`
			Result *struct {
				ErrorClass string `json:"errorClass"`
				Output     string `json:"output"`
			} `json:"result"`
			Launch *struct {
				Attempt int `json:"attempt"`
				PID     int `json:"pid"`
			} `json:"launch"`
		} `json:"targets"`
	} `json:"status"`
}

func (o operation) terminal() bool {
	switch o.Status.State {
	case "Succeeded", "PartiallySucceeded", "Failed", "Cancelled", "TimedOut", "Interrupted":
		return true
	}
	return false
}

// states counts targets by state and error class ("Failed/timeout").
func (o operation) states() map[string]int {
	out := map[string]int{}
	for _, target := range o.Status.Targets {
		key := target.State
		if target.Result != nil && target.Result.ErrorClass != "" {
			key += "/" + target.Result.ErrorClass
		}
		out[key]++
	}
	return out
}

// ---- topology ----

type topology struct {
	t        *testing.T
	work     string
	pgURL    string
	pgName   string // the harness's PostgreSQL container
	db       *sql.DB
	api      []*process
	cm       []*process
	cmProxy  []*cutProxy
	agents   []*process
	lb       *balancer
	owner    *client
	ssh      []*sshtest.Server
	sshHomes []string
	sshHosts []string // managed host IDs reached over SSH
	agentIDs []string // managed host IDs reached through agents
	cmPorts  []int
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(raw)
}

func secretFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// newTopology starts every process at once, as a deployment would, and
// registers SSH and agent hosts through the API.
func newTopology(t *testing.T, sshServers, hostsPerServer, agentCount int) *topology {
	t.Helper()
	apiBinary, cmBinary, agentBinary := buildAll(t)
	work := t.TempDir()
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatal(err)
	}
	topo := &topology{t: t, work: work}
	topo.pgURL, topo.pgName = startPostgres(t)
	db, err := sql.Open("pgx", topo.pgURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	topo.db = db

	var ports []string
	for range sshServers {
		home, err := os.MkdirTemp("", "mha")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(home) })
		t.Cleanup(func() { _ = exec.Command("pkill", "-KILL", "-f", home).Run() })
		server := sshtest.Start(t, sshtest.Options{Password: "ha-password", ExecStreams: sshtest.ShellExec("", home)})
		topo.ssh = append(topo.ssh, server)
		topo.sshHomes = append(topo.sshHomes, home)
		ports = append(ports, fmt.Sprint(server.Port))
	}

	ns := natstest.Start(t)
	controllerCreds := ns.ControllerCredentials(t, agentgateway.ControllerPermissions())
	signer := ns.SignerFile(t)
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	keyFile := secretFile(t, work, "signing.key", base64.StdEncoding.EncodeToString(seed))
	common := []string{
		"MACHINARCHY_ENVIRONMENT=development",
		"MACHINARCHY_DATABASE_ENGINE=postgres",
		"MACHINARCHY_DATABASE_URL=" + topo.pgURL,
		"MACHINARCHY_ENCRYPTION_SECRET=" + randomHex(t, 24),
		"MACHINARCHY_OPERATOR_TOKEN=" + randomHex(t, 24),
		"MACHINARCHY_BOOTSTRAP_ADMIN_PASSWORD=ha-admin-password",
		"MACHINARCHY_SSH_ALLOW_LOOPBACK=true",
		"MACHINARCHY_SSH_ALLOWED_PORTS=" + strings.Join(ports, ","),
		"MACHINARCHY_EXECUTION_ENABLED=true",
		"MACHINARCHY_EXECUTION_ALLOWED_PROGRAMS=sleep {int},sh -c *,uptime",
		"MACHINARCHY_LOG_LEVEL=info",
		// One test client stands in for many users: the per-client limit
		// would otherwise throttle the harness itself.
		"MACHINARCHY_RATE_LIMIT_RPS=2000",
		"MACHINARCHY_RATE_LIMIT_BURST=4000",
	}
	var apiPorts []int
	for i := range 2 {
		port := freePort(t)
		apiPorts = append(apiPorts, port)
		topo.api = append(topo.api, &process{name: fmt.Sprintf("api-%d", i+1), binary: apiBinary, dir: work,
			env: append(append([]string{}, common...), fmt.Sprintf("MACHINARCHY_LISTEN_ADDR=127.0.0.1:%d", port))})
	}
	for i := range 2 {
		proxy := newCutProxy(t, strings.TrimPrefix(ns.URL, "nats://"))
		port := freePort(t)
		topo.cmProxy = append(topo.cmProxy, proxy)
		topo.cmPorts = append(topo.cmPorts, port)
		topo.cm = append(topo.cm, &process{name: fmt.Sprintf("cm-%d", i+1), binary: cmBinary, dir: work,
			env: append(append([]string{}, common...),
				fmt.Sprintf("MACHINARCHY_LISTEN_ADDR=127.0.0.1:%d", port),
				"MACHINARCHY_AGENT_GATEWAY_NATS_URL="+proxy.URL(),
				"MACHINARCHY_AGENT_GATEWAY_SIGNING_KEY_FILE="+keyFile,
				"MACHINARCHY_AGENT_GATEWAY_NATS_CREDENTIALS_FILE="+controllerCreds,
				"MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT="+ns.Account,
				"MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT_SIGNING_KEY_FILE="+signer,
				"MACHINARCHY_AGENT_GATEWAY_LEASE_TTL=20s",
				"MACHINARCHY_AGENT_GATEWAY_HEARTBEAT=5s",
			)})
	}
	// Everything starts together, as replicas of a deployment do.
	for _, p := range append(append([]*process{}, topo.api...), topo.cm...) {
		p.start(t)
	}
	t.Cleanup(func() {
		for _, p := range append(append(append([]*process{}, topo.agents...), topo.cm...), topo.api...) {
			if p.alive() {
				_ = p.stop(30 * time.Second)
			}
		}
		if t.Failed() {
			keep := os.Getenv("MACHINARCHY_HA_LOG_DIR")
			for _, p := range append(append(append([]*process{}, topo.api...), topo.cm...), topo.agents...) {
				t.Logf("--- %s (%s) tail ---\n%s", p.name, p.log, tail(p.logText(), 40))
			}
			if keep != "" {
				logs, _ := filepath.Glob(filepath.Join(work, "*.log"))
				for _, log := range logs {
					if data, err := os.ReadFile(log); err == nil {
						_ = os.WriteFile(filepath.Join(keep, filepath.Base(log)), data, 0o600)
					}
				}
				t.Logf("process logs kept in %s", keep)
			}
		}
	})
	topo.lb = newBalancer(t, apiPorts...)
	for i, port := range apiPorts {
		topo.waitReady(fmt.Sprintf("api-%d", i+1), port, 60*time.Second)
	}
	for i, port := range topo.cmPorts {
		topo.waitReady(fmt.Sprintf("cm-%d", i+1), port, 60*time.Second)
	}

	owner := &client{base: topo.lb.addr}
	var login struct {
		Token string `json:"token"`
	}
	owner.must(t, http.MethodPost, "/api/auth/login", map[string]string{"username": "admin", "password": "ha-admin-password"}, &login, http.StatusOK)
	owner.token = login.Token
	topo.owner = owner

	// SSH hosts: a password profile with setsid detached execution.
	var secret struct {
		ID             string `json:"id"`
		CurrentVersion int    `json:"currentVersion"`
	}
	owner.must(t, http.MethodPost, "/api/v1/secrets", map[string]any{"name": "ha-ssh", "purpose": "ssh-credential", "value": "ha-password"}, &secret, http.StatusCreated)
	var sshProfile struct {
		ID string `json:"id"`
	}
	owner.must(t, http.MethodPost, "/api/v1/managed/access-profiles", map[string]any{"name": "ha-ssh", "spec": map[string]any{
		"connectorKind": "ssh", "username": "deploy", "authenticationMethod": "password", "enabled": true,
		"credential": map[string]any{"id": secret.ID, "version": secret.CurrentVersion},
		"execution":  map[string]any{"supervisor": "setsid"}}}, &sshProfile, http.StatusCreated)
	for i, server := range topo.ssh {
		for j := range hostsPerServer {
			var host struct {
				Metadata struct {
					ID string `json:"id"`
				} `json:"metadata"`
			}
			owner.must(t, http.MethodPost, "/api/v1/managed/hosts", map[string]any{"name": fmt.Sprintf("ssh-%d-%d", i, j),
				"labels": map[string]string{"kind": "ssh"},
				"spec": map[string]any{"accessProfileId": sshProfile.ID, "accessTarget": map[string]any{"endpoint": server.Addr(),
					"trust": []string{server.Fingerprint}}}}, &host, http.StatusCreated)
			topo.sshHosts = append(topo.sshHosts, host.Metadata.ID)
		}
	}

	// Agent hosts: one agent each, bound by a host-bound enrolment token.
	var agentProfile struct {
		ID string `json:"id"`
	}
	owner.must(t, http.MethodPost, "/api/v1/managed/access-profiles", map[string]any{"name": "ha-agents",
		"spec": map[string]any{"connectorKind": "agent", "enabled": true}}, &agentProfile, http.StatusCreated)
	var key struct {
		PublicKeys []string `json:"publicKeys"`
	}
	waitFor(t, 30*time.Second, "the controller key", func() bool {
		return owner.do(t, http.MethodGet, "/api/v1/agents/controller-key", nil, &key) == http.StatusOK && len(key.PublicKeys) > 0
	})
	hostname, _ := os.Hostname()
	bootstrap := ns.BootstrapCredentials(t)
	for i := range agentCount {
		var host struct {
			Metadata struct {
				ID string `json:"id"`
			} `json:"metadata"`
		}
		owner.must(t, http.MethodPost, "/api/v1/managed/hosts", map[string]any{"name": fmt.Sprintf("agent-%d", i),
			"labels": map[string]string{"kind": "agent"}, "spec": map[string]any{"accessProfileId": agentProfile.ID}}, &host, http.StatusCreated)
		var token struct {
			Token string `json:"token"`
		}
		owner.must(t, http.MethodPost, "/api/v1/agents/enrollment-tokens", map[string]any{"hostname": hostname, "hostId": host.Metadata.ID, "ttlSeconds": 3600},
			&token, http.StatusCreated)
		agent := &process{name: fmt.Sprintf("agent-%d", i), binary: agentBinary, dir: work, env: []string{
			"MACHINARCHY_ENVIRONMENT=development",
			fmt.Sprintf("MACHINARCHY_AGENT_NAME=ha-agent-%d", i),
			"MACHINARCHY_AGENT_NATS_URL=" + ns.URL,
			"MACHINARCHY_AGENT_NATS_CREDENTIALS_FILE=" + bootstrap,
			"MACHINARCHY_AGENT_CONTROLLER_PUBLIC_KEY=" + strings.Join(key.PublicKeys, ","),
			"MACHINARCHY_AGENT_ENROLLMENT_TOKEN_FILE=" + secretFile(t, work, fmt.Sprintf("agent-%d.token", i), token.Token),
			"MACHINARCHY_AGENT_STATE_FILE=" + filepath.Join(work, fmt.Sprintf("agent-%d", i), "state.json"),
		}}
		agent.start(t)
		topo.agents = append(topo.agents, agent)
		topo.agentIDs = append(topo.agentIDs, host.Metadata.ID)
	}
	waitFor(t, 60*time.Second, "every agent connected", func() bool {
		var list struct {
			Items []struct {
				Connected bool `json:"connected"`
			} `json:"items"`
		}
		if owner.do(t, http.MethodGet, "/api/v1/agents", nil, &list) != http.StatusOK || len(list.Items) != agentCount {
			return false
		}
		for _, a := range list.Items {
			if !a.Connected {
				return false
			}
		}
		return true
	})
	return topo
}

func tail(text string, lines int) string {
	all := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}

func (topo *topology) waitReady(name string, port int, timeout time.Duration) {
	topo.t.Helper()
	waitFor(topo.t, timeout, name+" readiness", func() bool { return ready(port) })
}

func ready(port int) bool {
	response, err := (&http.Client{Timeout: 2 * time.Second}).Get(fmt.Sprintf("http://127.0.0.1:%d/healthz/ready", port))
	if err != nil {
		return false
	}
	_ = response.Body.Close()
	return response.StatusCode == http.StatusOK
}

// submit starts an Operation and returns its ID.
func (topo *topology) submit(body map[string]any) string {
	topo.t.Helper()
	var op operation
	topo.owner.must(topo.t, http.MethodPost, "/api/v1/managed/operations", body, &op, http.StatusAccepted)
	return op.ID
}

func (topo *topology) operation(id string) operation {
	topo.t.Helper()
	var op operation
	topo.owner.do(topo.t, http.MethodGet, "/api/v1/managed/operations/"+id, nil, &op)
	return op
}

func (topo *topology) waitTerminal(id string, timeout time.Duration) operation {
	topo.t.Helper()
	var op operation
	// Poll once a second, as the CLI's --wait does.
	deadline := time.Now().Add(timeout)
	for {
		op = topo.operation(id)
		if op.terminal() {
			return op
		}
		if time.Now().After(deadline) {
			topo.t.Fatalf("timed out after %s waiting for Operation %s to finish", timeout, id)
		}
		time.Sleep(time.Second)
	}
}

// leaseOwner returns the current owner and fencing token of a lease.
func (topo *topology) leaseOwner(kind, id string) (owner string, token int64, expires time.Time) {
	var ns int64
	err := topo.db.QueryRow(`SELECT owner_id, fencing_token, expires_at_ns FROM resource_leases WHERE resource_kind = $1 AND resource_id = $2`, kind, id).
		Scan(&owner, &token, &ns)
	if err != nil {
		return "", 0, time.Time{}
	}
	return owner, token, time.Unix(0, ns)
}

// cmByOwner maps a worker lease owner to its Control Manager.
func (topo *topology) cmByOwner(owner string) int {
	for i, p := range topo.cm {
		if p.logField("operation worker started", "owner_id") == owner {
			return i
		}
	}
	return -1
}

// relaunches reports detached targets launched more than once, or never:
// each target must have exactly one launch, whichever replica made it.
func (o operation) relaunches() []string {
	var bad []string
	for _, target := range o.Status.Targets {
		switch {
		case target.Launch == nil:
			bad = append(bad, target.Spec.HostID+": no launch")
		case target.Launch.Attempt > 1:
			bad = append(bad, fmt.Sprintf("%s: launched %d times", target.Spec.HostID, target.Launch.Attempt))
		}
	}
	return bad
}

func (topo *topology) metric(port int, name string) float64 {
	response, err := (&http.Client{Timeout: 5 * time.Second}).Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", port))
	if err != nil {
		return -1
	}
	defer response.Body.Close()
	total := 0.0
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, name) {
			var value float64
			if _, err := fmt.Sscan(line[strings.LastIndex(line, " ")+1:], &value); err == nil {
				total += value
			}
		}
	}
	return total
}

// startPostgres runs a PostgreSQL container on a named volume for this test
// and returns its URL and container name.
func startPostgres(t *testing.T) (string, string) {
	t.Helper()
	image := os.Getenv("MACHINARCHY_HA_PG_IMAGE")
	if image == "" {
		image = "postgres:16-alpine"
	}
	name := "machinarchy-ha-pg-" + randomHex(t, 4)
	password := randomHex(t, 16)
	port := freePort(t)
	docker := func(args ...string) (string, error) {
		out, err := exec.Command("docker", args...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if out, err := docker("run", "-d", "--name", name, "-v", name+":/var/lib/postgresql/data",
		"-e", "POSTGRES_PASSWORD="+password, "-e", "POSTGRES_DB=ha", "-p", fmt.Sprintf("127.0.0.1:%d:5432", port), image); err != nil {
		t.Fatalf("start PostgreSQL container: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_, _ = docker("rm", "-f", "-v", name)
		_, _ = docker("volume", "rm", name)
	})
	url := fmt.Sprintf("postgres://postgres:%s@127.0.0.1:%d/ha?sslmode=disable", password, port)
	waitFor(t, 60*time.Second, "PostgreSQL to accept connections", func() bool {
		db, err := sql.Open("pgx", url)
		if err != nil {
			return false
		}
		defer db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return db.PingContext(ctx) == nil
	})
	return url, name
}

// serverLatency is an Operation's created-to-finished time as the server
// recorded it, so client polling does not distort it.
func (topo *topology) serverLatency(id string) (time.Duration, bool) {
	var created, finished sql.NullString
	if err := topo.db.QueryRow(`SELECT created_at, finished_at FROM operations WHERE id = $1`, id).Scan(&created, &finished); err != nil ||
		!created.Valid || !finished.Valid {
		return 0, false
	}
	c, err1 := time.Parse(time.RFC3339Nano, created.String)
	f, err2 := time.Parse(time.RFC3339Nano, finished.String)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return f.Sub(c), true
}

// cmName names the Control Manager whose worker or engine ID is owner.
func (topo *topology) cmName(owner string) string {
	if owner == "" {
		return "none"
	}
	for _, p := range topo.cm {
		if p.logField("operation worker started", "owner_id") == owner || p.logField("control manager engine started", "instance_id") == owner {
			return p.name
		}
	}
	return "unknown"
}
