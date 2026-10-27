# Git Hooks

Git hooks are scripts that run automatically at certain points in your Git workflow. They help enforce code quality, commit message conventions, and other automation tasks.

Machinarchy uses two repository-managed Git hooks:

- `pre-commit` → runs before a commit is finalized
- `commit-msg` → validates commit messages

The hook scripts live in [`.githooks`](../../.githooks/). Git uses them only after `make setup` configures this clone's local `core.hooksPath` setting.

## 1. Pre-Commit Hook

Ensures code meets quality standards before committing.

**Purpose**

- Run Go formatting (gofmt)
- Run Go static analysis (go vet, staticcheck)
- Run the Go test suite
- Prevent commits if checks fail

**Script path**: [`pre-commit`](../../.githooks/pre-commit)

## 2. Commit-Message Hook

Enforces **Conventional Commits**.

**Purpose**

- Ensure commit messages follow a predefined convention
- Support automation tools like semantic-release
- Prevent ambiguous commit messages

**Script path**: [`commit-msg`](../../.githooks/commit-msg)

## 3. Installing Git Hooks

Run the project setup script from the repository root:

```bash
make setup
```

This checks for Go 1.26 or newer, creates `.env` from `.env.example` with owner-only permissions if needed, and configures `.githooks` for this clone. It leaves an existing `.env` unchanged. If another non-default `core.hooksPath` is already configured, setup stops and asks you to review that setting instead of silently replacing it.

## 4. Workflow Diagram

```text
┌───────────────┐
│ Developer     │
│ runs `git add`│
└───────┬───────┘
        │
        ▼
┌───────────────┐
│ pre-commit    │
│ (gofmt, vet,  │
│ tests,        │
│ staticcheck)  │
└───────┬───────┘
        │ pass
        ▼
┌───────────────┐
│ commit-msg    │
│ (Conventional │
│ Commits check)│
└───────┬───────┘
        │ pass
        ▼
┌───────────────┐
│ Git Commit    │
│ stored locally│
└───────┬───────┘
        │ push
        ▼
┌───────────────┐
│ CI/CD Workflow│
│ - semantic-   │
│   release     │
│ - Docker push │
└───────────────┘
```
- **pre-commit** → prevents bad code from being committed
- **commit-msg** → ensures proper commit messages for automated releases
- CI/CD behavior depends on the configured repository pipeline.

## 5. Best Practices

- Keep hooks fast (avoid heavy tasks in pre-commit)
- Always make scripts executable (chmod +x)
- Include hooks in repo for team onboarding
- Update hooks when conventions or CI/CD changes

## 6. Benefits

- ✅ Consistent commit messages → automatic versioning
- ✅ Code quality enforced before commits
- ✅ Prevents broken code from entering repository
- ✅ Smooth onboarding for new developers

## 7. References

- [Git Hooks Documentation](https://git-scm.com/book/en/v2/Customizing-Git-Git-Hooks)
- [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/)
