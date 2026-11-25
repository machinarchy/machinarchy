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
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy/internal/config"
)

// Requested reports whether args ask for the health check.
func Requested(args []string) bool { return len(args) > 0 && args[0] == "healthcheck" }

// Run probes the process listening on its configured address and returns the
// exit code. args is the command line as the server gets it ("healthcheck"
// first); the address is resolved like the server does: the --listen-addr
// flag, MACHINARCHY_LISTEN_ADDR, the --config bootstrap file, then :8080. To
// probe a server started with flags, pass the same flags after "healthcheck".
func Run(args []string, stderr io.Writer) int {
	if len(args) > 0 {
		args = args[1:]
	}
	address, err := config.ListenAddr(args)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "unhealthy:", err)
		return 1
	}
	address = strings.TrimSpace(address)
	if err := Probe(context.Background(), address, 3*time.Second); err != nil {
		_, _ = fmt.Fprintln(stderr, "unhealthy:", err)
		return 1
	}
	return 0
}

// Probe GETs http://<address>/healthz/ready. Only the process's own listen
// address is probed: the host must be an IP literal (wildcards map to
// loopback) and the port numeric, so the target cannot be redirected.
func Probe(ctx context.Context, address string, timeout time.Duration) error {
	host, rawPort, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", address, err)
	}
	port, err := strconv.ParseUint(rawPort, 10, 16)
	if err != nil || port == 0 {
		return fmt.Errorf("invalid listen port %q", rawPort)
	}
	ip := net.IPv4(127, 0, 0, 1)
	if parsed := net.ParseIP(host); parsed != nil && !parsed.IsUnspecified() {
		ip = parsed
	} else if host != "" && parsed == nil {
		return fmt.Errorf("listen host %q must be an IP address for the health check", host)
	}
	target := url.URL{Scheme: "http", Host: net.JoinHostPort(ip.String(), strconv.FormatUint(port, 10)), Path: "/healthz/ready"}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return err
	}
	// #nosec G704 -- the target is this process's own listen address (IP literal and numeric port, validated above).
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
