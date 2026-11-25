package runtimeconfig

import (
	"testing"
	"time"
)

func TestNextRetryDelayDoublesAndCaps(t *testing.T) {
	delay := time.Second
	for i := 0; i < 5; i++ {
		delay = nextRetryDelay(delay)
	}
	if want := 30 * time.Second; delay != want {
		t.Fatalf("retry delay=%s, want capped delay %s", delay, want)
	}
	if got := nextRetryDelay(delay); got != delay {
		t.Fatalf("retry delay exceeded cap: got %s after %s", got, delay)
	}
}

func TestSSHAllowedPortsRequireExplicitBoundedUniqueValues(t *testing.T) {
	config := Defaults()
	if err := config.Validate(); err != nil {
		t.Fatalf("default configuration is invalid: %v", err)
	}
	config.SSH.AllowedPorts = []int{22, 2222}
	if err := config.Validate(); err != nil {
		t.Fatalf("valid SSH port allowlist was rejected: %v", err)
	}
	if Classifications()["ssh.allowedPorts"] != HotReloadable {
		t.Fatal("SSH allowed ports must be classified as hot-reloadable")
	}
	for _, ports := range [][]int{{0}, {65536}, {22, 22}} {
		config.SSH.AllowedPorts = ports
		if err := config.Validate(); err == nil {
			t.Errorf("invalid allowed ports %v were accepted", ports)
		}
	}
}
