package runtimeconfig_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/machinarchy/machinarchy/internal/runtimeconfig"
)

// recorder logs every call it receives, in order, across participants.
type recorder struct {
	name       string
	log        *[]string
	mu         *sync.Mutex
	failCommit bool
	failPrep   bool
}

func (r *recorder) note(call string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	*r.log = append(*r.log, r.name+"."+call)
}

func (r *recorder) Prepare(context.Context, runtimeconfig.Snapshot) (runtimeconfig.PreparedApply, error) {
	if r.failPrep {
		r.note("prepare-failed")
		return runtimeconfig.PreparedApply{}, errors.New("refused")
	}
	r.note("prepare")
	return runtimeconfig.PreparedApply{
		Commit: func() error {
			if r.failCommit {
				r.note("commit-failed")
				return errors.New("commit failed")
			}
			r.note("commit")
			return nil
		},
		Rollback: func() { r.note("rollback") },
		Abort:    func() { r.note("abort") },
	}, nil
}

// TestCommitFailureRollsBackOnlyWhatWasApplied (AUDIT Q-16): when the second
// of three participants fails to commit, the first (committed) and the
// second (possibly half-applied) are rolled back, newest first; the third,
// never committed, is aborted and never rolled back.
func TestCommitFailureRollsBackOnlyWhatWasApplied(t *testing.T) {
	ctx := context.Background()
	var log []string
	var mu sync.Mutex
	a := &recorder{name: "a", log: &log, mu: &mu}
	b := &recorder{name: "b", log: &log, mu: &mu}
	c := &recorder{name: "c", log: &log, mu: &mu}
	service, _ := newService(t, repository(t), a, b, c)
	if err := service.Bootstrap(ctx, runtimeconfig.Defaults()); err != nil {
		t.Fatal(err)
	}
	log = nil
	b.failCommit = true
	next := runtimeconfig.Defaults()
	next.Logging.Level = "debug"
	if _, err := service.Publish(ctx, owner, 1, next, ""); !errors.Is(err, runtimeconfig.ErrApplyPending) {
		t.Fatalf("publish = %v, want ErrApplyPending", err)
	}
	want := []string{"a.prepare", "b.prepare", "c.prepare", "a.commit", "b.commit-failed", "b.rollback", "a.rollback", "c.abort"}
	if !slices.Equal(log, want) {
		t.Fatalf("calls = %v\nwant    %v", log, want)
	}
}

// TestPrepareFailureAbortsWithoutRollback: nothing was committed, so the
// prepared participants are aborted, never rolled back.
func TestPrepareFailureAbortsWithoutRollback(t *testing.T) {
	ctx := context.Background()
	var log []string
	var mu sync.Mutex
	a := &recorder{name: "a", log: &log, mu: &mu}
	b := &recorder{name: "b", log: &log, mu: &mu}
	c := &recorder{name: "c", log: &log, mu: &mu}
	service, _ := newService(t, repository(t), a, b, c)
	if err := service.Bootstrap(ctx, runtimeconfig.Defaults()); err != nil {
		t.Fatal(err)
	}
	log = nil
	c.failPrep = true
	next := runtimeconfig.Defaults()
	next.Logging.Level = "debug"
	if _, err := service.Publish(ctx, owner, 1, next, ""); !errors.Is(err, runtimeconfig.ErrApplyPending) {
		t.Fatalf("publish = %v, want ErrApplyPending", err)
	}
	want := []string{"a.prepare", "b.prepare", "c.prepare-failed", "b.abort", "a.abort"}
	if !slices.Equal(log, want) {
		t.Fatalf("calls = %v\nwant    %v", log, want)
	}
}
