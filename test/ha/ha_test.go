package ha

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestHighAvailability runs the Phase 20 scenarios in order on one topology:
// 3 SSH servers with 2 hosts each, 3 agents, 2 API replicas, 2 Control
// Managers, PostgreSQL and NATS. Each scenario logs what it observed;
// AUDIT/20-ha.md records the results.
func TestHighAvailability(t *testing.T) {
	enabled(t)
	began := time.Now()
	topo := newTopology(t, 3, 2, 3)
	t.Logf("topology up in %s: %d SSH hosts, %d agent hosts", time.Since(began).Round(time.Second), len(topo.sshHosts), len(topo.agentIDs))

	t.Run("baseline", func(t *testing.T) { baseline(t, topo) })
	t.Run("kill a Control Manager mid-Operation", func(t *testing.T) { killControlManager(t, topo) })
	t.Run("restart PostgreSQL", func(t *testing.T) { restartPostgres(t, topo) })
	t.Run("partition NATS", func(t *testing.T) { partitionNATS(t, topo) })
	t.Run("SIGTERM during an outbox burst", func(t *testing.T) { sigtermDuringOutboxBurst(t, topo) })
	t.Run("load profile", func(t *testing.T) { loadProfile(t, topo) })
}

// baseline: every kind of Operation succeeds on the healthy topology.
func baseline(t *testing.T, topo *topology) {
	capability := topo.submit(map[string]any{"hostIds": topo.agentIDs, "capability": map[string]any{"name": "runtime.info", "timeoutSeconds": 20}})
	detached := topo.submit(map[string]any{"hostIds": topo.sshHosts, "run": map[string]any{"argv": []string{"sleep", "2"},
		"timeoutSeconds": 60, "concurrency": 6, "detach": true}})
	attached := topo.submit(map[string]any{"hostIds": topo.sshHosts, "run": map[string]any{"argv": []string{"uptime"},
		"timeoutSeconds": 30, "concurrency": 6}})
	for name, id := range map[string]string{"capability": capability, "detached": detached, "attached": attached} {
		op := topo.waitTerminal(id, 2*time.Minute)
		if op.Status.State != "Succeeded" {
			t.Errorf("%s Operation %s: %s %v", name, id, op.Status.State, op.states())
		}
	}
	if bad := topo.operation(detached).relaunches(); len(bad) > 0 {
		t.Errorf("detached launches: %v", bad)
	}
}

// restartCM brings a stopped Control Manager back and waits until it is ready.
func (topo *topology) restartCM(t *testing.T, i int) {
	t.Helper()
	topo.cm[i].start(t)
	topo.waitReady(topo.cm[i].name, topo.cmPorts[i], 60*time.Second)
}

// leaseHolders maps the Control Managers holding leases of one kind on ids.
func (topo *topology) leaseHolders(kind string, ids []string) map[int][]int64 {
	holders := map[int][]int64{}
	for _, id := range ids {
		owner, token, _ := topo.leaseOwner(kind, id)
		if owner == "" {
			continue
		}
		holders[topo.cmByOwner(owner)] = append(holders[topo.cmByOwner(owner)], token)
	}
	return holders
}

func allRunning(op operation) bool {
	if len(op.Status.Targets) == 0 {
		return false
	}
	for _, target := range op.Status.Targets {
		if target.State != "Running" {
			return false
		}
	}
	return true
}

