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
