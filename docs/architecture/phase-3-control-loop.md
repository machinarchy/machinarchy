# Phase 3 — Hybrid Control Loop and Controller Manager

**Status: Approved to proceed to Phase 4 on the recommended initial baseline.** The initial profile uses one active Control Manager, a five-minute jittered full resync, and four bounded Host reconciliation workers (with the existing SSH bulkhead as a separate ceiling); additional replicas require validated leases/fencing. This is a design, not an implementation. It builds on the inspected current code: the existing Control Manager only subscribes to `host.reconnected` and logs it; its `HandleHostHeartbeat` method has no caller; the NATS adapter uses a durable manual-ack consumer; and the current RunManager executes imperative runs inside API processes.

## Design goal

Events announce that a resource may need work. Persistent desired/observed state remains authoritative. Both event notifications and periodic/startup scans enqueue the same stable resource key; a controller loads the latest resource, compares desired and observed state, and makes an idempotent step toward convergence.

```mermaid
flowchart LR
    CHANGE[Committed resource change] --> OUTBOX[Transactional outbox]
    OUTBOX --> NOTIFY[Notification adapter]
    NOTIFY --> INGRESS[Event ingress: decode and enqueue key]
    INGRESS --> QUEUE[Coalescing rate-limited queue]
    RESYNC[Startup / periodic list scan] --> QUEUE
    QUEUE --> POOL[Fixed bounded controller workers]
    POOL --> CTRL[Controller Reconcile(key)]
    CTRL --> READ[Load latest state]
    READ --> COMPARE[Compare desired vs observed]
    COMPARE --> ACTION[Idempotent bounded adapter action]
    ACTION --> CAS[Conditional status update / condition]
    CAS --> QUEUE

    OPAPI[Operation API] --> OPDB[(Durable Operation state)]
    OPDB --> OPQ[Separate Operation work scheduler]
    OPQ --> OPW[Operation workers]
```

The queue stores **keys, not event payload snapshots**. An event handler validates the notification, maps it to a `ResourceKey`, calls `Add`, and acknowledges only after the local queue has admitted/coalesced the key. It does not read a Host, perform SSH, or invoke business reconciliation. If admission is backpressured, the notification remains retryable. After admission and acknowledgement, a process crash may lose the in-memory key; startup and periodic resync recover it from the database.

## Controller Manager components

```mermaid
flowchart TB
    subgraph Runtime[Control Manager runtime]
        REG[Controller registry] --> ROUTER[Kind-to-key router]
        NATS[NATS / local notification ingress] --> ROUTER
        SCAN[Startup and periodic resync] --> ROUTER
        ROUTER --> Q[Bounded coalescing queue per resource kind]
        Q --> RL[Per-key backoff + global admission limiter]
        RL --> POOL[Fixed worker pool per controller]
        POOL --> HOST[Host controller]
        HOST --> APP[Application ports and use cases]
        APP --> DB[(Persistent state)]
        APP --> CONNECTOR[Connector adapter]
        HEALTH[Readiness and metrics] --> Runtime
    end
    OP[Separate Operation worker subsystem] --> DB
    OP --> CONNECTOR
```

Controller Manager responsibilities:

- Register and validate controllers before start; reject duplicate resource-kind ownership and missing required ports.
- Own notification subscriptions, resync loops, queues, retry clocks, worker pools, worker cancellation, readiness, and shutdown.
- Ensure a fixed, configured maximum number of reconciliation workers; never create a goroutine per event or resource.
- Route both edge and level triggers into the exact same queue and controller method.
- Recover panics at the worker boundary, log a stack and key, record an error metric, then rate-limit retry; a panic must not permanently mark the key successful.
- Keep imperative Operation workers separate in lifecycle, queue, concurrency, retry policy, authorization and metrics. A controller may create or observe an Operation, but must not run a long command as part of reconciliation.

## Controller and key contracts

```go
type ResourceKey struct {
    Kind      ResourceKind
    Workspace WorkspaceID
    ID        ResourceID
}

type ResourceKind string

type Admission uint8

const (
    QueueAccepted Admission = iota
    QueueCoalesced
    QueueBackpressured
    QueueShuttingDown
)

type ReconcileResult struct {
    RequeueAfter time.Duration // zero means no timed requeue requested
}

type Controller interface {
    Name() string
    Kind() ResourceKind
    Reconcile(ctx context.Context, key ResourceKey) (ReconcileResult, error)
}

type Queue interface {
    Add(ResourceKey) Admission
    AddAfter(ResourceKey, time.Duration) Admission
    AddRateLimited(ResourceKey) Admission
    Get(context.Context) (ResourceKey, bool)
    Done(ResourceKey)
    Forget(ResourceKey)
    NumRequeues(ResourceKey) int
}
```

