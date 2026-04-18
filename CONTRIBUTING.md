# Contributing to kubeswarm

Thank you for your interest in contributing. This document covers how to get started, the development workflow and the standards we expect from contributions.

## Before you start

- **Open an issue first** for any non-trivial change (new feature, refactor, or API change). This avoids wasted effort if the direction isn't a good fit.
- Bug fixes and documentation improvements can go straight to a PR.

## Development setup

**Prerequisites**

| Tool   | Version              |
| ------ | -------------------- |
| Go     | 1.26+ (see `go.mod`) |
| Docker | 24+                  |
| kubectl| 1.35+                |
| kind   | 0.27+                |

```bash
# 1. Clone
git clone https://github.com/kubeswarm/kubeswarm.git
cd kubeswarm

# 2. One-time setup (installs pre-push hook)
make setup

# 3. Run the full CI pipeline locally
make ci

# 4. Start a local Kind cluster with the operator deployed
make local-up

# 5. After code changes, rebuild and redeploy
make local-up
```

## Project layout

```
api/v1alpha1/          # CRD type definitions - edit here first
internal/controller/   # One reconciler per Kind
internal/webhook/      # Admission webhooks
internal/mcpgateway/   # MCP SSE gateway for agent-to-agent calls
pkg/                   # Shared packages (audit, costs, healthz, observability)
runtime/               # Nested Go module - agent binary + vendor SDK implementations
  cmd/agent/           # Agent runtime entrypoint
  cmd/operator/        # Operator binary entrypoint
  providers/           # LLM provider implementations (Anthropic, OpenAI, Gemini)
config/                # Kustomize manifests (CRDs, RBAC)
cmd/main.go            # Operator entrypoint (dev mode)
```

## Making changes

### Changing a CRD type

1. Edit the relevant `api/v1alpha1/*_types.go` file.
2. Regenerate code and manifests:

   ```bash
   make generate   # regenerates zz_generated.deepcopy.go
   make manifests  # regenerates config/crd/ and config/rbac/
   ```

3. Update the cookbook if the new field should be showcased.
4. Update the controller if the new field needs to be acted on.

### Adding a controller feature

1. Edit `internal/controller/<kind>_controller.go`.
2. Add or update tests in `internal/controller/<kind>_controller_test.go`.
3. Run tests: `make test`.

### Changing the agent runtime

The runtime lives in `runtime/` and is built into a separate Docker image (`Dockerfile.runtime.local` for local dev). It reads all config from environment variables injected by the operator (`AGENT_MODEL`, `AGENT_SYSTEM_PROMPT`, `AGENT_MCP_SERVERS`, `ANTHROPIC_API_KEY`, `TASK_QUEUE_URL`, etc.).

## Available make targets

```bash
make help           # Show all targets

# Development
make setup          # Install git hooks (one-time)
make build          # Compile controller + runtime binaries
make generate       # Regenerate deepcopy after type changes
make manifests      # Regenerate CRDs and RBAC after marker changes
make clean          # Remove build artifacts and test cache

# Quality
make fmt            # Format all Go code
make test           # Run all tests (controller + runtime)
make lint           # Format and run all linters
make lint-fix       # Format and run all linters with auto-fix
make coverage       # Run tests and open HTML coverage report
make check-generate # Verify generated code is up-to-date

# Security
make vulncheck      # Scan for known vulnerabilities
make license-check  # Verify no incompatible licenses
make scan-secrets   # Scan for leaked secrets

# CI
make ci             # Run the full CI pipeline locally

# Local cluster
make local-up       # Build, load, deploy to Kind cluster
make local-down     # Remove from cluster
make local-logs     # Tail controller logs
make kind-delete    # Delete the Kind cluster
```

## Running tests

```bash
# All tests (controller + runtime)
make test

# Controller tests only (uses envtest, no cluster needed)
make test-controller

# Runtime tests only
make test-runtime
```

Tests use the standard `testing` package with `envtest` for controller integration tests.

## Code style

- Run `make lint` before submitting - CI will block on lint failures.
- `make lint` runs `gofmt` automatically before linting.
- Keep reconcile functions focused: extract helpers for anything non-trivial.
- Never panic in a `Reconcile()` function.
- Use `ctrl.Result{}, err` for transient errors (triggers exponential backoff) and `ctrl.Result{}, nil` when done.

## Security practices

These are non-negotiable. PRs that violate them will not be merged.

### Dependency management

- **Pin exact versions** - always specify exact versions in `go.mod` (`v1.2.3`, not `v1.2` or `>=v1.2`). Never use `@latest` in `go get` commands and then commit without verifying the exact version that was selected.
- **No unnecessary dependencies** - every new `require` line must be justified. Prefer stdlib or already-imported packages before adding a new module.
- **Update deliberately** - bump a dependency only when you need a specific fix or feature. Blanket upgrades belong in a dedicated PR with a rationale.
- **Run `go mod tidy`** before committing - ensures `go.sum` only contains hashes for reachable packages.

