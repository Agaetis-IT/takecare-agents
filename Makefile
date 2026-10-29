# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

# Every pinned version lives in hack/versions.env (spec 04).
include hack/versions.env

MODULE := github.com/agaetis-it/takecare-agents
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
OPERATOR_IMG ?= $(IMAGE_REGISTRY)/takecare-operator:$(VERSION)
# docker-buildx pushes only when PUSH=true (the release workflow, from PR 6).
PUSH ?= false

LOCALBIN ?= $(CURDIR)/bin
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen-$(CONTROLLER_TOOLS_VERSION)
GOLANGCI_LINT ?= $(LOCALBIN)/golangci-lint-$(GOLANGCI_LINT_VERSION)
SETUP_ENVTEST ?= $(LOCALBIN)/setup-envtest-$(SETUP_ENVTEST_VERSION)
GO_LICENSES ?= $(LOCALBIN)/go-licenses-$(GO_LICENSES_VERSION)

# Containerized linters see the repository at /src, as the calling user.
DOCKER_RUN := docker run --rm --user $(shell id -u):$(shell id -g) -v "$(CURDIR)":/src -w /src

# Third-party licenses are resolved for the platform the operator ships on,
# so that NOTICE does not depend on the developer's machine.
LICENSES_ENV := GOOS=linux GOARCH=amd64 CGO_ENABLED=0

SHELL := /usr/bin/env bash -o pipefail
.SHELLFLAGS := -ec
.DEFAULT_GOAL := help

##@ General

.PHONY: help
help: ## Show this help.
	@awk 'BEGIN {FS = ":.*##"; printf "Usage: make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)

##@ Development

# Kubebuilder v4 layout, without kubebuilder's PROJECT file or kustomize
# config/: the entrypoint is cmd/operator (spec 04), and `kubebuilder create
# api` requires cmd/main.go. API types are written by hand with controller-gen
# markers; deployment manifests come from the charts (PR 3).
.PHONY: generate
generate: controller-gen ## Generate deepcopy code, CRDs and RBAC manifests.
	"$(CONTROLLER_GEN)" object:headerFile=hack/boilerplate.go.txt paths="./..."
	"$(CONTROLLER_GEN)" rbac:roleName=takecare-operator crd:allowDangerousTypes=true paths="./..." \
		output:crd:artifacts:config=config/crd output:rbac:artifacts:config=config/rbac

.PHONY: fmt
fmt: ## Run go fmt.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet.
	go vet ./...