These are discussion-level types, not a demand to export a generic framework. The controller loads current state using its application ports after `Reconcile` begins. If the resource is absent and no cleanup tombstone remains, reconciliation is a successful no-op. If it is deleting, the controller performs only its owned cleanup. The result/error contract is:

- Return `nil` after convergence, a harmless no-op, or after recording a permanent domain condition. The manager forgets the per-key error backoff and schedules only `RequeueAfter` if needed.
- Return an error for transient failures such as database unavailability, connector timeout, or an uncompleted cleanup step. The manager retries with bounded backoff.
- Do not return permanent invalid-spec errors repeatedly. Validation should reject invalid writes; for legacy/corrupt state, record a stable failure condition and alert.
- Treat cancellation as a context exit, not a successful reconciliation. On manager shutdown, do not turn cancellation into a retry storm; level-triggered recovery will reschedule unfinished work.

## Workqueue architecture

### Resource-key scheduling

Key identity is `(resource kind, workspace ID, resource ID)`. It must not contain a generation: several changes to one Host collapse into one reconciliation, which reads the latest generation. A separate key kind keeps Host reconciliation distinct from Operation scheduling and any future resource controller.

Only events that can affect desired convergence or lifecycle enqueue a controller key. A `HostConditionChanged`/`HostObserved` event used for UI, audit, or alert projections must not feed back into the same Host controller unless it represents a separate desired-state trigger; otherwise condition writes can form a self-trigger loop.

### Deduplication and per-resource exclusion

Use the Kubernetes client-go workqueue pattern:

1. **Dirty set:** `Add(key)` marks a key dirty. Duplicate adds while queued coalesce.
2. **Processing set:** `Get` moves the key to processing and clears its dirty marker.
3. If an update arrives while processing, it marks the key dirty without starting another reconcile.
4. `Done(key)` removes it from processing; if it became dirty meanwhile, enqueue it once more.

This prevents concurrent processing of the same key in one process while allowing fresh state to be revisited after an update. Queue capacity counts distinct pending keys; backpressure blocks/throttles resync page admission and leaves broker notifications unacknowledged/retryable. Never silently drop an admitted key. No queue-length check should be used as a correctness gate.

### Worker pool and resource isolation

Use fixed workers per controller plus a process-wide ceiling. Configure controller concurrency and connector/SSH capacity separately; a queue worker should not hold a database transaction while waiting on network I/O. Initially, modest fixed concurrency (for example, four Host reconciliation workers) is safer than matching inventory size. The existing SSH limit of 32 is a process-local outer bulkhead, not a controller concurrency target. Operation workers have their own bounded pool and share only the connector capacity bulkhead.

Separate queues per resource kind prevent repeated slow Host work from starving other controllers. Within a queue, use fair FIFO admission with coalescing. Global token-bucket admission can bound total reconciliations; per-key exponential delay handles individual failures. Multiple replicas multiply process-local limits, so deployment-level SSH admission remains an explicit later scale concern.

### Retry and queue rate limiting

Recommended initial policy: exponential per-key delay beginning at 1 second, capped at 5 minutes, with jitter; combine it with a global token bucket. Respect a longer `Retry-After` from a connector only up to a configured maximum. Successful reconciliation and permanent recorded conditions call `Forget`; transient errors call `AddRateLimited`. Requeue-after health/drift checks use a delayed queue entry, not a timer goroutine per Host.

Do not impose a finite total retry count that discards a still-relevant key. After repeated failures, retain capped slow retries, expose a degraded condition and alert. Database-wide outage should cause backpressure and readiness degradation, not thousands of immediate retries. A malformed or unsupported notification is quarantined/logged and acknowledged as poison input; the level scan still discovers the underlying resource state.

## Controller interface and lifecycle

