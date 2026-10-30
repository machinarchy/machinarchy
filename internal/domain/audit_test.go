package domain

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func sampleAuditEvent() AuditEvent {
	return AuditEvent{
		WorkspaceID: 1, ActorUserID: 7, ActorUsername: "alice", ActorTokenID: 0,
		RequestID: "req-1", Source: "api", Action: AuditHostCreated,
		TargetType: AuditTargetHost, TargetID: "42", Outcome: AuditOutcomeSuccess,
		Details: map[string]string{"name": "web-01", "address": "10.0.0.1"},
	}
}

func TestComputeAuditHashCoversEveryField(t *testing.T) {
	const at = "2026-10-29T10:00:00.123456789Z"
	base := ComputeAuditHash(AuditGenesisHash, at, sampleAuditEvent())
	if len(base) != 64 {
		t.Fatalf("hash %q is not a SHA-256 hex digest", base)
	}
	if again := ComputeAuditHash(AuditGenesisHash, at, sampleAuditEvent()); again != base {
		t.Error("the hash must be deterministic")
	}

	mutations := map[string]func(*AuditEvent){
		"workspace":    func(e *AuditEvent) { e.WorkspaceID = 2 },
		"actor user":   func(e *AuditEvent) { e.ActorUserID = 8 },
		"actor name":   func(e *AuditEvent) { e.ActorUsername = "mallory" },
		"actor token":  func(e *AuditEvent) { e.ActorTokenID = 3 },
		"request id":   func(e *AuditEvent) { e.RequestID = "req-2" },
		"source":       func(e *AuditEvent) { e.Source = "bootstrap" },
		"action":       func(e *AuditEvent) { e.Action = AuditHostDeleted },
		"target type":  func(e *AuditEvent) { e.TargetType = AuditTargetUser },
		"target id":    func(e *AuditEvent) { e.TargetID = "43" },
		"outcome":      func(e *AuditEvent) { e.Outcome = "denied" },
		"detail value": func(e *AuditEvent) { e.Details = map[string]string{"name": "web-02", "address": "10.0.0.1"} },
		"detail key":   func(e *AuditEvent) { e.Details = map[string]string{"nome": "web-01", "address": "10.0.0.1"} },
		"extra detail": func(e *AuditEvent) { e.Details["extra"] = "x" },
	}
	for name, mutate := range mutations {
		event := sampleAuditEvent()
		mutate(&event)
		if ComputeAuditHash(AuditGenesisHash, at, event) == base {
			t.Errorf("changing the %s did not change the hash", name)
		}
	}
	if ComputeAuditHash(AuditGenesisHash, "2026-10-29T10:00:01Z", sampleAuditEvent()) == base {
		t.Error("changing the timestamp did not change the hash")
	}
	if ComputeAuditHash(strings.Repeat("1", 64), at, sampleAuditEvent()) == base {
		t.Error("changing the previous hash did not change the hash")
	}
}

func TestComputeAuditHashIsUnambiguous(t *testing.T) {
	// Without length prefixes these two events would hash the same input.
	a := AuditEvent{WorkspaceID: 1, Action: "ab", TargetType: "c"}
	b := AuditEvent{WorkspaceID: 1, Action: "a", TargetType: "bc"}
	if ComputeAuditHash(AuditGenesisHash, "t", a) == ComputeAuditHash(AuditGenesisHash, "t", b) {
		t.Error("field boundaries must be part of the hashed input")
	}
	// Detail order must not matter.
	x := sampleAuditEvent()
	y := sampleAuditEvent()
	y.Details = map[string]string{"address": "10.0.0.1", "name": "web-01"}
	if ComputeAuditHash(AuditGenesisHash, "t", x) != ComputeAuditHash(AuditGenesisHash, "t", y) {
		t.Error("map iteration order must not change the hash")
	}
}

func TestSanitizeAuditDetails(t *testing.T) {
	clean, err := SanitizeAuditDetails(map[string]string{"name": "web\n01\x00", "port": "22"})
	if err != nil {
		t.Fatal(err)
	}
	if clean["name"] != "web 01 " || clean["port"] != "22" {
		t.Errorf("control characters must be neutralized: %q", clean)
	}

	long := strings.Repeat("é", 400) // multi-byte runes must not be cut in the middle
	clean, err = SanitizeAuditDetails(map[string]string{"note": long})
	if err != nil {
		t.Fatal(err)
	}
	if got := []rune(clean["note"]); len(got) != maxAuditDetailValue || got[len(got)-1] != '…' {
		t.Errorf("long values must be truncated to %d runes with an ellipsis, got %d", maxAuditDetailValue, len(got))
	}

	input := map[string]string{"a": "b"}
	out, _ := SanitizeAuditDetails(input)
	out["a"] = "changed"
	if input["a"] != "b" {
		t.Error("the input map must not be shared with the result")
	}

	for name, details := range map[string]map[string]string{
		"upper-case key": {"Name": "x"},
		"key with space": {"a b": "x"},
		"empty key":      {"": "x"},
		"key too long":   {strings.Repeat("a", 33): "x"},
	} {
		if _, err := SanitizeAuditDetails(details); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: error = %v, want ErrInvalidInput", name, err)
		}
	}
	tooMany := map[string]string{}
	for i := 0; i <= maxAuditDetails; i++ {
		tooMany["k"+strings.Repeat("a", i/26)+string(rune('a'+i%26))] = "v"
	}
	if _, err := SanitizeAuditDetails(tooMany); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("too many details error = %v, want ErrInvalidInput", err)
	}
}