### Secrets and credentials

- **Never commit secrets** - API keys, tokens, passwords, or any credentials must not appear in source files, `config/` manifests, or sample YAMLs. Use `apiKeys.existingSecret` in Helm values and Kubernetes `Secret` references in manifests.
- **Redact in bug reports** - when reporting issues or including YAML in PRs, always redact real key values (replace with `sk-ant-...` or similar placeholders).
- **No plaintext secrets in env var defaults** - default values for env vars like `ANTHROPIC_API_KEY` must always be empty string.

### Input validation and injection

- **Validate at boundaries** - validate all user-supplied input (CRD spec fields, webhook payloads, env vars) at the point it enters the system. Trust internal interfaces.
- **No `fmt.Sprintf` in shell/exec calls** - any string passed to `exec.Command` or used in a template that reaches a shell must be sanitized or passed as a separate argument (never interpolated into a command string).
- **Use `subtle.ConstantTimeCompare`** for any token or secret comparison - never use `==` or `strings.Compare`.

### HTTP and network

- **Always set timeouts** - every `http.Client` or server (`http.Server`) must have explicit `ReadTimeout`, `WriteTimeout` and `IdleTimeout` set. Never use `http.DefaultClient`.
- **TLS for external endpoints** - MCP server URLs and any external webhook targets must use `https://` in production. The operator logs a warning for `http://` URLs.
- **Rate-limit public endpoints** - any HTTP handler exposed outside the cluster (webhooks, trigger endpoints) must have per-IP rate limiting.

### Container and Kubernetes security

- **Non-root containers** - agent pods must run as a non-root user (`runAsNonRoot: true`). The operator enforces this in `buildDeployment()`; do not remove it.
- **Read-only root filesystem** - set `readOnlyRootFilesystem: true` on all containers unless a writable path is explicitly required (and mounted as an `emptyDir`).
- **Drop all capabilities** - container `SecurityContext` must include `capabilities: drop: ["ALL"]`.
- **Minimal RBAC** - RBAC rules (generated from `+kubebuilder:rbac` markers) must follow least privilege. Do not add `*` verbs or broad resource wildcards.

## Branch naming

Name your branch with one of these prefixes - a GitHub Action will automatically label the PR:

| Prefix | Label | Example |
| --- | --- | --- |
| `feat/` | `feat` | `feat/mcp-discovery-cache` |
| `fix/` | `bug` | `fix/nil-pointer-health-check` |
| `docs/` | `docs` | `docs/swarmteam-crd-guide` |
| `chore/` | `chore` | `chore/bump-go-version` |
| `refactor/` | `refactor` | `refactor/agent-runner-startup` |
| `test/` | `test` | `test/reconcile-timeout` |
| `ci/` | `ci` | `ci/add-arm64-build` |

These labels drive the auto-generated release notes.

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/). The prefix must match the branch prefix:

```
<type>: <short description>
```

| Type | When |
| --- | --- |
| `feat` | New feature or capability |
| `fix` | Bug fix |
| `docs` | Documentation only |
| `chore` | Maintenance, deps, CI, tooling |
| `refactor` | Code change that neither fixes a bug nor adds a feature |
| `test` | Adding or updating tests |
| `ci` | CI/CD pipeline changes |

Examples:

```
feat: add SwarmMemory vector store support
fix: prevent nil pointer in health check when queue is down
chore: rewrite Makefile and rename operator to controller
```

Keep the first line under 72 characters. Add a body for non-obvious changes.

## Submitting a pull request

1. Fork the repo and create a branch from `main` using the naming convention above.
2. Make your changes with focused commits following the commit message convention.
3. Ensure `make ci` passes locally (lint, test, vulncheck, license, secrets).
4. Open a PR against `main` with a clear description of what and why.
5. Link the related issue (if any) in the PR description.

We use **Rebase and merge** for all PRs to keep a linear history on `main`.

## Reporting bugs

Open a [GitHub issue](https://github.com/kubeswarm/kubeswarm/issues/new) with:

- Operator version (`kubectl get deployment -n kubeswarm-system`)
- Kubernetes version (`kubectl version`)
- The SwarmAgent / SwarmTeam YAML (redact secrets)
- Relevant controller logs (`kubectl logs -n kubeswarm-system -l control-plane=controller-manager`)

## Security vulnerabilities

See [SECURITY.md](./SECURITY.md) - please do **not** open a public issue.

## License

By contributing, you agree that your contributions will be licensed under the [Apache 2.0 License](./LICENSE).
