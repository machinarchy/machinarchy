package operationworker

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/hostaccess/runner"
)

// PollConfig tunes the detached-target poller. Zero values take defaults.
type PollConfig struct {
	// MinInterval and MaxInterval bound how often one target is polled: the
	// interval grows from MinInterval with the target's age (5 s for the
	// first minute) up to MaxInterval (30 s).
	MinInterval time.Duration
	MaxInterval time.Duration
	// Tick is how often the poller looks for due targets (1 s).
	Tick time.Duration
	// HostConcurrency bounds hosts polled at once (16).
	HostConcurrency int
	// CallTimeout bounds one SSH session (60 s).
	CallTimeout time.Duration
	// LaunchConfirmGrace is how long an unconfirmed launch may stay
	// invisible on the host before it counts as never started (2 min).
	LaunchConfirmGrace time.Duration
	// TimeoutGrace is how long past its deadline a command may run before
	// the poller cancels it itself (60 s; the host-side watchdog acts at the
	// deadline).
	TimeoutGrace time.Duration
	// UnreachableGrace: a target whose host cannot be reached becomes Lost
	// once its deadline plus this grace has passed, or this long after an
	// Operation cancel (5 min).
	UnreachableGrace time.Duration
}

func (c PollConfig) withDefaults() PollConfig {
	set := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	set(&c.MinInterval, 5*time.Second)
	set(&c.MaxInterval, 30*time.Second)
	set(&c.Tick, time.Second)
	set(&c.CallTimeout, 60*time.Second)
	set(&c.LaunchConfirmGrace, 2*time.Minute)
	set(&c.TimeoutGrace, 60*time.Second)
	set(&c.UnreachableGrace, 5*time.Minute)
	if c.HostConcurrency <= 0 {
		c.HostConcurrency = 16
	}
	return c
}

type targetKey struct {
	operation resource.ID
	host      resource.ID
}

type hostKey struct {
	workspace int
	host      resource.ID
}

type poller struct {
	w         *Worker
	cfg       PollConfig
	connector RunnerConnector
	mu        sync.Mutex
	nextPoll  map[targetKey]time.Time
	inFlight  map[hostKey]bool
}

func (w *Worker) runPoller(ctx context.Context, connector RunnerConnector) {
	p := &poller{w: w, cfg: w.deps.Poll.withDefaults(), connector: connector,
		nextPoll: map[targetKey]time.Time{}, inFlight: map[hostKey]bool{}}
	slots := make(chan struct{}, p.cfg.HostConcurrency)
	var wg sync.WaitGroup
	defer wg.Wait()
	ticker := time.NewTicker(p.cfg.Tick)
	defer ticker.Stop()
	for {
		p.tick(ctx, slots, &wg)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// tick dispatches one poll per host that has due targets and is not being
// polled already.
func (p *poller) tick(ctx context.Context, slots chan struct{}, wg *sync.WaitGroup) {
	targets, err := p.w.detached.ListDetachedTargets(ctx, 5000)
	if err != nil {
		if ctx.Err() == nil {
			p.w.deps.Logger.Warn("list detached targets failed", "error", err)
		}
		return
	}
	p.w.metrics.runningTargets.Set(float64(len(targets)))
	now := time.Now()
	groups := map[hostKey][]resource.DetachedTarget{}
	seen := map[targetKey]bool{}
	p.mu.Lock()
	for _, target := range targets {
		key := targetKey{target.OperationID, target.HostID}
		seen[key] = true
		if next, ok := p.nextPoll[key]; ok && now.Before(next) {
			continue
		}
		host := hostKey{target.WorkspaceID, target.HostID}
		if !p.inFlight[host] {
			groups[host] = append(groups[host], target)
		}
	}
	for key := range p.nextPoll {
		if !seen[key] {
			delete(p.nextPoll, key) // finished
		}
	}
	for host := range groups {
		p.inFlight[host] = true
	}
	p.mu.Unlock()
	hosts := make([]hostKey, 0, len(groups))
	for host := range groups {
		hosts = append(hosts, host)
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].host < hosts[j].host })
	for _, host := range hosts {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			p.mu.Lock()
			for _, h := range hosts {
				delete(p.inFlight, h)
			}
			p.mu.Unlock()
			return
		}
		wg.Add(1)
		go func(host hostKey, targets []resource.DetachedTarget) {
			defer wg.Done()
			defer func() { <-slots }()
			defer func() { p.mu.Lock(); delete(p.inFlight, host); p.mu.Unlock() }()
			p.pollHost(ctx, host, targets)
		}(host, groups[host])
	}
}