func TestNormalizeAuditFilter(t *testing.T) {
	got, err := NormalizeAuditFilter(AuditFilter{})
	if err != nil || got.Limit != DefaultAuditPageSize {
		t.Fatalf("default filter = %+v, %v", got, err)
	}
	if got, _ := NormalizeAuditFilter(AuditFilter{Limit: MaxAuditPageSize * 10}); got.Limit != MaxAuditPageSize {
		t.Errorf("limit must be capped at %d, got %d", MaxAuditPageSize, got.Limit)
	}
	if got, _ := NormalizeAuditFilter(AuditFilter{Action: "  host.created "}); got.Action != "host.created" {
		t.Errorf("action must be trimmed, got %q", got.Action)
	}
	for name, filter := range map[string]AuditFilter{
		"negative limit":  {Limit: -1},
		"negative cursor": {BeforeID: -5},
		"long action":     {Action: strings.Repeat("a", 65)},
	} {
		if _, err := NormalizeAuditFilter(filter); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: error = %v, want ErrInvalidInput", name, err)
		}
	}
}

func TestAuditActorContext(t *testing.T) {
	if _, ok := AuditActorFrom(context.Background()); ok {
		t.Error("a bare context must carry no actor")
	}
	want := AuditActor{UserID: 3, Username: "alice", RequestID: "r", Source: "api"}
	got, ok := AuditActorFrom(WithAuditActor(context.Background(), want))
	if !ok || got != want {
		t.Errorf("actor = %+v, %v; want %+v", got, ok, want)
	}
}

// chain builds n correctly linked stored events.
func chain(n int) []StoredAuditEvent {
	var events []StoredAuditEvent
	prev := AuditGenesisHash
	for i := 1; i <= n; i++ {
		event := sampleAuditEvent()
		event.ID = int64(i)
		event.TargetID = string(rune('a' + i))
		at := time.Date(2026, 10, 29, 10, 0, i, 0, time.UTC).Format(time.RFC3339Nano)
		event.PrevHash = prev
		event.Hash = ComputeAuditHash(prev, at, event)
		events = append(events, StoredAuditEvent{AuditEvent: event, OccurredAtText: at})
		prev = event.Hash
	}
	return events
}

func verify(events []StoredAuditEvent) AuditVerification {
	v := NewAuditChainVerifier()
	for _, e := range events {
		if !v.Add(e) {
			break
		}
	}
	return v.Result()
}

func TestAuditChainVerifier(t *testing.T) {
	empty := verify(nil)
	if !empty.Valid || empty.Events != 0 || empty.HeadHash != AuditGenesisHash {
		t.Errorf("an empty chain is valid with the genesis head: %+v", empty)
	}

	events := chain(5)
	ok := verify(events)
	if !ok.Valid || ok.Events != 5 || ok.HeadHash != events[4].Hash {
		t.Fatalf("intact chain = %+v", ok)
	}

	t.Run("modified event", func(t *testing.T) {
		tampered := chain(5)
		tampered[2].Details = map[string]string{"name": "evil", "address": "10.0.0.1"}
		got := verify(tampered)
		if got.Valid || got.FirstInvalidID != 3 || !strings.Contains(got.Reason, "modified") {
			t.Errorf("result = %+v, want event 3 reported as modified", got)
		}
		if got.Events != 2 {
			t.Errorf("events verified before the failure = %d, want 2", got.Events)
		}
	})
	t.Run("removed event", func(t *testing.T) {
		all := chain(5)
		got := verify(append(append([]StoredAuditEvent{}, all[:2]...), all[3:]...))
		if got.Valid || got.FirstInvalidID != 4 || !strings.Contains(got.Reason, "link") {
			t.Errorf("result = %+v, want event 4 reported as unlinked", got)
		}
	})
	t.Run("reordered events", func(t *testing.T) {
		swapped := chain(4)
		swapped[1], swapped[2] = swapped[2], swapped[1]
		if got := verify(swapped); got.Valid {
			t.Errorf("a reordered chain must not verify: %+v", got)
		}
	})
	t.Run("rewritten history with a recomputed hash still breaks the next link", func(t *testing.T) {
		forged := chain(4)
		forged[1].Details = map[string]string{"name": "evil"}
		forged[1].Hash = ComputeAuditHash(forged[1].PrevHash, forged[1].OccurredAtText, forged[1].AuditEvent)
		got := verify(forged)
		if got.Valid || got.FirstInvalidID != 3 {
			t.Errorf("result = %+v, want the break reported at event 3", got)
		}
	})
	t.Run("truncating the tail is not detectable from the chain alone", func(t *testing.T) {
		full := chain(5)
		short := verify(full[:3])
		if !short.Valid || short.HeadHash == verify(full).HeadHash {
			t.Errorf("a truncated chain verifies but its head differs from the original: %+v", short)
		}
	})
	t.Run("a failed verifier ignores later events", func(t *testing.T) {
		v := NewAuditChainVerifier()
		tampered := chain(3)
		tampered[0].Hash = "bad"
		if v.Add(tampered[0]) || v.Add(tampered[1]) {
			t.Error("Add must keep returning false after a failure")
		}
		if got := v.Result(); got.FirstInvalidID != 1 {
			t.Errorf("first invalid id = %d, want 1", got.FirstInvalidID)
		}
	})
}
