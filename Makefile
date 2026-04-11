# kubeswarm Makefile
# Single source of truth for CI, local dev, and code generation.
# Run `make help` for all available targets.

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

# ---------------------------------------------------------------------------
# Variables
# ---------------------------------------------------------------------------

LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	@mkdir -p "$(LOCALBIN)"

KIND_CLUSTER   ?= kubeswarm
CONTROLLER_IMG ?= kubeswarm-controller:dev
RUNTIME_IMG    ?= kubeswarm-runtime:dev

# Tool versions - pinned for reproducibility.
GOLANGCI_LINT_VERSION  ?= v2.8.0
GOVULNCHECK_VERSION    ?= v1.1.4
GO_LICENSES_VERSION    ?= v1.6.0
CONTROLLER_GEN_VERSION ?= v0.20.1
KUSTOMIZE_VERSION      ?= v5.8.1
TRUFFLEHOG_VERSION     ?= 3.88.2

# Tool paths.
GOLANGCI_LINT  = $(LOCALBIN)/golangci-lint
GOVULNCHECK    = $(LOCALBIN)/govulncheck
GO_LICENSES    = $(LOCALBIN)/go-licenses
CONTROLLER_GEN = $(LOCALBIN)/controller-gen
KUSTOMIZE      = $(LOCALBIN)/kustomize
ENVTEST        = $(LOCALBIN)/setup-envtest
TRUFFLEHOG     = $(LOCALBIN)/trufflehog

# Envtest versions - auto-detected from go.mod.
ENVTEST_VERSION ?= $(shell v='$(call gomodver,sigs.k8s.io/controller-runtime)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_VERSION manually" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?([0-9]+)\.([0-9]+).*/release-\1.\2/')

ENVTEST_K8S_VERSION ?= $(shell v='$(call gomodver,k8s.io/api)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_K8S_VERSION manually" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?[0-9]+\.([0-9]+).*/1.\1/')

# CI step counter.
STEP = 0
define step
$(eval STEP=$(shell echo $$(( $(STEP) + 1 ))))
@printf "\n\033[1;34m[$(STEP)/14] %s\033[0m\n" "$(1)"
endef

# ---------------------------------------------------------------------------
##@ Development
# ---------------------------------------------------------------------------

.PHONY: help
help: ## Show this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

.PHONY: setup
setup: ## One-time dev setup: install git hooks.
	@ln -sf ../../scripts/pre-push .git/hooks/pre-push
	@ln -sf ../../scripts/commit-msg .git/hooks/commit-msg
	@echo "Installed git hooks (commit-msg, pre-push)."

.PHONY: generate
generate: controller-gen ## Regenerate deepcopy methods after type changes.
	@GOWORK=off "$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: manifests
manifests: controller-gen ## Regenerate CRDs and RBAC after marker changes.
	@GOWORK=off "$(CONTROLLER_GEN)" rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

HELM_CHART ?= ../helm-charts/charts/kubeswarm