1. Bootstrap creates application ports and a manager with queue/concurrency/backoff config.
2. Controllers register during construction. The manager validates one controller per kind and required adapters; registration closes before `Start`.
3. Manager starts bounded workers, notification ingress, and resync loops under one root context. It remains unready until dependencies and subscriptions are usable and the initial scan has completed enqueueing.
4. Controller methods receive a bounded context, a stable key, and return promptly after one bounded reconciliation step. Long-running workflows become Operations.
5. Manager readiness requires active required controllers, queue/worker health, and required DB/notification dependencies. Queue non-emptiness alone is not unready; backlog age and saturation are monitored separately.
6. `Stop` first marks unready and stops admission/subscriptions, then cancels/drains current work to a deadline, shuts queues, waits for workers, and returns. Unfinished keys are recoverable by resync.

## Startup recovery and periodic resynchronization

At startup, subscribe before enumerating state so changes racing with the initial scan enter the same queue. Then start workers and perform a paginated, bounded scan of all managed Hosts, pending/deleting resources, and controller-specific recovery keys. Enqueue by the same `ResourceKey`. Track a scan checkpoint only for progress/observability; an interrupted scan restarts or resumes safely because Add is idempotent.

Run periodic resync with interval jitter. Initial recommendation: a five-minute full sweep for the 10–100+ host deployment profile, with immediate startup recovery and faster controller-specific delayed requeues when needed. Each scan pages through persistent state with bounded query size and admission rate, and schedules every resource requiring management. The scan repairs missed/expired NATS messages, process crashes after acknowledgement, old poison events, and bugs in notification delivery. A scan must include deleting tombstones and other incomplete lifecycle states, not just healthy active Hosts. Full state reads are authoritative; no event log replay is required for correctness. The interval must be tuned from recovery objectives and database load.

For distributed deployments, avoid every replica repeatedly listing every Host: use a single resync leader or deterministic key-range sharding, while retaining leases for correctness. A failover leader repeats the scan. The exact interval and shard strategy depend on measured DB load and recovery objectives.

## Generation handling and stale-work protection

The controller snapshots the current Host and its `Generation` at the start of reconciliation. It computes actions from that snapshot. Before writing status, it conditionally updates only if the same generation is still current and the worker's distributed fencing token is valid. If the write loses the race, discard the stale result and enqueue the key again. `ObservedGeneration` advances only for the generation actually examined. Resource Revision may advance for status; it does not replace the desired-generation guard.

On deletion, a current read determines whether the object is absent (no-op) or a deletion tombstone remains (perform owned finalization). An in-flight SSH connection uses a bounded context and should be canceled when possible. Any remote side effect that may have completed despite cancellation must be treated as uncertain and made safe to repeat; non-idempotent actions belong in Operations with explicit result semantics.

## Reconciliation sequences

### Edge-driven notification

```mermaid
sequenceDiagram
    participant API as Application write
    participant DB as Database + outbox
    participant Relay as Outbox relay / NATS
    participant Ingress as Event ingress
    participant Q as Coalescing queue
    participant C as Controller worker
    participant Adapter as Connector adapter

    API->>DB: Commit desired change + outbox(key, generation)
    Relay->>NATS: Publish wake-up
    NATS->>Ingress: Deliver notification
    Ingress->>Q: Add(resource key)
    Q-->>Ingress: Admitted or coalesced
    Ingress->>NATS: Ack after admission
    Q->>C: Get(key)
    C->>DB: Load latest Host and generation
    C->>C: Compare desired and observed state
    C->>Adapter: Bounded idempotent action if needed
    Adapter-->>C: Observation/result
    C->>DB: Conditional status update for read generation
    C->>Q: Done; Forget or delayed/rate-limited requeue
```

### Level-triggered resync and multiple updates

```mermaid
sequenceDiagram
    participant Scan as Startup/periodic scanner
    participant DB as Database
    participant Q as Coalescing queue
    participant W as Worker
    participant C as Controller

    Scan->>DB: List next page of managed/incomplete resource keys
    DB-->>Scan: keys
    Scan->>Q: Add keys through same admission path
    W->>Q: Get(key)
    W->>C: Reconcile(key)
    C->>DB: Read current generation N
    Note over C,DB: A newer write advances Host to generation N+1
    DB-->>Q: Change notification adds same key (marks dirty)
    C->>DB: Conditional status update for N
    DB-->>C: Conflict; stale observation rejected
    C->>Q: Done(key); dirty key is queued once
    W->>C: Reconcile(key) again
    C->>DB: Load latest generation N+1
```

