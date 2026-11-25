package healthcheck

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProbe(t *testing.T) {
	ready := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz/ready" || !ready {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))

	if err := Probe(context.Background(), ":"+port, time.Second); err != nil {
		t.Fatalf("ready server: %v", err)
	}
	ready = false
	if err := Probe(context.Background(), "0.0.0.0:"+port, time.Second); err == nil {
		t.Fatal("unready server passed")
	}
	for _, bad := range []string{"nonsense", "example.com:" + port, "127.0.0.1:http", "127.0.0.1:0"} {
		if err := Probe(context.Background(), bad, time.Second); err == nil {
			t.Fatalf("%q passed", bad)
		}
	}
	t.Setenv("MACHINARCHY_LISTEN_ADDR", "127.0.0.1:"+port)
	var stderr bytes.Buffer
	if code := Run([]string{"healthcheck"}, &stderr); code != 1 || !strings.Contains(stderr.String(), "503") {
		t.Fatalf("Run() = %d, %q", code, stderr.String())
	}
	if !Requested([]string{"healthcheck"}) || Requested(nil) || Requested([]string{"--healthcheck"}) {
		t.Fatal("Requested")
	}
}

// AUDIT F14-02: the probe uses the address the server was started with, from
// a flag or the bootstrap file as well as the environment, with the server's
// precedence (flag, environment, file, default).
func TestRunResolvesTheListenAddressLikeTheServer(t *testing.T) {
	ready := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !ready {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	address := "127.0.0.1:" + port
	t.Setenv("MACHINARCHY_LISTEN_ADDR", "")
	file := filepath.Join(t.TempDir(), "bootstrap.json")
	if err := os.WriteFile(file, []byte(`{"MACHINARCHY_LISTEN_ADDR":"`+address+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (int, string) {
		var stderr bytes.Buffer
		return Run(append([]string{"healthcheck"}, args...), &stderr), stderr.String()
	}
	if code, out := run("--listen-addr", address); code != 0 {
		t.Errorf("flag: %d %s", code, out)
	}
	if code, out := run("--listen-addr=" + address); code != 0 {
		t.Errorf("flag with =: %d %s", code, out)
	}
	if code, out := run("--config", file); code != 0 {
		t.Errorf("bootstrap file: %d %s", code, out)
	}
	// The environment wins over the file; the flag wins over both.
	t.Setenv("MACHINARCHY_LISTEN_ADDR", "127.0.0.1:1")
	if code, _ := run("--config", file); code == 0 {
		t.Error("environment did not override the file")
	}
	if code, out := run("--config", file, "--listen-addr", address); code != 0 {
		t.Errorf("flag over environment: %d %s", code, out)
	}
	if code, out := run("--no-such-flag"); code != 1 || !strings.Contains(out, "unknown startup flag") {
		t.Errorf("bad flag: %d %q", code, out)
	}
}