.PHONY: helm-sync
helm-sync: manifests ## Sync CRDs into the Helm chart and verify RBAC alignment.
	@cp config/crd/bases/*.yaml $(HELM_CHART)/crds/
	@echo "helm-sync: CRDs synced ($$(ls config/crd/bases/*.yaml | wc -l | tr -d ' ') files)."
	@bash scripts/rbac-check.sh config/rbac/role.yaml $(HELM_CHART)/templates/clusterrole.yaml

.PHONY: build
build: generate ## Compile controller and runtime binaries.
	@echo "Building controller..."
	@CGO_ENABLED=0 go build -o bin/kubeswarm-controller ./runtime/cmd/operator/
	@echo "Building runtime..."
	@CGO_ENABLED=0 go build -o bin/kubeswarm-runtime ./runtime/cmd/agent/

IMG         ?= ghcr.io/kubeswarm/kubeswarm-controller:latest
RUNTIME_REL ?= ghcr.io/kubeswarm/kubeswarm-runtime:latest

.PHONY: build-installer
build-installer: manifests kustomize ## Build dist/install.yaml for kubectl-based installs.
	@mkdir -p dist
	@cd config/manager && "$(KUSTOMIZE)" edit set image kubeswarm-controller=$(IMG)
	@"$(KUSTOMIZE)" build config/default > dist/install.yaml
	@sed -i.bak 's|--agent-image=.*|--agent-image=$(RUNTIME_REL)|' dist/install.yaml && rm -f dist/install.yaml.bak

.PHONY: clean
clean: ## Remove build artifacts and Go test cache.
	@rm -f bin/kubeswarm-controller bin/kubeswarm-runtime cover.out runtime/cover.out
	@go clean -testcache

# ---------------------------------------------------------------------------
##@ Quality
# ---------------------------------------------------------------------------

.PHONY: fmt
fmt: ## Format all Go code.
	@gofmt -w .
	@cd runtime && gofmt -w .

.PHONY: test
test: test-controller test-runtime ## Run all tests.

.PHONY: lint
lint: fmt lint-controller lint-runtime ## Format and run all linters.

.PHONY: lint-fix
lint-fix: fmt lint-fix-controller lint-fix-runtime ## Format and run all linters with auto-fix.

.PHONY: verify
verify: verify-controller verify-runtime ## Verify all module dependencies.

.PHONY: tidy
tidy: tidy-controller tidy-runtime ## Run go mod tidy on all modules.

.PHONY: coverage
coverage: test ## Run all tests and open HTML coverage reports.
	@go tool cover -html=cover.out
	@cd runtime && go tool cover -html=cover.out

.PHONY: check-unicode
check-unicode: ## Reject Unicode smart quotes in api/ types (breaks CEL markers).
	@if LC_ALL=C grep -rn $$'\xe2\x80\x9c\|\xe2\x80\x9d\|\xe2\x80\x98\|\xe2\x80\x99' api/ 2>/dev/null; then \
		echo "ERROR: Unicode smart quotes found in api/ types. These break CEL validation markers."; \
		exit 1; \
	fi

.PHONY: check-generate
check-generate: generate manifests ## Verify generated code is up-to-date.
	@if [ -n "$$(git diff --name-only)" ]; then \
		echo "ERROR: Generated files are out of date. Run 'make generate manifests' and commit."; \
		git diff --name-only; \
		exit 1; \
	fi

# ---------------------------------------------------------------------------
##@ Security
# ---------------------------------------------------------------------------

.PHONY: vulncheck
vulncheck: vulncheck-controller vulncheck-runtime ## Scan all modules for known vulnerabilities.

.PHONY: license-check
license-check: license-check-controller license-check-runtime ## Verify no incompatible licenses in either module.

.PHONY: scan-secrets
scan-secrets: trufflehog ## Scan repo for leaked secrets.
	@"$(TRUFFLEHOG)" filesystem . --fail --exclude-paths=.trufflehog-ignore

# ---------------------------------------------------------------------------
##@ CI
# ---------------------------------------------------------------------------

.PHONY: ci
ci: ## Run the full CI pipeline locally.
	@printf "\033[1;36m========================================\033[0m\n"
	@printf "\033[1;36m  kubeswarm CI\033[0m\n"
	@printf "\033[1;36m========================================\033[0m\n"
	@$(MAKE) --no-print-directory _ci
	@printf "\n\033[1;32m========================================\033[0m\n"
	@printf "\033[1;32m  CI passed.\033[0m\n"
	@printf "\033[1;32m========================================\033[0m\n"

.PHONY: _ci
_ci: _clean _check-generate _check-unicode _verify-controller _lint-controller _verify-runtime _lint-runtime _test-controller _test-runtime _license-check-controller _license-check-runtime _vulncheck-controller _vulncheck-runtime _scan-secrets

.PHONY: _clean
_clean:
	$(call step,Clean)
	@rm -f cover.out runtime/cover.out
	@go clean -testcache

.PHONY: _check-generate
_check-generate: controller-gen
	$(call step,Check generated code)
	@GOWORK=off "$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt" paths="./..."
	@GOWORK=off "$(CONTROLLER_GEN)" rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases
	@if [ -n "$$(git diff --name-only)" ]; then \
		echo "ERROR: Generated files are out of date. Run 'make generate manifests' and commit."; \
		git diff --name-only; \
		exit 1; \
	fi
	@echo "Generated code is up-to-date."

.PHONY: _check-unicode
_check-unicode:
	$(call step,Check unicode)
	@if LC_ALL=C grep -rn $$'\xe2\x80\x9c\|\xe2\x80\x9d\|\xe2\x80\x98\|\xe2\x80\x99' api/ 2>/dev/null; then \
		echo "ERROR: Unicode smart quotes found in api/ types. These break CEL validation markers."; \
		exit 1; \
	fi
	@echo "No smart quotes found."

.PHONY: _verify-controller
_verify-controller:
	$(call step,Verify controller)
	@GOWORK=off go mod verify

.PHONY: _lint-controller
_lint-controller: golangci-lint
	$(call step,Lint controller)
	@GOWORK=off "$(GOLANGCI_LINT)" run

.PHONY: _verify-runtime
_verify-runtime:
	$(call step,Verify runtime)
	@cd runtime && GOWORK=off go mod verify

.PHONY: _lint-runtime
_lint-runtime: golangci-lint
	$(call step,Lint runtime)
	@cd runtime && GOWORK=off "$(GOLANGCI_LINT)" run

.PHONY: _test-controller
_test-controller: setup-envtest
	$(call step,Test controller)
	@KUBEBUILDER_ASSETS="$$($(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" \
		GOWORK=off go test $$(GOWORK=off go list ./... | grep -v /e2e) -coverprofile cover.out -covermode=atomic

.PHONY: _test-runtime
_test-runtime:
	$(call step,Test runtime)
	@cd runtime && GOWORK=off go test ./... -coverprofile cover.out -covermode=atomic

.PHONY: _license-check-controller
_license-check-controller: go-licenses
	$(call step,License check - controller)
	@GOWORK=off "$(GO_LICENSES)" report ./... 2>/dev/null | awk -F',' '{print $$NF}' | sort -u | tee /tmp/licenses.txt
	@if grep -qiE 'GPL|AGPL|SSPL|EUPL|CC-BY-SA' /tmp/licenses.txt; then echo "ERROR: Incompatible license found in controller module"; exit 1; fi
	@echo "Controller licenses OK"

.PHONY: _license-check-runtime
_license-check-runtime: go-licenses
	$(call step,License check - runtime)
	@cd runtime && GOWORK=off "$(GO_LICENSES)" report ./... 2>/dev/null | awk -F',' '{print $$NF}' | sort -u | tee /tmp/licenses-runtime.txt
	@if grep -qiE 'GPL|AGPL|SSPL|EUPL|CC-BY-SA' /tmp/licenses-runtime.txt; then echo "ERROR: Incompatible license found in runtime module"; exit 1; fi
	@echo "Runtime licenses OK"

.PHONY: _vulncheck-controller
_vulncheck-controller: govulncheck
	$(call step,Vulnerability scan - controller)
	@GOWORK=off "$(GOVULNCHECK)" ./...

.PHONY: _vulncheck-runtime
_vulncheck-runtime: govulncheck
	$(call step,Vulnerability scan - runtime)
	@cd runtime && GOWORK=off "$(GOVULNCHECK)" ./...

.PHONY: _scan-secrets
_scan-secrets: trufflehog
	$(call step,Secret scan)
	@"$(TRUFFLEHOG)" filesystem . --fail --exclude-paths=.trufflehog-ignore

# ---------------------------------------------------------------------------
##@ Local cluster
# ---------------------------------------------------------------------------

.PHONY: kind-create
kind-create: ## Create Kind cluster if it doesn't exist.
	@if kind get clusters 2>/dev/null | grep -q "^$(KIND_CLUSTER)$$"; then \
		echo "Kind cluster '$(KIND_CLUSTER)' already exists."; \
	else \
		kind create cluster --name $(KIND_CLUSTER); \
	fi

.PHONY: kind-delete
kind-delete: ## Delete the Kind cluster.
	@kind delete cluster --name $(KIND_CLUSTER)

.PHONY: local-up
local-up: kind-create generate manifests ## Build, load, and deploy to local Kind cluster.
	@echo "Building controller..."
	@CGO_ENABLED=0 GOOS=linux GOARCH=$$(go env GOARCH) go build -o bin/kubeswarm-controller ./runtime/cmd/operator/
	@echo "Building runtime..."
	@CGO_ENABLED=0 GOOS=linux GOARCH=$$(go env GOARCH) go build -o bin/kubeswarm-runtime ./runtime/cmd/agent/
	@echo "Building images..."
	@docker build -q -t $(CONTROLLER_IMG) -f Dockerfile.local bin/
	@docker build -q -t $(RUNTIME_IMG) -f Dockerfile.runtime.local bin/
	@echo "Loading into Kind..."
	@kind load docker-image $(CONTROLLER_IMG) $(RUNTIME_IMG) --name $(KIND_CLUSTER)
	@echo "Applying manifests..."
	@kubectl apply -k config/dev
	@kubectl -n kubeswarm-system rollout restart deployment kubeswarm-controller-manager 2>/dev/null || true
	@kubectl -n kubeswarm-system rollout status deployment kubeswarm-controller-manager --timeout=60s
	@echo ""
	@echo "Ready."

.PHONY: local-up-helm
local-up-helm: kind-create generate manifests helm-sync ## Build, load, and deploy via Helm.
	@echo "Building controller..."
	@CGO_ENABLED=0 GOOS=linux GOARCH=$$(go env GOARCH) go build -o bin/kubeswarm-controller ./runtime/cmd/operator/
	@echo "Building runtime..."
	@CGO_ENABLED=0 GOOS=linux GOARCH=$$(go env GOARCH) go build -o bin/kubeswarm-runtime ./runtime/cmd/agent/
	@echo "Building images..."
	@docker build -q -t $(CONTROLLER_IMG) -f Dockerfile.local bin/
	@docker build -q -t $(RUNTIME_IMG) -f Dockerfile.runtime.local bin/
	@echo "Loading into Kind..."
	@kind load docker-image $(CONTROLLER_IMG) $(RUNTIME_IMG) --name $(KIND_CLUSTER)
	@echo "Deploying Redis for local dev..."
	@kubectl --context kind-$(KIND_CLUSTER) create namespace kubeswarm-system 2>/dev/null || true
	@kubectl --context kind-$(KIND_CLUSTER) -n kubeswarm-system apply -f config/dev/redis.yaml
	@kubectl --context kind-$(KIND_CLUSTER) -n kubeswarm-system wait --for=condition=ready pod/redis --timeout=60s
	@echo "Installing via Helm..."
	@helm upgrade --install kubeswarm $(HELM_CHART) \
		-f $(HELM_CHART)/values.local.yaml \
		--namespace kubeswarm-system --create-namespace \
		--kube-context kind-$(KIND_CLUSTER)
	@kubectl --context kind-$(KIND_CLUSTER) -n kubeswarm-system rollout status deployment/kubeswarm --timeout=90s
	@echo ""
	@echo "Ready (Helm)."

.PHONY: local-down
local-down: ## Remove kubeswarm from the local cluster (kustomize).
	@kubectl delete -k config/dev --ignore-not-found

.PHONY: local-down-helm
local-down-helm: ## Remove kubeswarm from the local cluster (Helm).
	@helm uninstall kubeswarm --namespace kubeswarm-system 2>/dev/null || true

.PHONY: local-logs
local-logs: ## Tail controller logs.
	@kubectl -n kubeswarm-system logs -f deployment/kubeswarm-controller-manager

# ---------------------------------------------------------------------------
# Per-module targets (used by CI jobs for parallel execution)
# ---------------------------------------------------------------------------

.PHONY: test-controller
test-controller: setup-envtest
	@KUBEBUILDER_ASSETS="$$($(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" \
		GOWORK=off go test $$(GOWORK=off go list ./... | grep -v /e2e) -coverprofile cover.out -covermode=atomic

.PHONY: test-runtime
test-runtime:
	@cd runtime && GOWORK=off go test ./... -coverprofile cover.out -covermode=atomic

.PHONY: lint-controller
lint-controller: golangci-lint
	@GOWORK=off "$(GOLANGCI_LINT)" run

.PHONY: lint-runtime
lint-runtime: golangci-lint
	@cd runtime && GOWORK=off "$(GOLANGCI_LINT)" run

.PHONY: lint-fix-controller
lint-fix-controller: golangci-lint check-unicode
	@GOWORK=off "$(GOLANGCI_LINT)" run --fix

.PHONY: lint-fix-runtime
lint-fix-runtime: golangci-lint
	@cd runtime && GOWORK=off "$(GOLANGCI_LINT)" run --fix

.PHONY: verify-controller
verify-controller:
	@GOWORK=off go mod verify

.PHONY: verify-runtime
verify-runtime:
	@cd runtime && GOWORK=off go mod verify

.PHONY: tidy-controller
tidy-controller:
	@GOWORK=off go mod tidy

.PHONY: tidy-runtime
tidy-runtime:
	@cd runtime && GOWORK=off go mod tidy

.PHONY: vulncheck-controller
vulncheck-controller: govulncheck
	@GOWORK=off "$(GOVULNCHECK)" ./...

.PHONY: vulncheck-runtime
vulncheck-runtime: govulncheck
	@cd runtime && GOWORK=off "$(GOVULNCHECK)" ./...

.PHONY: license-check-controller
license-check-controller: go-licenses
	@GOWORK=off "$(GO_LICENSES)" report ./... 2>/dev/null | awk -F',' '{print $$NF}' | sort -u | tee /tmp/licenses.txt
	@if grep -qiE 'GPL|AGPL|SSPL|EUPL|CC-BY-SA' /tmp/licenses.txt; then echo "ERROR: Incompatible license found in controller module"; exit 1; fi
	@echo "Controller licenses OK"

.PHONY: license-check-runtime
license-check-runtime: go-licenses
	@cd runtime && GOWORK=off "$(GO_LICENSES)" report ./... 2>/dev/null | awk -F',' '{print $$NF}' | sort -u | tee /tmp/licenses-runtime.txt
	@if grep -qiE 'GPL|AGPL|SSPL|EUPL|CC-BY-SA' /tmp/licenses-runtime.txt; then echo "ERROR: Incompatible license found in runtime module"; exit 1; fi
	@echo "Runtime licenses OK"

# ---------------------------------------------------------------------------
##@ Dependencies (auto-installed, pinned versions)
# ---------------------------------------------------------------------------

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT)
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

.PHONY: govulncheck
govulncheck: $(GOVULNCHECK)
$(GOVULNCHECK): $(LOCALBIN)
	$(call go-install-tool,$(GOVULNCHECK),golang.org/x/vuln/cmd/govulncheck,$(GOVULNCHECK_VERSION))

.PHONY: go-licenses
go-licenses: $(GO_LICENSES)
$(GO_LICENSES): $(LOCALBIN)
	$(call go-install-tool,$(GO_LICENSES),github.com/google/go-licenses,$(GO_LICENSES_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN)
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_GEN_VERSION))

.PHONY: kustomize
kustomize: $(KUSTOMIZE)
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest
	@"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path > /dev/null 2>&1

.PHONY: envtest
envtest: $(ENVTEST)
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: trufflehog
trufflehog: $(TRUFFLEHOG)
$(TRUFFLEHOG): $(LOCALBIN)
	@[ -f "$(TRUFFLEHOG)-$(TRUFFLEHOG_VERSION)" ] || { \
		set -e; \
		echo "Downloading trufflehog $(TRUFFLEHOG_VERSION)"; \
		curl -sSfL https://raw.githubusercontent.com/trufflesecurity/trufflehog/main/scripts/install.sh \
			| sh -s -- -b "$(LOCALBIN)" v$(TRUFFLEHOG_VERSION); \
		mv "$(LOCALBIN)/trufflehog" "$(TRUFFLEHOG)-$(TRUFFLEHOG_VERSION)"; \
	}
	@ln -sf "$$(realpath "$(TRUFFLEHOG)-$(TRUFFLEHOG_VERSION)")" "$(TRUFFLEHOG)"

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