// killControlManager: a Control Manager crashes (SIGKILL) while it runs
// Operations. The other one must finish them: detached runs keep running on
// the hosts and are polled to completion, attached runs end Interrupted, and
// capability results arrive once each.
func killControlManager(t *testing.T, topo *topology) {
	t.Run("detached SSH run", func(t *testing.T) {
		id := topo.submit(map[string]any{"hostIds": topo.sshHosts, "run": map[string]any{"argv": []string{"sleep", "40"},
			"timeoutSeconds": 300, "concurrency": 6, "detach": true}})
		waitFor(t, time.Minute, "every target launched", func() bool { return allRunning(topo.operation(id)) })
		var holders map[int][]int64
		waitFor(t, 30*time.Second, "pollers holding host leases", func() bool {
			holders = topo.leaseHolders("detached-host", topo.sshHosts)
			return len(holders) > 0
		})
		victim := -1
		for i := range holders {
			if i >= 0 {
				victim = i
				break
			}
		}
		if victim < 0 {
			t.Fatalf("no Control Manager holds a detached-host lease: %v", holders)
		}
		killed := time.Now()
		topo.cm[victim].kill()
		t.Logf("killed %s holding %d of %d host leases (tokens %v)", topo.cm[victim].name, len(holders[victim]), len(topo.sshHosts), holders[victim])
		op := topo.waitTerminal(id, 5*time.Minute)
		t.Logf("detached Operation finished %s %v, %s after the kill", op.Status.State, op.states(), time.Since(killed).Round(time.Second))
		if op.Status.State != "Succeeded" {
			t.Errorf("detached Operation after a Control Manager crash: %s %v", op.Status.State, op.states())
		}
		if bad := op.relaunches(); len(bad) > 0 {
			t.Errorf("a command ran twice or never: %v", bad)
		}
		topo.restartCM(t, victim)
	})

	t.Run("attached SSH run", func(t *testing.T) {
		id := topo.submit(map[string]any{"hostIds": topo.sshHosts[:4], "run": map[string]any{"argv": []string{"sleep", "30"},
			"timeoutSeconds": 120, "concurrency": 4}})
		waitFor(t, time.Minute, "every target running", func() bool { return allRunning(topo.operation(id)) })
		owner, token, expires := topo.leaseOwner("operation", id)
		victim := topo.cmByOwner(owner)
		if victim < 0 {
			t.Fatalf("Operation lease owner %q is no Control Manager", owner)
		}
		killed := time.Now()
		topo.cm[victim].kill()
		t.Logf("killed %s owning the Operation (fencing token %d, lease expires in %s)", topo.cm[victim].name, token, time.Until(expires).Round(time.Second))
		op := topo.waitTerminal(id, 5*time.Minute)
		t.Logf("attached Operation finished %s %v, %s after the kill", op.Status.State, op.states(), time.Since(killed).Round(time.Second))
		if op.states()["Running"] > 0 {
			t.Errorf("targets left Running: %v", op.states())
		}
		if newOwner, newToken, _ := topo.leaseOwner("operation", id); newOwner != "" && newOwner != owner && newToken <= token {
			t.Errorf("the survivor took the lease with token %d, not above %d: fencing broken", newToken, token)
		}
		topo.restartCM(t, victim)
	})

	t.Run("capability burst", func(t *testing.T) {
		var ids []string
		for range 5 {
			ids = append(ids, topo.submit(map[string]any{"hostIds": topo.agentIDs, "capability": map[string]any{"name": "runtime.info", "timeoutSeconds": 20}}))
		}
		topo.cm[0].kill()
		for _, id := range ids {
			if op := topo.waitTerminal(id, 5*time.Minute); op.Status.State != "Succeeded" {
				t.Errorf("capability Operation %s after a crash: %s %v", id, op.Status.State, op.states())
			}
		}
		var requests int
		if err := topo.db.QueryRow(`SELECT count(*) FROM operation_agent_commands WHERE type = 'operation.request' AND operation_id = ANY($1)`,
			ids).Scan(&requests); err != nil {
			t.Fatal(err)
		}
		if want := len(ids) * len(topo.agentIDs); requests != want {
			t.Errorf("%d request commands for %d targets", requests, want)
		}
		topo.restartCM(t, 0)
	})
}

