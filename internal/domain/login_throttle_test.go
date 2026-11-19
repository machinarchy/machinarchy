package domain

import (
	"errors"
	"testing"
	"time"
)

func TestAccountDelayBacksOffAndCaps(t *testing.T) {
	p := DefaultLoginThrottlePolicy()
	for failures, want := range map[int]time.Duration{
		1: 0, 5: 0, 6: time.Second, 7: 2 * time.Second, 8: 4 * time.Second, 15: 512 * time.Second, 16: 15 * time.Minute, 100: 15 * time.Minute,
	} {
		if got := p.AccountDelay(failures); got != want {
			t.Errorf("AccountDelay(%d) = %s, want %s", failures, got, want)
		}
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []LoginThrottlePolicy{
		{}, {FreeFailures: 5, BaseDelay: time.Second, MaxDelay: time.Millisecond, IPMaxFailures: 20, Window: time.Hour},
		{FreeFailures: 5, BaseDelay: time.Second, MaxDelay: time.Minute, IPMaxFailures: 0, Window: time.Hour},
		{FreeFailures: 5, BaseDelay: time.Second, MaxDelay: time.Minute, IPMaxFailures: 20, Window: time.Second},
	} {
		if err := bad.Validate(); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func TestClientKeyGroupsIPv6By64(t *testing.T) {
	for in, want := range map[string]string{
		"192.0.2.7":                "192.0.2.7",
		"::ffff:192.0.2.7":         "192.0.2.7",
		"2001:db8:1:2:aaaa::1":     "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff:ffff::": "2001:db8:1:2::/64",
		"2001:db8:1:3::1":          "2001:db8:1:3::/64",
		"not-an-ip":                "not-an-ip",
		"":                         "",
	} {
		if got := ClientKey(in); got != want {
			t.Errorf("ClientKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestThrottledErrorMatches(t *testing.T) {
	var err error = &ThrottledError{RetryAfter: 1500 * time.Millisecond}
	if !errors.Is(err, ErrLoginThrottled) || err.Error() != "too many failed logins; retry after 2s" {
		t.Fatalf("error = %v", err)
	}
}