## Failure and retry matrix

| Scenario | Response |
|---|---|
| Duplicate notification | Queue coalesces same key; one current-state read/reconcile. No duplicate business action. |
| Out-of-order notification | Payload generation is not trusted; controller loads latest database state. Older event is only an extra wake-up. |
| Multiple updates to one Host | Adds while queued coalesce; adds while processing mark dirty and cause one follow-up reconcile. Latest spec wins. |
| Controller process crashes mid-reconcile | In-flight network operation is bounded; local queue is lost. Startup scan re-adds the resource. A lease expires in distributed mode; stale status writes fail fencing/generation checks. External side-effect outcome may be uncertain, so controller actions must be idempotent. |
| Database unavailable | Reconcile returns transient error; per-key and global backoff apply. Readiness degrades. Notification stays unacked if key could not be admitted; resync resumes after recovery. |
| Host deleted during reconcile | Re-read before action where practical; status CAS rejects stale writes. Absent resource becomes a no-op; tombstone remains until owned cleanup completes. New Operations cannot target deleting/deleted Hosts. |
| Long SSH connection | Controller work is bounded by deadline and shared SSH bulkhead. Context cancellation closes the connector when supported. Long commands are never run inside reconcile; they are Operations. |
| Worker cancellation/shutdown | Mark unready, stop new admission, cancel current contexts, wait up to drain deadline. Keep durable state incomplete; restart scan reschedules it. Do not claim external action did not happen if outcome is uncertain. |
| Multiple Control Manager replicas | PostgreSQL-backed per-key lease/fencing prevents concurrent reconciles. Initial notification delivery uses DB-backed queue/outbox polling with optional `LISTEN/NOTIFY` wake-up; JetStream may be added only under ADR-018 criteria. Periodic scan is sharded or jittered. SQLite is not the supported multi-replica coordination store. |
| Missed notification or crash after notification acknowledgment | Startup and periodic list scan enqueue the current resource key through the same path; event loss delays convergence only until recovery scan. |
| Poison notification | Validate schema and key; quarantine/metric/log malformed messages and acknowledge/dead-letter rather than retry forever. State scan independently recovers valid persisted work. |

## SQLite and PostgreSQL coordination

### SQLite

Use all-in-one runtime with one Control Manager instance and an in-memory queue. SQLite is configured with one open connection, WAL, and busy timeout; it serializes writes and is appropriate for the accepted single-node mode, not as a distributed lease service. Keep transactions short and never hold a write lock across SSH. Local process restart recovery comes from listing persistent Host state. If the Control Manager process is separate on the same node, treat it as one deployment instance and account for SQLite lock contention; do not claim horizontal manager replicas.

### PostgreSQL

Use PostgreSQL for independently deployed API/Control Manager and multiple manager replicas. Store a per-key lease with owner, expiry, and monotonically increasing fencing token. Claim/renew/release with short transactions and conditional updates or row locks (`SKIP LOCKED` where claiming a batch); never hold a row lock during remote I/O. Renew before expiry. A worker that loses lease cancels work and may not write status with an old fencing token. Generation checks protect against desired-state races; leases protect against cross-process duplicate work. They solve different problems.

A lease cannot guarantee exactly-once external side effects: a worker can perform a remote action, lose connectivity, and fail before recording success. Idempotent reconciliation and truthful uncertainty are required. Controller leases do not replace the separately designed Operation claim/heartbeat model.

## Operation worker separation

Resource reconciliation asks “what is the latest desired state, and what bounded step makes observed state closer?” Operation execution asks “what happened to this accepted request and its fixed targets?” Keep separate queues, state transitions, retry policy, status tables/contracts, cancellation, limits, and metrics. The Control Manager runtime may host both subsystems, but a blocked command run must not occupy a reconciliation worker. Controllers can enqueue an Operation and then reconcile its resulting resource state from the database.

## Reconciliation metrics and health

Use bounded labels such as controller name, outcome class, and resource kind. Never label with Host ID, name, address, workspace ID, event ID, or arbitrary error text.

- queue depth, distinct dirty keys, oldest queue age, admission rejections/backpressure;
- enqueue/coalesced/reconcile totals, retries, current backoff, and reconciliation duration histogram;
- workers configured/in-flight, per-controller saturation, panic count, and canceled work;
- stale-generation/fencing rejects, lease contention/expiry/loss, and conflict count;
- resync last success, duration, keys scanned/enqueued, scan failures, and oldest incomplete resource age;
- notification/outbox publish lag, malformed/poison events, handler failures, ack/nak totals;
- condition transitions by bounded type/reason and number of resources not Ready.

