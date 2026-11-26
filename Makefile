# ============================================================================
# Machinarchy developer commands
# ============================================================================
# Run `make help` for every target. Common workflows:
#
#   make setup-local          first-time local setup
#   make build                build all binaries into ./bin
#   make verify               format, tidy, vet, lint and test
#   make dev-up / dev-down    start or stop infrastructure and applications
#   make run-cli ARGS="hosts"
#
# Requires GNU Make 3.81 or newer (the macOS default works). The targets are
# split by topic under mk/:
#   config.mk      tools, paths, build metadata, shared helpers
#   build.mk       setup, dependencies, builds, local runs, API docs
#   checks.mk      formatting, linting, tests, coverage, security
#   containers.mk  Docker Compose environments and SSH test fixtures
#   help.mk        `make help`
# ============================================================================

.DEFAULT_GOAL := help
.DELETE_ON_ERROR:
MAKEFLAGS += --no-builtin-rules

include mk/config.mk
include mk/build.mk
include mk/checks.mk
include mk/containers.mk
include mk/help.mk
