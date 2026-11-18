# ADR-023: Separate immutable bootstrap configuration from runtime settings

**Status:** Accepted on the confirmed constraints and recommended defaults when Phase 7 was authorized
**Date:** 2026-11-11

## Context

Machinarchy currently loads server configuration from environment variables before storage initialization. Configuration files and flags are used by the CLI, but not by server entry points. Runtime settings will be persisted in the database, which itself requires bootstrap connection and key settings.

## Decision

Load a typed immutable bootstrap configuration from explicit flags, environment variables, a bootstrap file, and compiled defaults, with precedence flag > environment > file > default. Secret values are supplied through protected file/environment providers and never raw flags. Bootstrap-only values initialize the process and are not mutable runtime settings. Runtime-compatible values only seed the first global revision; after initialization, the database revision is authoritative. Never mutate process environment to apply changes.

## Consequences

Database connectivity, root key access, listener identity, process role, migration mode, and telemetry wiring require restart. Changing an environment variable after first initialization does not silently override persisted runtime configuration. Production must not derive encryption keys from authentication secrets.

## Alternatives considered

- Runtime mutation through `os.Setenv`: rejected because dependencies receive snapshots and behavior would vary by read timing.
- Environment always overrides persisted runtime config: rejected because process behavior would diverge from revision and status reporting.
- Put all secrets in command-line flags or the bootstrap YAML: rejected due to process-list/history/config-file exposure.