func (p *poller) schedule(target resource.DetachedTarget, now time.Time, immediately bool) {
	interval := p.cfg.MinInterval
	if age := now.Sub(target.Launch.LaunchedAt); age > time.Minute {
		interval = max(p.cfg.MinInterval, min(p.cfg.MaxInterval, age/12))
	}
	next := now.Add(interval)
	if immediately {
		next = now
	}
	p.mu.Lock()
	p.nextPoll[targetKey{target.OperationID, target.HostID}] = next
	p.mu.Unlock()
}

// pollHost runs one status session per profile revision (and state root)
// for all of the host's due targets, then the cancels and cleanups they
// need, and finishes Operations whose last target ended.
func (p *poller) pollHost(ctx context.Context, host hostKey, targets []resource.DetachedTarget) {
	started := time.Now()
	defer func() { p.w.metrics.pollDuration.Observe(time.Since(started).Seconds()) }()
	if p.w.deps.Leases != nil {
		lease, acquired, err := p.w.deps.Leases.AcquireResourceLease(ctx, resource.Lease{Kind: "detached-host",
			WorkspaceID: host.workspace, ID: string(host.host), Owner: p.w.owner}, time.Now(), 2*time.Minute)
		if err != nil || !acquired {
			return // another worker polls this host
		}
		defer func() {
			if err := p.w.deps.Leases.ReleaseResourceLease(context.WithoutCancel(ctx), lease); err != nil {
				p.w.deps.Logger.Warn("release detached poll lease failed", "error", err)
			}
		}()
	}
	type batchKey struct {
		profile  resource.ID
		revision uint64
		root     string
	}
	batches := map[batchKey][]resource.DetachedTarget{}
	for _, target := range targets {
		key := batchKey{target.AccessProfileID, target.AccessProfileRevision, target.Launch.Root}
		batches[key] = append(batches[key], target)
	}
	finished := map[resource.OperationKey]bool{}
	for key, batch := range batches {
		for start := 0; start < len(batch); start += runner.MaxRefsPerCall {
			chunk := batch[start:min(start+runner.MaxRefsPerCall, len(batch))]
			for opKey := range p.pollBatch(ctx, host, key.profile, key.revision, key.root, chunk) {
				finished[opKey] = true
			}
		}
	}
	for opKey := range finished {
		p.w.finishDetachedIfDone(context.WithoutCancel(ctx), opKey)
	}
}

// outcome of one target after a poll.
type outcome struct {
	target resource.DetachedTarget
	state  resource.TargetState // "" while it keeps running
	result *resource.TargetResult
	cancel string // "cancelled" or "timeout": send a cancel
	clean  bool   // remove the directory on the host
	soon   bool   // poll again on the next tick
	// relaunch: the launch was cut off before it started anything; launch
	// it again (the runner's lock makes a double start impossible).
	relaunch bool
}

