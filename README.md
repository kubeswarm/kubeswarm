<p align="center">
  <img src="https://raw.githubusercontent.com/kubeswarm/kubeswarm-design/main/logo/logo-full.svg" width="400" alt="kubeswarm - orchestrate. scale. swarm.">
</p>

<p align="center">
  <a href="https://github.com/kubeswarm/kubeswarm/actions/workflows/ci.yml"><img src="https://github.com/kubeswarm/kubeswarm/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/kubeswarm/kubeswarm/releases"><img src="https://img.shields.io/github/v/release/kubeswarm/kubeswarm" alt="GitHub release"></a>
  <a href="./LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-blue.svg" alt="License"></a>
  <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/kubeswarm/kubeswarm" alt="Go version"></a>
  <a href="https://goreportcard.com/report/github.com/kubeswarm/kubeswarm"><img src="https://goreportcard.com/badge/github.com/kubeswarm/kubeswarm" alt="Go Report Card"></a>
</p>

**Orchestrate AI agents at swarm scale.** kubeswarm is a Kubernetes operator that manages LLM-powered agents as first-class resources. Define your agents in YAML, connect MCP tools, deploy with `kubectl apply` and operate with the same Kubernetes tooling you already use for services.

> **Status: alpha.** Core primitives are stable. Not recommended for production workloads yet.

---

Full documentation at **[docs.kubeswarm.io](https://docs.kubeswarm.io)**.

---

## Prerequisites

- Kubernetes 1.35+
- Helm 3.16+
- kubectl

## Quick start

```bash
# 1. Add the Helm repo
helm repo add kubeswarm https://kubeswarm.github.io/helm-charts/
helm repo update

# 2. Install the operator
helm install kubeswarm kubeswarm/kubeswarm \
  --namespace swarm-system --create-namespace

# 3. Create a Secret with your LLM API key in your team's namespace
kubectl create secret generic my-llm-keys \
  --namespace default \
  --from-literal=ANTHROPIC_API_KEY=sk-ant-...

# 4. Apply a sample team (references the secret directly in the CR)
kubectl apply -f https://raw.githubusercontent.com/kubeswarm/cookbook/main/teams/01-simple-pipeline/blog-writer.yaml

# 5. Trigger a run
swarm trigger blog-writer-team -n default \
  --input '{"topic": "Kubernetes operators explained"}'

# 6. Watch it run
swarm status -n default
```

Install the `swarm` CLI for local development: see [kubeswarm-cli](https://github.com/kubeswarm/kubeswarm-cli).

## Contributing

Issues, ideas and PRs are welcome. See [CONTRIBUTING.md](./CONTRIBUTING.md) for architecture notes and design decisions before diving in.

## Code of Conduct

This project follows the [Contributor Covenant Code of Conduct](./CODE_OF_CONDUCT.md).

## License

Apache 2.0 - see [LICENSE](./LICENSE)
