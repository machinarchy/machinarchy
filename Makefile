# ============================================================================
# MACHINARCHY - DEVELOPER COMMANDS
# ============================================================================
# First-time setup:
#   make setup-local
#   make help
#
# Common workflow:
#   make build
#   make test
#   make verify
#
# Full local verification:
#   make verify-full
#
# Local infrastructure:
#   make infra-up
#   make infra-down
#
# Local application environment:
#   make apps-up
#   make apps-down
#
# Complete development environment:
#   make dev-up
#   make dev-down
#
# Local SSH fixtures:
#   make ssh-servers-up
#   make ssh-servers-ps
#   make ssh-servers-down
# ============================================================================

.DEFAULT_GOAL := help

# Shared variables and build metadata must be loaded before target definitions.
include mk/config.mk

# Go development and application binaries.
include mk/go.mk

# Code quality, tests, and security checks.
include mk/quality.mk
include mk/test.mk
include mk/security.mk

# Local container environments and SSH test fixtures.
include mk/compose.mk
include mk/ssh.mk

# Keep help last so it can inspect every included Makefile.
include mk/help.mk