func (p *poller) pollBatch(ctx context.Context, hk hostKey, profileID resource.ID, revisionNumber uint64, root string, batch []resource.DetachedTarget) map[resource.OperationKey]bool {
	now := time.Now().UTC()
	persist := context.WithoutCancel(ctx)
	host, hostErr := p.w.deps.Hosts.GetHost(ctx, hk.workspace, hk.host)
	revision, revisionErr := p.w.deps.Profiles.GetAccessProfileRevision(ctx, hk.workspace, profileID, revisionNumber)
	var statuses map[runner.Ref]runner.Status
	var pollErr error
	switch {
	case hostErr != nil || revisionErr != nil:
		pollErr = errors.Join(hostErr, revisionErr)
	case host.Spec.AccessTarget.Endpoint != batch[0].Launch.Endpoint:
		pollErr = errors.New("the Host's endpoint changed since the launch")
	default:
		statuses, pollErr = p.status(ctx, host, revision, root, batch)
	}
	if ctx.Err() != nil {
		return nil
	}
	if pollErr != nil {
		p.w.metrics.polls.WithLabelValues("unreachable").Inc()
	} else {
		p.w.metrics.polls.WithLabelValues("ok").Inc()
	}
	outcomes := make([]outcome, 0, len(batch))
	for _, target := range batch {
		if pollErr != nil {
			outcomes = append(outcomes, p.unreachable(target, now))
			continue
		}
		status, ok := statuses[runner.Ref{OperationID: string(target.OperationID), TargetID: string(target.HostID)}]
		if !ok {
			outcomes = append(outcomes, p.unreachable(target, now))
			continue
		}
		outcomes = append(outcomes, p.observe(persist, target, status, now))
	}
	// Cancels first (one session), then results, cleanups, and finally the
	// Operations: the credential authorizer needs the Operation active.
	var cancels []runner.Ref
	for i := range outcomes {
		if outcomes[i].cancel != "" {
			cancels = append(cancels, runner.Ref{OperationID: string(outcomes[i].target.OperationID), TargetID: string(outcomes[i].target.HostID)})
		}
	}
	if len(cancels) > 0 && pollErr == nil {
		if command, err := runner.CancelCommand(root, cancels); err == nil {
			if _, err := p.call(ctx, host, revision, batch[0].OperationID, command); err != nil {
				p.w.deps.Logger.Warn("send detached cancel failed", "host_id", hk.host, "error", err)
			} else {
				for i := range outcomes {
					if outcomes[i].cancel != "" {
						sent := now
						outcomes[i].target.Live.CancelSentAt, outcomes[i].target.Live.CancelReason = &sent, outcomes[i].cancel
					}
				}
			}
		}
	}
	// Clean up finished targets on the host before recording their results:
	// the credential authorizer only serves active Operations, and recording
	// the last target finishes the Operation. If the poller stopped between
	// the two, the next poll finds the directory missing and reports Lost:
	// an honest "unknown", never a guessed result.
	var cleanups []runner.Ref
	for _, o := range outcomes {
		if o.state != "" && o.clean {
			cleanups = append(cleanups, runner.Ref{OperationID: string(o.target.OperationID), TargetID: string(o.target.HostID)})
		}
	}
	if len(cleanups) > 0 {
		if command, err := runner.CleanupCommand(root, cleanups); err == nil {
			if _, err := p.call(ctx, host, revision, batch[0].OperationID, command); err != nil {
				// The host's sweep removes it after the retention period.
				p.w.deps.Logger.Info("detached cleanup failed; the host sweep will remove it", "host_id", hk.host, "error", err)
			}
		}
	}
	finished := map[resource.OperationKey]bool{}
	for _, o := range outcomes {
		if o.relaunch {
			p.relaunch(ctx, o.target)
			p.schedule(o.target, now, false)
			continue
		}
		storeLive := func(what string) {
			if err := p.w.detached.UpdateTargetLive(persist, o.target.WorkspaceID, o.target.OperationID, o.target.HostID, o.target.Live); err != nil &&
				!errors.Is(err, resource.ErrOperationFinished) {
				p.w.deps.Logger.Warn("store "+what+" failed", "operation_id", o.target.OperationID, "error", err)
			}
		}
		if o.state == "" {
			storeLive("detached progress")
			p.schedule(o.target, now, o.soon)
			continue
		}
		// Keep the final capture: the output endpoint serves it by offset.
		storeLive("final detached output")
		p.w.completeTarget(persist, resource.OperationKey{WorkspaceID: o.target.WorkspaceID, ID: o.target.OperationID}, o.target.HostID, o.state, o.result)
		if o.state == resource.TargetLost {
			p.w.metrics.lost.Inc()
		}
		finished[resource.OperationKey{WorkspaceID: o.target.WorkspaceID, ID: o.target.OperationID}] = true
	}
	return finished
}

// call runs one runner session within CallTimeout.
func (p *poller) call(ctx context.Context, host *resource.Host, revision *resource.AccessProfileRevision, operationID resource.ID, command string) (string, error) {
	callCtx, cancel := context.WithTimeout(ctx, p.cfg.CallTimeout)
	defer cancel()
	return p.connector.RunRunner(callCtx, host, revision, string(operationID), command)
}

func (p *poller) status(ctx context.Context, host *resource.Host, revision *resource.AccessProfileRevision, root string, batch []resource.DetachedTarget) (map[runner.Ref]runner.Status, error) {
	queries := make([]runner.Query, 0, len(batch))
	for _, target := range batch {
		queries = append(queries, runner.Query{Ref: runner.Ref{OperationID: string(target.OperationID), TargetID: string(target.HostID)},
			StdoutFrom: target.Live.Stdout.NextFrom(), StderrFrom: target.Live.Stderr.NextFrom()})
	}
	command, err := runner.StatusCommand(root, queries)
	if err != nil {
		return nil, err
	}
	output, err := p.call(ctx, host, revision, batch[0].OperationID, command)
	if err != nil {
		return nil, err
	}
	statuses, err := runner.ParseStatus(output)
	if err != nil {
		return nil, err
	}
	byRef := make(map[runner.Ref]runner.Status, len(statuses))
	for _, status := range statuses {
		byRef[status.Ref] = status
	}
	return byRef, nil
}

