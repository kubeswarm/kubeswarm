# kubeswarm-runtime Makefile
# Usage: make <target>
# Run `make help` for all available targets.

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

##@ Core

.PHONY: all
all: lint test ## Lint and test everything.

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Quality

.PHONY: test
test: ## Run all tests.
	GOWORK=off go test ./... -coverprofile cover.out -covermode=atomic

.PHONY: coverage
coverage: test ## Run tests and open HTML coverage report.
	go tool cover -html=cover.out

.PHONY: lint
lint: golangci-lint ## Run golangci-lint (read-only).
	GOWORK=off "$(GOLANGCI_LINT)" run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint with auto-fix.
	GOWORK=off "$(GOLANGCI_LINT)" run --fix

.PHONY: tidy
tidy: ## Run go mod tidy.
	GOWORK=off go mod tidy

.PHONY: verify
verify: ## Verify module dependencies.
	GOWORK=off go mod verify

##@ Build

.PHONY: build-agent
build-agent: ## Build the agent binary.
	GOWORK=off go build -o bin/agent ./cmd/agent/

.PHONY: build-operator
build-operator: ## Build the operator binary.
	GOWORK=off go build -o bin/operator ./cmd/operator/

.PHONY: build
build: build-agent build-operator ## Build all binaries.

##@ Dependencies

LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

GOLANGCI_LINT         = $(LOCALBIN)/golangci-lint
GOLANGCI_LINT_VERSION ?= v2.8.0

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
