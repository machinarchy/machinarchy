# ADR-051: Keep Kubernetes optional

**Status:** Accepted as the working design baseline when Phase 12 was authorized
**Date:** 2026-11-11

## Context

Kubernetes can provide scheduling, rolling updates, probes, resource controls, and deployment automation, but it adds a significant operator dependency and does not replace Machinarchy's own resource/Operation coordination.

## Decision

Design OCI containers and private management endpoints so they can run on Kubernetes, Compose, systemd, or VMs. Do not require a Kubernetes API or CRDs at runtime. Use Kubernetes Deployments and probes only as optional packaging/orchestration; SQLite stays one replica on supported local persistent storage, while multi-replica deployments use PostgreSQL.

## Consequences

Operations documentation and release artifacts should define a platform-neutral environment/config contract. Kubernetes-specific manifests can be added when a supported operator audience and maintenance capacity exist.

## Alternatives considered

- Make Kubernetes mandatory: rejected because single-node users should not need a cluster.
- Implement a Kubernetes controller as the core runtime: rejected because Machinarchy already has its own domain and controller model and must remain independently usable.