// observe decides what a status reply means for one target.
func (p *poller) observe(ctx context.Context, target resource.DetachedTarget, status runner.Status, now time.Time) outcome {
	o := outcome{target: target}
	live := &o.target.Live
	contact := now
	live.LastContactAt, live.Polls = &contact, live.Polls+1
	live.Stdout.Apply(status.Stdout.From, status.Stdout.Size, status.Stdout.Data)
	live.Stderr.Apply(status.Stderr.From, status.Stderr.Size, status.Stderr.Data)
	finish := func(state resource.TargetState, code *int, class string, clean bool) outcome {
		o.state, o.result, o.clean = state, live.Result(code, class), clean
		return o
	}
	if !target.Launch.Confirmed() && status.State != runner.StateMissing {
		// The launch reply was lost but the command did start: adopt it.
		confirmed := target.Launch
		confirmed.Mode = "adopted"
		if err := p.w.detached.RecordTargetLaunch(ctx, target.WorkspaceID, target.OperationID, target.HostID, confirmed); err == nil {
			o.target.Launch = confirmed
		}
	}
	switch status.State {
	case runner.StateMissing:
		if !target.Launch.Confirmed() {
			if now.Sub(target.Launch.LaunchedAt) < p.cfg.LaunchConfirmGrace {
				return o // the launch may still appear
			}
			if target.Launch.Attempt < resource.MaxLaunchAttempts && target.OperationState == resource.OperationRunning {
				o.relaunch = true
				return o
			}
			return finish(resource.TargetFailed, nil, "launch_failed", false)
		}
		return finish(resource.TargetLost, nil, "state_missing", false)
	case runner.StateUnsupported:
		return finish(resource.TargetLost, nil, "runner_version_unsupported", false)
	case runner.StateLost:
		return finish(resource.TargetLost, nil, "supervisor_lost", true)
	case runner.StateExited:
		if !live.Stdout.CaughtUp() || !live.Stderr.CaughtUp() {
			o.soon = true // read the rest of the output first
			return o
		}
		code := *status.ExitCode
		switch {
		case status.Cancelled && live.CancelReason != "timeout":
			return finish(resource.TargetCancelled, &code, "cancelled", true)
		case status.TimedOut || live.CancelReason == "timeout":
			return finish(resource.TargetTimedOut, &code, "timeout", true)
		case code == 0:
			return finish(resource.TargetSucceeded, &code, "", true)
		default:
			return finish(resource.TargetFailed, &code, "", true)
		}
	}
	// Running.
	resend := live.CancelSentAt != nil && now.Sub(*live.CancelSentAt) > 2*time.Minute
	switch {
	case target.OperationState == resource.OperationCancelRequested && (live.CancelSentAt == nil || resend):
		o.cancel = "cancelled"
	case now.After(target.Launch.Deadline.Add(p.cfg.TimeoutGrace)) && (live.CancelSentAt == nil || resend):
		o.cancel = "timeout" // the host-side watchdog did not act
	}
	if live.CancelReason != "" && o.cancel != "" {
		o.cancel = live.CancelReason // keep the first reason when resending
	}
	return o
}

// unreachable decides what a failed poll means for one target.
func (p *poller) unreachable(target resource.DetachedTarget, now time.Time) outcome {
	o := outcome{target: target}
	lastContact := target.Launch.LaunchedAt
	if target.Live.LastContactAt != nil {
		lastContact = *target.Live.LastContactAt
	}
	switch {
	case now.After(target.Launch.Deadline.Add(p.cfg.UnreachableGrace)):
		o.state, o.result = resource.TargetLost, target.Live.Result(nil, "unreachable")
	case target.OperationState == resource.OperationCancelRequested && now.Sub(lastContact) > p.cfg.UnreachableGrace:
		o.state, o.result = resource.TargetLost, target.Live.Result(nil, "unreachable_during_cancel")
	}
	return o
}

// relaunch launches a target again whose previous launch never started.
func (p *poller) relaunch(ctx context.Context, target resource.DetachedTarget) {
	key := resource.OperationKey{WorkspaceID: target.WorkspaceID, ID: target.OperationID}
	op, err := p.w.deps.Operations.GetOperation(ctx, key.WorkspaceID, key.ID)
	if err != nil || op.Status.State != resource.OperationRunning {
		return
	}
	for _, spec := range op.Spec.Targets {
		if spec.HostID == target.HostID {
			p.w.metrics.launches.WithLabelValues("relaunched").Inc()
			p.w.launchTarget(ctx, p.connector, key, op, spec, target.Launch.Attempt+1)
			return
		}
	}
}