// restartPostgres: the database restarts under load. Readiness must report
// it (the "readiness failing" alert), every process must survive or come
// back, and running Operations must still finish.
func restartPostgres(t *testing.T, topo *topology) {
	container := topo.pgName
	id := topo.submit(map[string]any{"hostIds": topo.sshHosts, "run": map[string]any{"argv": []string{"sleep", "20"},
		"timeoutSeconds": 300, "concurrency": 6, "detach": true}})
	waitFor(t, time.Minute, "every target launched", func() bool { return allRunning(topo.operation(id)) })

	var notReady atomic.Int32
	stop := make(chan struct{})
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		for {
			select {
			case <-stop:
				return
			case <-time.After(200 * time.Millisecond):
			}
			for _, p := range topo.api {
				if port := apiPort(p); port > 0 && !ready(port) {
					notReady.Add(1)
				}
			}
		}
	}()
	began := time.Now()
	if out, err := exec.Command("docker", "restart", "-t", "2", container).CombinedOutput(); err != nil {
		t.Fatalf("docker restart: %v\n%s", err, out)
	}
	t.Logf("PostgreSQL restarted in %s", time.Since(began).Round(100*time.Millisecond))
	for i, p := range topo.api {
		topo.waitReady(p.name, apiPort(p), 2*time.Minute)
		_ = i
	}
	recovered := time.Since(began)
	close(stop)
	<-watched
	t.Logf("API readiness failed in %d checks during the restart; ready again %s after it began", notReady.Load(), recovered.Round(100*time.Millisecond))
	if notReady.Load() == 0 {
		t.Error("readiness never reported the database outage")
	}
	for _, p := range append(append(append([]*process{}, topo.api...), topo.cm...), topo.agents...) {
		if !p.alive() {
			t.Errorf("%s exited during the database restart: %v", p.name, p.exitErr())
		}
	}
	op := topo.waitTerminal(id, 5*time.Minute)
	if op.Status.State != "Succeeded" || len(op.relaunches()) > 0 {
		t.Errorf("detached Operation across the restart: %s %v %v", op.Status.State, op.states(), op.relaunches())
	}
	after := topo.submit(map[string]any{"hostIds": topo.agentIDs, "capability": map[string]any{"name": "runtime.info", "timeoutSeconds": 20}})
	if op := topo.waitTerminal(after, 2*time.Minute); op.Status.State != "Succeeded" {
		t.Errorf("capability Operation after the restart: %s %v", op.Status.State, op.states())
	}
}

func apiPort(p *process) int {
	for _, kv := range p.env {
		if strings.HasPrefix(kv, "MACHINARCHY_LISTEN_ADDR=127.0.0.1:") {
			var port int
			fmt.Sscan(strings.TrimPrefix(kv, "MACHINARCHY_LISTEN_ADDR=127.0.0.1:"), &port)
			return port
		}
	}
	return 0
}

// partitionNATS: one Control Manager loses NATS for 30 s. Agents and the
// other gateway keep working (the queue group shares agent traffic), and the
// cut-off replica rejoins.
func partitionNATS(t *testing.T, topo *topology) {
	const renewals = "machinarchy_agent_gateway_lease_renewals_total"
	before := []float64{topo.metric(topo.cmPorts[0], renewals), topo.metric(topo.cmPorts[1], renewals)}
	t.Logf("lease renewals answered before the partition: cm-1 %.0f, cm-2 %.0f", before[0], before[1])
	topo.cmProxy[0].Cut()
	cut := time.Now()
	var ids []string
	for range 3 {
		ids = append(ids, topo.submit(map[string]any{"hostIds": topo.agentIDs, "capability": map[string]any{"name": "runtime.info", "timeoutSeconds": 20}}))
	}
	for _, id := range ids {
		op := topo.waitTerminal(id, 3*time.Minute)
		t.Logf("capability Operation during the partition: %s %v after %s", op.Status.State, op.states(), time.Since(cut).Round(100*time.Millisecond))
		if op.Status.State != "Succeeded" {
			t.Errorf("capability Operation during the partition: %s %v", op.Status.State, op.states())
		}
	}
	t.Logf("cm-1 readiness while cut off from NATS: %v", ready(topo.cmPorts[0]))
	time.Sleep(time.Until(cut.Add(30 * time.Second)))
	topo.cmProxy[0].Heal()
	healed := topo.metric(topo.cmPorts[0], renewals)
	waitFor(t, 90*time.Second, "cm-1 answering agents again", func() bool { return topo.metric(topo.cmPorts[0], renewals) > healed })
	after := topo.submit(map[string]any{"hostIds": topo.agentIDs, "capability": map[string]any{"name": "runtime.info", "timeoutSeconds": 20}})
	if op := topo.waitTerminal(after, 2*time.Minute); op.Status.State != "Succeeded" {
		t.Errorf("capability Operation after healing: %s %v", op.Status.State, op.states())
	}
	for i, p := range topo.agents {
		if !p.alive() {
			t.Errorf("agent %d exited during the partition: %v", i, p.exitErr())
		}
	}
}

