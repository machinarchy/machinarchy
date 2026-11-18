package healthcheck

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
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
	if err := Probe(context.Background(), "nonsense", time.Second); err == nil {
		t.Fatal("bad address passed")
	}
	t.Setenv("MACHINARCHY_LISTEN_ADDR", "127.0.0.1:"+port)
	var stderr bytes.Buffer
	if code := Run(&stderr); code != 1 || !strings.Contains(stderr.String(), "503") {
		t.Fatalf("Run() = %d, %q", code, stderr.String())
	}
	if !Requested([]string{"healthcheck"}) || Requested(nil) || Requested([]string{"--healthcheck"}) {
		t.Fatal("Requested")
	}
}
