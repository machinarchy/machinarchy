// Package healthcheck implements the "healthcheck" subcommand of the server
// binaries. The images are distroless (no shell, no curl), so a container
// HEALTHCHECK runs the binary itself: it asks the local process for
// /healthz/ready and exits 0 when it is ready.
package healthcheck

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Requested reports whether args ask for the health check.
func Requested(args []string) bool { return len(args) > 0 && args[0] == "healthcheck" }

// Run probes the process listening on MACHINARCHY_LISTEN_ADDR (default
// :8080) and returns the exit code.
func Run(stderr io.Writer) int {
	address := strings.TrimSpace(os.Getenv("MACHINARCHY_LISTEN_ADDR"))
	if address == "" {
		address = ":8080"
	}
	if err := Probe(context.Background(), address, 3*time.Second); err != nil {
		_, _ = fmt.Fprintln(stderr, "unhealthy:", err)
		return 1
	}
	return 0
}

// Probe GETs http://<address>/healthz/ready on loopback.
func Probe(ctx context.Context, address string, timeout time.Duration) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", address, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, port)+"/healthz/ready", nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("readiness returned HTTP %d", response.StatusCode)
	}
	return nil
}