.PHONY: test
test: setup-envtest ## Run unit, golden and envtest tests.
	KUBEBUILDER_ASSETS="$$("$(SETUP_ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" \
		go test ./... -coverprofile cover.out

##@ Lint

.PHONY: lint
lint: lint-go lint-py lint-sh lint-md lint-spdx ## Run every linter.

.PHONY: lint-go
lint-go: golangci-lint ## Run golangci-lint.
	"$(GOLANGCI_LINT)" run

.PHONY: lint-py
lint-py: ## Run ruff on the Python code.
	$(DOCKER_RUN) $(RUFF_IMAGE) check --no-cache .
	$(DOCKER_RUN) $(RUFF_IMAGE) format --check --no-cache .

.PHONY: lint-sh
lint-sh: ## Run shellcheck on the shell scripts.
	@files=$$(find . -name '*.sh' -not -path './.git/*' -not -path './bin/*' -not -path './spikes/*'); \
	if [ -n "$$files" ]; then $(DOCKER_RUN) $(SHELLCHECK_IMAGE) $$files; else echo "lint-sh: no shell script"; fi

.PHONY: lint-md
lint-md: ## Run markdownlint on the documentation.
	$(DOCKER_RUN) $(MARKDOWNLINT_IMAGE)

.PHONY: lint-spdx
lint-spdx: ## Check the SPDX header of our source files.
	hack/check-spdx.sh

.PHONY: gitleaks
gitleaks: ## Scan the working tree for secrets (GITLEAKS_MODE=git scans the history).
	@# safe.directory: the mounted repository belongs to another uid inside the
	@# container, and git then returns nothing. An empty scan is a failure.
	@out=$$($(DOCKER_RUN) -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0='*' \
		$(GITLEAKS_IMAGE) $(or $(GITLEAKS_MODE),dir) --redact --no-banner /src 2>&1) || { echo "$$out"; exit 1; }; \
	echo "$$out"; \
	if echo "$$out" | grep -q 'scanned ~0 bytes'; then echo "gitleaks scanned nothing: failing"; exit 1; fi

.PHONY: licenses
licenses: go-licenses ## Check dependency licenses and regenerate NOTICE; fail if it changed.
	$(LICENSES_ENV) "$(GO_LICENSES)" check ./... --ignore $(MODULE) \
		--disallowed_types=forbidden,restricted,unknown
	$(LICENSES_ENV) "$(GO_LICENSES)" report ./... --ignore $(MODULE) \
		--template hack/notice.tpl > NOTICE
	@if [ -n "$$(git status --porcelain -- NOTICE)" ]; then \
		git --no-pager diff -- NOTICE; \
		echo "NOTICE is out of date: commit the regenerated file."; exit 1; \
	fi

##@ Build

.PHONY: build
build: ## Build the operator binary.
	go build -trimpath -o bin/operator ./cmd/operator

.PHONY: run
run: ## Run the operator against the current kube context.
	go run ./cmd/operator --metrics-secure=false --metrics-bind-address=:8080

DOCKER_BUILD_ARGS := --build-arg GO_BUILD_IMAGE=$(GO_BUILD_IMAGE) --build-arg RUNTIME_IMAGE=$(RUNTIME_IMAGE) \
	--build-arg GO_LICENSES_VERSION=$(GO_LICENSES_VERSION)

.PHONY: docker-build
docker-build: ## Build the operator image for the local platform.
	docker build $(DOCKER_BUILD_ARGS) -t $(OPERATOR_IMG) .

.PHONY: docker-buildx
docker-buildx: ## Build the operator image for amd64 and arm64 (pushed only with PUSH=true).
	docker buildx build --platform $(IMAGE_PLATFORMS) $(DOCKER_BUILD_ARGS) -t $(OPERATOR_IMG) \
		$(if $(filter true,$(PUSH)),--push,) .

##@ Not yet implemented

.PHONY: e2e demo-up demo-incident demo-verdict demo-screens demo-bench demo-reset demo-down
e2e: ## End-to-end tests on k3d (PR 6).
	@echo "$@: not implemented yet, see CLAUDE.md (PR 6)"; exit 1
demo-up demo-incident demo-verdict demo-screens demo-bench demo-reset demo-down: ## Demo mode (PR 5).
	@echo "$@: not implemented yet, see CLAUDE.md (PR 5)"; exit 1

##@ Tools

.PHONY: controller-gen golangci-lint setup-envtest go-licenses
controller-gen: $(CONTROLLER_GEN)
golangci-lint: $(GOLANGCI_LINT)
setup-envtest: $(SETUP_ENVTEST)
go-licenses: $(GO_LICENSES)

$(CONTROLLER_GEN):
	$(call go-install-tool,$@,sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))
$(GOLANGCI_LINT):
	$(call go-install-tool,$@,github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))
$(SETUP_ENVTEST):
	$(call go-install-tool,$@,sigs.k8s.io/controller-runtime/tools/setup-envtest,$(SETUP_ENVTEST_VERSION))
$(GO_LICENSES):
	$(call go-install-tool,$@,github.com/google/go-licenses/v2,$(GO_LICENSES_VERSION))

# go-install-tool installs a Go tool at a pinned version into $(LOCALBIN),
# under a versioned file name so that a bump reinstalls it.
# $1: target path, $2: package, $3: version.
define go-install-tool
@mkdir -p "$(LOCALBIN)"
@echo "Installing $(2)@$(3)"
@tmp=$$(mktemp -d) && GOBIN="$$tmp" go install "$(2)@$(3)" && mv "$$tmp"/* "$(1)" && rm -rf "$$tmp"
endef
