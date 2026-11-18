# Implementation Phase F — Event Infrastructure and Control Loop

**Status:** Implemented foundation; PostgreSQL runtime validation still requires `MACHINARCHY_TEST_POSTGRES_URL`.

## Runtime path

Managed Host create/spec-change/deletion writes now commit the Host row, audit record, outbox event, and `control-manager` delivery together. Events use a versioned envelope and carry only the Host key, generation/revision, actor/correlation metadata, and a small change classification. Operation arguments, Host snapshots, credentials, and remote output are excluded.

The Control Manager polls the durable outbox and admits a stable `(kind, workspace, ID)` key to a bounded coalescing queue. It acknowledges after accepted or coalesced admission. Claim leases make a dispatcher crash retryable; startup and periodic scans recover keys lost after acknowledgement. Every controller invocation receives a key and must load current resource state. Event revisions and payload values are never used as current state.

```mermaid
sequenceDiagram
    participant App as Resource repository
    participant DB as SQL transaction
    participant Relay as Outbox dispatcher
    participant Q as Key workqueue
    participant W as Bounded worker
    participant C as Host controller

    App->>DB: Update Host + audit + immutable event + delivery
    DB-->>App: Commit all or roll back all
    Relay->>DB: Claim delivery with owner/expiry
    DB-->>Relay: Versioned envelope
    Relay->>Q: Add(kind, workspace, Host ID)
    Q-->>Relay: Accepted or coalesced
    Relay->>DB: Acknowledge delivery
    W->>Q: Get key
    W->>C: Reconcile(key, bounded context)
    C->>DB: Load latest Host and generation
    C->>C: Compare desired and observed state
    C->>DB: Conditional status/delete write
    W->>Q: Done; retry or timed requeue if needed
```

## Recovery and shutdown

```mermaid
sequenceDiagram
    participant CM as Control Manager
    participant DB as SQLite/PostgreSQL
    participant Q as Local key queue
    participant Ctrl as Controller

    CM->>DB: Startup paginated Host-key scan
    DB-->>CM: Current Host keys
    CM->>Q: Add each key through normal admission
    Note over CM,DB: PostgreSQL full scans use a renewable leader lease
    Q->>Ctrl: Reconcile stable key
    Ctrl->>DB: Read latest generation
    Ctrl->>DB: Lease-fenced status write (PostgreSQL)
    Note over CM,Q: Shutdown stops ingress and resync, then drains queued work
    CM->>Q: Shutdown admission; promote delayed keys for drain
    CM->>Ctrl: Cancel remaining reconcile contexts at drain deadline
```

Outbox redelivery is at least once. A crash between queue admission and acknowledgement can redeliver the same event; queue deduplication absorbs it. A crash after acknowledgement can lose local memory, so startup/periodic scans are mandatory. Unknown event types or schema versions are quarantined instead of retried indefinitely. Retry attempts and sanitized failure classes persist in the delivery row. Delivered events are retained for 7 days and quarantined events for 30 days; pending deliveries are never pruned.

The queue has a bounded number of distinct pending keys, dirty/processing sets, per-key exponential backoff with jitter, delayed retry scheduling through one scheduler goroutine, and fixed worker count. Same-key updates during processing result in one follow-up pass. Panic recovery turns controller faults into retryable errors. Shutdown closes admissions and drains queued/in-flight work up to the configured deadline.

## Database coordination

- **SQLite:** one Control Manager per single-node deployment; one local queue and short database transactions. No distributed lease is used. Outbox dispatch uses polling and restart resync.
- **PostgreSQL:** dispatchers compete on per-consumer deliveries using `FOR UPDATE SKIP LOCKED`. Per-resource leases include owner, expiry, and monotonically increasing fencing token. Host status writes check that the fencing token is current and unexpired in the same transaction. One manager at a time leads the full resync scan; local queues remain process-local. Polling is authoritative; `LISTEN/NOTIFY` was not added.

The Control Manager process no longer requires NATS. The pre-existing API NATS connection remains for legacy API event behavior; the new canonical resource outbox path does not publish to NATS. Kafka is not used.

## Current limits

- The controller framework currently registers the canonical Host controller. A Host with `Presence=Absent` is removed immediately and emits a transactional `host.deleted` event. For a managed Host, no Phase F connector exists yet; the controller records `Ready=False / ConnectorUnavailable` and checks again on a delayed interval. It does not claim remote convergence.
- Canonical `managed_hosts` remain additive beside legacy `hosts`; legacy API host changes do not write these outbox events and are not reconciled by this controller.
- Operation execution remains separate and is not scheduled by the Host reconciliation queue. No inbox is needed for the coalescible key-notification consumer; durable consumer side effects will require one.
- PostgreSQL lease and outbox SQL has shared contract tests, but those cases only execute when a PostgreSQL URL is configured. No PostgreSQL server was available for this validation run.