// sigtermDuringOutboxBurst: a Control Manager is stopped gracefully while
// hosts are being created. Every host is still reconciled (by the other
// replica) and no outbox delivery is left behind.
func sigtermDuringOutboxBurst(t *testing.T, topo *topology) {
	const total = 120
	defer func() {
		if !topo.cm[0].alive() {
			topo.restartCM(t, 0)
		}
	}()
	var created atomic.Int32
	var wg sync.WaitGroup
	jobs := make(chan int)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				var host struct{}
				if status := topo.owner.do(t, http.MethodPost, "/api/v1/managed/hosts", map[string]any{"name": fmt.Sprintf("burst-%03d", i),
					"labels": map[string]string{"burst": "yes"}, "spec": map[string]any{}}, &host); status != http.StatusCreated {
					t.Errorf("create burst-%03d: %d", i, status)
				}
				created.Add(1)
			}
		}()
	}
	stopped := make(chan error, 1)
	go func() {
		for i := range total {
			if i == total/3 {
				go func() { stopped <- topo.cm[0].stop(60 * time.Second) }()
			}
			jobs <- i
		}
		close(jobs)
	}()
	wg.Wait()
	if err := <-stopped; err != nil {
		t.Errorf("cm-1 did not stop cleanly on SIGTERM during the burst: %v", err)
	}
	t.Logf("created %d hosts; cm-1 stopped after about %d", created.Load(), total/3)
	type burstHost struct {
		Metadata struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Generation uint64 `json:"generation"`
		} `json:"metadata"`
		Status struct {
			ObservedGeneration uint64            `json:"observedGeneration"`
			Conditions         []json.RawMessage `json:"conditions"`
		} `json:"status"`
	}
	unreconciled := func() (int, []burstHost) {
		var page struct {
			Items []burstHost `json:"items"`
		}
		if topo.owner.do(t, http.MethodGet, "/api/v1/managed/hosts?selector=burst%3Dyes&limit=200", nil, &page) != http.StatusOK {
			return -1, nil
		}
		var stuck []burstHost
		for _, h := range page.Items {
			if h.Status.ObservedGeneration != h.Metadata.Generation {
				stuck = append(stuck, h)
			}
		}
		return len(page.Items), stuck
	}
	began := time.Now()
	deadline := began.Add(7 * time.Minute) // longer than one periodic resync (5 min)
	for {
		listed, stuck := unreconciled()
		if listed == total && len(stuck) == 0 {
			t.Logf("all %d burst hosts reconciled %s after the burst", total, time.Since(began).Round(time.Second))
			break
		}
		if time.Now().After(deadline) {
			for i, h := range stuck {
				if i == 5 {
					break
				}
				t.Logf("stuck: %s generation %d observed %d conditions %s", h.Metadata.Name, h.Metadata.Generation, h.Status.ObservedGeneration, h.Status.Conditions)
				rows, err := topo.db.Query(`SELECT d.consumer_group, d.attempts, d.delivered_at_ns, d.quarantined_at_ns, d.lease_owner, d.last_error_class
					FROM outbox_events e JOIN outbox_deliveries d ON d.event_id = e.event_id WHERE e.subject_id = $1`, h.Metadata.ID)
				if err == nil {
					for rows.Next() {
						var group, owner, lastErr string
						var attempts int
						var delivered, quarantined int64
						_ = rows.Scan(&group, &attempts, &delivered, &quarantined, &owner, &lastErr)
						t.Logf("  delivery %s attempts=%d delivered=%v quarantined=%v lease_owner=%s (%s) error=%q", group, attempts, delivered > 0,
							quarantined > 0, owner, topo.cmName(owner), lastErr)
					}
					_ = rows.Close()
				} else {
					t.Logf("  deliveries: %v", err)
				}
				owner, token, expires := topo.leaseOwner("host", h.Metadata.ID)
				t.Logf("  host lease owner=%s (%s) token=%d expires in %s", owner, topo.cmName(owner), token, time.Until(expires).Round(time.Second))
			}
			resyncOwner, _, resyncExpires := topo.leaseOwner("resync", "managed-hosts")
			t.Logf("resync lease owner=%s (%s) expires in %s", resyncOwner, topo.cmName(resyncOwner), time.Until(resyncExpires).Round(time.Second))
			t.Fatalf("%d of %d listed burst hosts not reconciled after %s", len(stuck), listed, time.Since(began).Round(time.Second))
		}
		if time.Since(began) > 3*time.Minute && len(stuck) > 0 {
			t.Logf("%s after the burst: %d hosts still unreconciled", time.Since(began).Round(time.Second), len(stuck))
		}
		time.Sleep(5 * time.Second)
	}
	var pending int
	waitFor(t, time.Minute, "no undelivered outbox events", func() bool {
		_ = topo.db.QueryRow(`SELECT count(*) FROM outbox_deliveries WHERE consumer_group = 'control-manager' AND delivered_at_ns = 0 AND quarantined_at_ns = 0`).Scan(&pending)
		return pending == 0
	})
}

