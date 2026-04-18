<p align="center">
  <img src="https://assets.kubeswarm.io/logo.svg" width="200" alt="kubeswarm">
</p>

<h3 align="center">Agents are workloads. Manage them like it.</h3>

<p align="center">
  <a href="https://github.com/kubeswarm/kubeswarm/actions/workflows/ci.yml"><img src="https://github.com/kubeswarm/kubeswarm/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/kubeswarm/kubeswarm/releases"><img src="https://img.shields.io/github/v/release/kubeswarm/kubeswarm?include_prereleases" alt="GitHub release"></a>
  <a href="./LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-blue.svg" alt="License"></a>
  <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/kubeswarm/kubeswarm" alt="Go version"></a>
  <a href="https://goreportcard.com/report/github.com/kubeswarm/kubeswarm"><img src="https://goreportcard.com/badge/github.com/kubeswarm/kubeswarm" alt="Go Report Card"></a>
</p>

Kubernetes operator that manages AI agents as first-class resources. Define agents in YAML, connect MCP tools, compose multi-agent pipelines, and operate with the same tooling you already use for services.

> **Status: v0.2.0-alpha** - Core primitives are functional. API is `v1alpha1` and may change between minor versions. Not recommended for production workloads yet. See [VERSIONING.md](./VERSIONING.md).

---

## What it does

kubeswarm introduces CRDs that model every part of an AI agent deployment:

| CRD               | Purpose                                                                     |
| ----------------- | --------------------------------------------------------------------------- |
| **SwarmAgent**    | LLM agent with model, system prompt, MCP tools, guardrails, and autoscaling |
| **SwarmTeam**     | Multi-agent pipeline (DAG, sequential, or LLM-routed)                       |
| **SwarmRun**      | Single execution of a team pipeline with audit trail                        |
| **SwarmBudget**   | Token spend tracking and enforcement per team/namespace                     |
| **SwarmRegistry** | Agent capability discovery and delegation                                   |
| **SwarmSettings** | Shared configuration (MCP servers, context policies)                        |
| **SwarmMemory**   | Vector memory backends (pgvector, Qdrant) for agent recall                  |
| **SwarmEvent**    | Trigger pipeline runs from external events (webhooks, cron)                 |
| **SwarmNotify**   | Run completion notifications (webhook, Slack)                               |
| **SwarmPolicy**   | Governance constraints (model allow/deny, token limits, tool restrictions)  |

All resources are namespace-scoped. `kubectl get kubeswarm -A` shows everything.

## Key features

- **Multi-provider** - Anthropic, OpenAI, Google Gemini, or any provider via gRPC plugin
- **Reasoning support** - Anthropic extended thinking, OpenAI reasoning effort, guardrail clamping
- **MCP tool integration** - connect any MCP server; dynamic tool discovery; per-tool trust levels
- **Agent-to-agent** - gateway dispatch, advisor consultations, and capability-based routing across agents
- **Pipeline orchestration** - DAG-based, sequential, or LLM-routed dispatch with step validation
- **Governance** - SwarmPolicy enforces model allow/deny lists, token limits, and tool restrictions across namespaces
- **Cost controls** - per-agent token limits, daily budgets, circuit breakers, spend tracking
- **Observability** - OTel metrics and traces, structured audit trail, MCP health monitoring
- **Security** - pod hardening, network policies, prompt injection defense, tool allow/deny lists
- **Autoscaling** - KEDA-based scale-to-zero and demand-driven replica management

---

Full documentation at **[docs.kubeswarm.io](https://docs.kubeswarm.io)**.

---

## Prerequisites

- Kubernetes 1.35+
- kubectl
- Docker 24+
- kind 0.27+ (for local development)

## Quick start (local development)

```bash
# 1. Clone and setup
git clone https://github.com/kubeswarm/kubeswarm.git
cd kubeswarm
make setup

# 2. Run the full CI pipeline locally
make ci

# 3. Deploy to a local Kind cluster
make local-up

# 4. Verify the controller is running
kubectl get pods -n kubeswarm-system
```

## Quick start (Helm)

```bash
# 1. Add the Helm repo
helm repo add kubeswarm https://kubeswarm.github.io/helm-charts/
helm repo update

# 2. Install the operator
helm install kubeswarm kubeswarm/kubeswarm \
  --namespace kubeswarm-system --create-namespace

# 3. Create a Secret with your LLM API key
kubectl create secret generic llm-api-key \
  --namespace default \
  --from-literal=ANTHROPIC_API_KEY=sk-ant-...

# 4. Apply a sample team
kubectl apply -f https://raw.githubusercontent.com/kubeswarm/kubeswarm-cookbook/main/teams/01-simple-pipeline/blog-writer.yaml

# 5. Trigger a run
swarm trigger blog-writer-team -n default \
  --input '{"topic": "Kubernetes operators explained"}'

# 6. Watch it run
swarm status -n default
```

Install the `swarm` CLI: [kubeswarm-cli](https://github.com/kubeswarm/kubeswarm-cli).

## Project structure

```
kubeswarm/
  api/v1alpha1/          CRD type definitions
  internal/controller/   Reconcilers (one per CRD)
  internal/webhook/      Admission webhooks
  internal/mcpgateway/   MCP SSE gateway for agent-to-agent calls
  pkg/                   Shared packages (audit, costs, healthz, observability)
  runtime/               Nested Go module - agent binary + vendor SDK implementations
    cmd/kubeswarm-runtime/      Agent runtime entrypoint
    cmd/kubeswarm-controller/   Controller binary entrypoint
    pkg/providers/       LLM provider implementations (Anthropic, OpenAI, Gemini)
    pkg/queue/           Task queue backends (Redis)
    pkg/vectors/         Vector store backends (pgvector, Qdrant)
    pkg/artifacts/       Artifact store backends (S3, GCS)
```

The core `kubeswarm/` module has zero vendor SDK imports. All LLM, queue, and storage
SDK dependencies live in `runtime/` (nested module) to keep the operator's dependency
tree clean.

## Related repos

| Repo                                                                  | Description                                  |
| --------------------------------------------------------------------- | -------------------------------------------- |
| [helm-charts](https://github.com/kubeswarm/helm-charts)               | Helm chart for operator deployment           |
| [kubeswarm-cli](https://github.com/kubeswarm/kubeswarm-cli)           | Local dev CLI (`swarm run`, `swarm trigger`) |
| [kubeswarm-docs](https://github.com/kubeswarm/kubeswarm-docs)         | Documentation site (docs.kubeswarm.io)       |
| [kubeswarm-cookbook](https://github.com/kubeswarm/kubeswarm-cookbook) | Example pipelines and recipes                |

## Contributing

Issues, ideas and PRs are welcome. See [CONTRIBUTING.md](./CONTRIBUTING.md) for guidelines.

## License

Apache 2.0 - see [LICENSE](./LICENSE)
