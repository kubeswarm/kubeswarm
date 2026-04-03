# kubeswarm operator Makefile
# Usage: make <target>
# Run `make help` for all available targets.

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

HELM_CHART ?= ../helm-charts/charts/kubeswarm

##@ Core

.PHONY: all
all: generate manifests lint-fix test ## Generate, lint and test everything.

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Code generation

.PHONY: generate
generate: controller-gen ## Regenerate zz_generated.deepcopy.go.
	GOWORK=off "$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: manifests
manifests: controller-gen kustomize ## Regenerate CRDs, RBAC and webhook manifests.
	GOWORK=off "$(CONTROLLER_GEN)" rbac:roleName=manager-role crd:allowDangerousTypes=true webhook paths="./..." output:crd:artifacts:config=config/crd/bases
	GOWORK=off "$(KUSTOMIZE)" build config/default > /dev/null

##@ Quality

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint with auto-fix.
	GOWORK=off "$(GOLANGCI_LINT)" run --fix

.PHONY: lint
lint: golangci-lint ## Run golangci-lint (read-only).
	GOWORK=off "$(GOLANGCI_LINT)" run

.PHONY: security
security: golangci-lint ## Run security checks (gosec).
	GOWORK=off "$(GOLANGCI_LINT)" run --enable gosec

.PHONY: test
test: manifests generate setup-envtest ## Run all tests.
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" \
		GOWORK=off go test $$(GOWORK=off go list ./... | grep -v /e2e) -coverprofile cover.out -covermode=atomic

.PHONY: coverage
coverage: test ## Run tests and open HTML coverage report.
	go tool cover -html=cover.out

##@ Helm