Readiness covers DB access, required notification subscription, registered controllers, worker loop health, and initial recovery scan completion. Liveness remains independent of a transient database or broker outage. Backlog and resource degradation should be visible in metrics/alerts even while the process remains live.

## Go package boundary recommendations

- Keep `cmd/controlmanager` as a thin signal/config/runtime entry point.
- Keep `internal/controlmanager` as the runtime manager, registry, worker supervision, and lifecycle owner; add the queue implementation there initially rather than creating a generic queue framework.
- Put resource-specific reconciliation logic beside application capabilities (for example `internal/controller/host`) and have it call narrow application ports. It must not import Echo or NATS.
- Keep event decoding/transport in `internal/broker` or a focused notification adapter. Its callback only validates the envelope and enqueues keys.
- Keep persistence contracts with their consuming application/controller capability; SQL adapters implement claims, conditional status updates, and outbox contracts. Do not expose `*sql.Tx` to controllers.
- Keep operation submission and execution lifecycle separate from the resource controller package, even if the Control Manager runtime supervises both.
- Do not add Kubernetes/client-go as a dependency: the required queue and reconciliation behavior is small enough to implement locally with explicit semantics.

## Comparison with Kubernetes controller patterns

Machinarchy should adopt the core controller loop: watch/change notification triggers a key, the controller loads current state, computes an idempotent action, and records status; retries and later events continue convergence. The client-go workqueue's dirty/processing behavior, per-item exclusion, re-enqueue-after-processing, shutdown, and combined global/per-item rate limiting directly fit this design. [Kubernetes controller concepts](https://kubernetes.io/docs/concepts/architecture/controller/) · [client-go workqueue API](https://pkg.go.dev/k8s.io/client-go/util/workqueue).

Machinarchy should not copy Kubernetes' API server, informer cache, CRD/GVK machinery, or cluster-wide controller framework. The SQL database is the authoritative resource store, and the first implementation can read current state directly. Adopt `Generation`, `ObservedGeneration`, and conditions for real desired/observed state needs, but do not add fields to resources that have no asynchronous reconciliation. PostgreSQL leases are needed for Machinarchy's independently deployed replicas; the in-process Kubernetes workqueue alone does not provide cross-process exclusion.

## Proposed ADRs

The three draft ADRs are in `decisions/` and remain proposed pending this phase's approval:

- ADR 013 — Level-triggered, edge-driven reconciliation pipeline.
- ADR 014 — Bounded key-based queue, deduplication, retries, and backpressure.
- ADR 015 — Single-node SQLite and PostgreSQL replica coordination.

## Decisions requiring owner input

1. **First distributed deployment topology:** Should the first PostgreSQL deployment support multiple Control Manager replicas immediately, or one active manager while the API scales independently? **Recommendation:** start with one active Control Manager per installation; enable multiple only after lease/fencing and crash tests validate. This reduces early coordination complexity but limits controller throughput until that validation is complete.
2. **Recovery objective and resync interval:** Is a five-minute worst-case recovery after a missed edge notification acceptable, or is a shorter interval required? **Recommendation:** five-minute jittered full sweep plus immediate startup scan and targeted retries. Shorter intervals reduce missed-event convergence delay but increase database reads and can create scan pressure across replicas.
3. **Initial Host controller concurrency:** Should the default be four bounded workers per manager with the existing process SSH bulkhead of 32, or a different fixed concurrency? **Recommendation:** four Host reconciliation workers initially, with configuration and metrics to tune after load measurements. Higher concurrency improves catch-up speed but increases DB contention and remote connection pressure; the 32-connection cap remains a hard process-local ceiling.

## Phase 3 acceptance criteria

- Owner approves the same-pipeline edge/level control loop and key semantics.
- Owner approves queue backpressure, retry bounds, worker isolation, and reconciliation timeouts.
- Owner approves the SQLite single-manager and PostgreSQL multi-manager boundary and lease/fencing requirements.
- Operation workers remain separate from controller reconciliation.
- ADRs are accepted/amended before Phase 4; no code change is authorized by this document.