// loadProfile submits a steady mix for a minute and reports throughput and
// submit-to-finish latency per kind. On a laptop the numbers are indicative;
// AUDIT/20 records them with the hardware.
func loadProfile(t *testing.T, topo *topology) {
	type sample struct {
		kind    string
		latency time.Duration
		state   string
	}
	kinds := map[string]map[string]any{
		"capability (3 agents)": {"hostIds": topo.agentIDs, "capability": map[string]any{"name": "runtime.info", "timeoutSeconds": 20}},
		"detached sleep 1 (6 hosts)": {"hostIds": topo.sshHosts, "run": map[string]any{"argv": []string{"sleep", "1"},
			"timeoutSeconds": 120, "concurrency": 6, "detach": true}},
		"attached uptime (6 hosts)": {"hostIds": topo.sshHosts, "run": map[string]any{"argv": []string{"uptime"},
			"timeoutSeconds": 60, "concurrency": 6}},
	}
	// MACHINARCHY_HA_LOAD_KINDS=capability limits the mix (a substring of
	// the kind), e.g. to measure without the SSH test servers' shells.
	if only := os.Getenv("MACHINARCHY_HA_LOAD_KINDS"); only != "" {
		for kind := range kinds {
			if !strings.Contains(kind, only) {
				delete(kinds, kind)
			}
		}
	}
	var mu sync.Mutex
	var samples []sample
	var wg sync.WaitGroup
	// What the database sessions wait on, sampled every 2 s.
	waits := map[string]int{}
	oldest := map[string]float64{}
	sampling := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		for {
			select {
			case <-sampling:
				return
			case <-time.After(2 * time.Second):
			}
			rows, err := topo.db.Query(`SELECT COALESCE(state, ''), COALESCE(wait_event_type, ''), COALESCE(wait_event, ''),
				regexp_replace(left(query, 70), '\s+', ' ', 'g') FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid()`)
			if err != nil {
				continue
			}
			for rows.Next() {
				var state, waitType, wait, query string
				if rows.Scan(&state, &waitType, &wait, &query) == nil && state != "idle" {
					waits[fmt.Sprintf("%s %s/%s: %s", state, waitType, wait, query)]++
				}
			}
			_ = rows.Close()
			// Who holds the audit-chain lock, and for how long its
			// transaction has been open.
			holders, err := topo.db.Query(`SELECT a.state, regexp_replace(left(a.query, 70), '\s+', ' ', 'g'),
				EXTRACT(EPOCH FROM now() - a.xact_start)::float8 FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
				WHERE l.locktype = 'advisory' AND l.granted AND a.pid <> pg_backend_pid()`)
			if err != nil {
				continue
			}
			for holders.Next() {
				var state, query string
				var age float64
				if holders.Scan(&state, &query, &age) == nil {
					key := fmt.Sprintf("HOLDER %s: %s", state, query)
					waits[key]++
					if age > oldest[key] {
						oldest[key] = age
					}
				}
			}
			_ = holders.Close()
		}
	}()
	began := time.Now()
	for time.Since(began) < time.Minute {
		for kind, body := range kinds {
			wg.Add(1)
			go func() {
				defer wg.Done()
				id := topo.submit(body)
				op := topo.waitTerminal(id, 5*time.Minute)
				latency, ok := topo.serverLatency(id)
				if !ok {
					t.Errorf("no server timestamps for Operation %s", id)
					return
				}
				mu.Lock()
				samples = append(samples, sample{kind, latency, op.Status.State})
				mu.Unlock()
			}()
		}
		time.Sleep(2 * time.Second)
	}
	wg.Wait()
	elapsed := time.Since(began)
	close(sampling)
	<-sampled
	type count struct {
		what string
		n    int
	}
	var top []count
	for what, n := range waits {
		top = append(top, count{what, n})
	}
	for i := 1; i < len(top); i++ {
		for j := i; j > 0 && top[j].n > top[j-1].n; j-- {
			top[j], top[j-1] = top[j-1], top[j]
		}
	}
	for i, c := range top {
		if i == 25 {
			break
		}
		t.Logf("  db sessions (sum over samples) %4d  %s", c.n, c.what)
	}
	for what, age := range oldest {
		t.Logf("  audit lock %s: seen %d times, oldest transaction %.1fs", what, waits[what], age)
	}
	if rows, err := topo.db.Query(`SELECT action, count(*) FROM audit_events GROUP BY action ORDER BY 2 DESC`); err == nil {
		for rows.Next() {
			var action string
			var n int
			if rows.Scan(&action, &n) == nil {
				t.Logf("  audit events %-36s %d", action, n)
			}
		}
		_ = rows.Close()
	}
	byKind := map[string][]time.Duration{}
	failed := map[string]int{}
	for _, s := range samples {
		byKind[s.kind] = append(byKind[s.kind], s.latency)
		if s.state != "Succeeded" {
			failed[s.kind]++
		}
	}
	t.Logf("load profile: %d Operations in %s (%.0f per minute); latency is created to finished, from the server's timestamps",
		len(samples), elapsed.Round(time.Second), float64(len(samples))/elapsed.Minutes())
	for kind, latencies := range byKind {
		sortDurations(latencies)
		t.Logf("  %-28s n=%3d  p50 %6s  p95 %6s  p99 %6s  max %6s  not succeeded %d", kind, len(latencies),
			pct(latencies, 50), pct(latencies, 95), pct(latencies, 99), latencies[len(latencies)-1].Round(10*time.Millisecond), failed[kind])
		if failed[kind] > 0 {
			t.Errorf("%s: %d Operations did not succeed under load", kind, failed[kind])
		}
	}
}

func sortDurations(d []time.Duration) {
	for i := 1; i < len(d); i++ {
		for j := i; j > 0 && d[j] < d[j-1]; j-- {
			d[j], d[j-1] = d[j-1], d[j]
		}
	}
}

func pct(sorted []time.Duration, p int) time.Duration {
	i := (len(sorted)*p + 99) / 100
	if i < 1 {
		i = 1
	}
	return sorted[i-1].Round(10 * time.Millisecond)
}