.PHONY: helm-sync
helm-sync: manifests ## Sync CRDs into the Helm chart and verify RBAC alignment.
	cp config/crd/bases/*.yaml $(HELM_CHART)/crds/
	@missing=$$( \
	  grep '^\s*- swarm' config/rbac/role.yaml | sed 's|/.*||;s|.*- ||' | sort -u | while read res; do \
	    grep -q "$$res" $(HELM_CHART)/templates/clusterrole.yaml || echo "  $$res"; \
	  done); \
	if [ -n "$$missing" ]; then \
	  echo "WARNING: helm-sync: resources missing from Helm ClusterRole:"; \
	  echo "$$missing"; \
	else \
	  echo "helm-sync: CRDs and RBAC in sync."; \
	fi

##@ Docs

DOCS_PATH ?= ../kubeswarm-docs/docs/reference/api.md

.PHONY: docs-api
docs-api: crd-ref-docs ## Generate API reference docs from Go types.
	GOWORK=off "$(CRD_REF_DOCS)" \
		--source-path=./api/v1alpha1 \
		--config=./docs/crd-ref-docs.yaml \
		--output-path=/tmp/swarm-api-ref.md \
		--renderer=markdown
	@printf -- '---\nid: api\ntitle: API Reference\nsidebar_position: 1\ndescription: Complete field reference for all kubeswarm/v1alpha1 CRDs.\n---\n\n' > $(DOCS_PATH)
	@sed 's/<\([a-zA-Z][a-zA-Z0-9_:-]*\)>/\&lt;\1\&gt;/g' /tmp/swarm-api-ref.md \
		| sed 's/<\/\([a-zA-Z][a-zA-Z0-9_:-]*\)>/\&lt;\/\1\&gt;/g' \
		| sed 's/Optional: \\{\\}/Optional: true/g' \
		| sed 's/Required: \\{\\}/Required: true/g' \
		| sed 's/{{/\&#123;\&#123;/g' \
		| sed 's/}}/\&#125;\&#125;/g' >> $(DOCS_PATH)
	@echo "Generated API reference -> $(DOCS_PATH)"

##@ Release

IMG ?= ghcr.io/kubeswarm/kubeswarm-operator:latest

.PHONY: build-installer
build-installer: manifests kustomize ## Build dist/install.yaml for kubectl-based installs.
	mkdir -p dist
	cd config/manager && $(KUSTOMIZE) edit set image controller=$(IMG)
	$(KUSTOMIZE) build config/default > dist/install.yaml

##@ Local dev (Kind + Helm)

HELM_CHART     ?= ../helm-charts/charts/kubeswarm
LOCAL_VALUES   ?= $(HELM_CHART)/values.local.yaml
LOCAL_NS       ?= kubeswarm-system
TEAMS_NS       ?= swarm-teams
KIND_CLUSTER   ?= kubeswarm
ARCH           := $(shell uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
GOWORK_FILE    ?= $(abspath ../go.work)
CONTAINER_TOOL ?= docker
AGENT_IMG      ?= kubeswarm-runtime:latest

.PHONY: local-up
local-up: helm-sync ## Build images, load into Kind, install/upgrade via Helm.
	CGO_ENABLED=0 GOOS=linux GOARCH=$(ARCH) go build -C runtime -o $(abspath kubeswarm-operator) ./cmd/operator/
	CGO_ENABLED=0 GOOS=linux GOARCH=$(ARCH) go build -C runtime -o $(abspath kubeswarm-runtime)  ./cmd/agent/
	$(CONTAINER_TOOL) build -t kubeswarm:latest          -f Dockerfile.local       .
	$(CONTAINER_TOOL) build -t kubeswarm-runtime:latest   -f Dockerfile.agent.local .
	rm -f kubeswarm-operator kubeswarm-runtime
	$(CONTAINER_TOOL) pull --platform linux/$(ARCH) redis:7-alpine
	kind load docker-image kubeswarm:latest        --name $(KIND_CLUSTER)
	kind load docker-image kubeswarm-runtime:latest  --name $(KIND_CLUSTER)
	$(CONTAINER_TOOL) save redis:7-alpine | \
		$(CONTAINER_TOOL) exec -i $(KIND_CLUSTER)-control-plane \
		ctr --namespace=k8s.io images import --snapshotter=overlayfs -
	helm upgrade --install kubeswarm $(HELM_CHART) \
		-f $(LOCAL_VALUES) \
		--namespace $(LOCAL_NS) --create-namespace \
		--kube-context kind-$(KIND_CLUSTER)
	kubectl --context kind-$(KIND_CLUSTER) rollout restart deployment/kubeswarm -n $(LOCAL_NS)
	kubectl --context kind-$(KIND_CLUSTER) wait --for=condition=available deployment/kubeswarm -n $(LOCAL_NS) --timeout=90s

.PHONY: local-update
local-update: ## Rebuild all images, reload into Kind, restart pods.
	CGO_ENABLED=0 GOOS=linux GOARCH=$(ARCH) go build -C runtime -o $(abspath kubeswarm-operator) ./cmd/operator/
	CGO_ENABLED=0 GOOS=linux GOARCH=$(ARCH) go build -C runtime -o $(abspath kubeswarm-runtime)  ./cmd/agent/
	$(CONTAINER_TOOL) build -t kubeswarm:latest          -f Dockerfile.local       .
	$(CONTAINER_TOOL) build -t kubeswarm-runtime:latest   -f Dockerfile.agent.local .
	rm -f kubeswarm-operator kubeswarm-runtime
	kind load docker-image kubeswarm:latest        --name $(KIND_CLUSTER)
	kind load docker-image kubeswarm-runtime:latest  --name $(KIND_CLUSTER)
	kubectl --context kind-$(KIND_CLUSTER) rollout restart deployment/kubeswarm -n $(LOCAL_NS)
	kubectl --context kind-$(KIND_CLUSTER) delete deployments --all -n $(TEAMS_NS) 2>/dev/null || true

.PHONY: local-update-operator
local-update-operator: ## Rebuild and reload operator only.
	CGO_ENABLED=0 GOOS=linux GOARCH=$(ARCH) go build -C runtime -o $(abspath kubeswarm-operator) ./cmd/operator/
	$(CONTAINER_TOOL) build -t kubeswarm:latest -f Dockerfile.local .
	rm -f kubeswarm-operator
	kind load docker-image kubeswarm:latest --name $(KIND_CLUSTER)
	kubectl --context kind-$(KIND_CLUSTER) rollout restart deployment/kubeswarm -n $(LOCAL_NS)

.PHONY: local-update-agent
local-update-agent: ## Rebuild and reload agent runtime only.
	CGO_ENABLED=0 GOOS=linux GOARCH=$(ARCH) go build -C runtime -o $(abspath kubeswarm-runtime) ./cmd/agent/
	$(CONTAINER_TOOL) build -t kubeswarm-runtime:latest -f Dockerfile.agent.local .
	rm -f kubeswarm-runtime
	kind load docker-image kubeswarm-runtime:latest --name $(KIND_CLUSTER)
	kubectl --context kind-$(KIND_CLUSTER) delete deployments --all -n $(TEAMS_NS) 2>/dev/null || true

##@ Dependencies

LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

KUSTOMIZE       ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN  ?= $(LOCALBIN)/controller-gen
ENVTEST         ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT    = $(LOCALBIN)/golangci-lint
CRD_REF_DOCS    ?= $(LOCALBIN)/crd-ref-docs

KUSTOMIZE_VERSION        ?= v5.8.1
CONTROLLER_TOOLS_VERSION ?= v0.20.1
CRD_REF_DOCS_VERSION     ?= v0.3.0
GOLANGCI_LINT_VERSION    ?= v2.8.0

ENVTEST_VERSION ?= $(shell v='$(call gomodver,sigs.k8s.io/controller-runtime)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_VERSION manually" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?([0-9]+)\.([0-9]+).*/release-\1.\2/')

ENVTEST_K8S_VERSION ?= $(shell v='$(call gomodver,k8s.io/api)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_K8S_VERSION manually" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?[0-9]+\.([0-9]+).*/1.\1/')

.PHONY: kustomize
kustomize: $(KUSTOMIZE)
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN)
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest
	@"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path > /dev/null 2>&1

.PHONY: envtest
envtest: $(ENVTEST)
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: crd-ref-docs
crd-ref-docs: $(CRD_REF_DOCS)
$(CRD_REF_DOCS): $(LOCALBIN)
	$(call go-install-tool,$(CRD_REF_DOCS),github.com/elastic/crd-ref-docs,$(CRD_REF_DOCS_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT)
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

define go-install-tool
@[ -f "$(1)-$(3)" ] && [ "$$(readlink -- "$(1)" 2>/dev/null)" = "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f "$(1)" ;\
GOBIN="$(LOCALBIN)" go install $${package} ;\
mv "$(LOCALBIN)/$$(basename "$(1)")" "$(1)-$(3)" ;\
} ;\
ln -sf "$$(realpath "$(1)-$(3)")" "$(1)"
endef

define gomodver
$(shell GOWORK=off go list -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' $(1) 2>/dev/null)
endef
