# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

# Every pinned version lives in hack/versions.env (spec 04).
include hack/versions.env

MODULE := github.com/agaetis-it/takecare-agents
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# The images follow takecare-agents-<component> (issue #22).
OPERATOR_IMG ?= $(IMAGE_REGISTRY)/takecare-agents-operator:$(VERSION)
REPORT_SLACK_IMG ?= $(IMAGE_REGISTRY)/takecare-agents-mcp-report-slack:$(VERSION)
# The demo's images (spec 03): the application and the corpus MCP server.
DEMO_APP_IMG ?= $(IMAGE_REGISTRY)/takecare-agents-demo-app:$(VERSION)
MCP_CORPUS_IMG ?= $(IMAGE_REGISTRY)/takecare-agents-mcp-corpus:$(VERSION)
# docker-buildx pushes only when PUSH=true (the release workflow, from PR 6).
PUSH ?= false

LOCALBIN ?= $(CURDIR)/bin
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen-$(CONTROLLER_TOOLS_VERSION)
GOLANGCI_LINT ?= $(LOCALBIN)/golangci-lint-$(GOLANGCI_LINT_VERSION)
SETUP_ENVTEST ?= $(LOCALBIN)/setup-envtest-$(SETUP_ENVTEST_VERSION)
GO_LICENSES ?= $(LOCALBIN)/go-licenses-$(GO_LICENSES_VERSION)
HELM ?= $(LOCALBIN)/helm-$(HELM_VERSION)
KUBECONFORM ?= $(LOCALBIN)/kubeconform-$(KUBECONFORM_VERSION)
# Host platform, for the binaries downloaded from GitHub releases.
HOST_OS := $(shell go env GOOS)
HOST_ARCH := $(shell go env GOARCH)
HOST_KEY := $(shell echo $(HOST_OS)_$(HOST_ARCH) | tr a-z A-Z)
CHART_OUT ?= $(CURDIR)/.chart-out
# Helm runs with its own repository config and cache under ./bin: the
# developer's repositories are never read or touched, and dependency
# resolution by URL is not confused by the same URL registered under several
# names (seen with Helm 4.3 on a workstation).
HELM_ENV := HELM_REPOSITORY_CONFIG="$(LOCALBIN)/helm/repositories.yaml" HELM_REPOSITORY_CACHE="$(LOCALBIN)/helm/cache"
HELM_RUN := $(HELM_ENV) "$(HELM)"

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
	"$(CONTROLLER_GEN)" rbac:roleName=takecare-operator crd paths="./..." \
		output:crd:artifacts:config=config/crd output:rbac:artifacts:config=config/rbac

.PHONY: fmt
fmt: ## Run go fmt.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet.
	go vet ./...

.PHONY: test
test: setup-envtest chart-deps ## Run unit, golden, envtest and chart tests.
	KUBEBUILDER_ASSETS="$$("$(SETUP_ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" \
	HELM="$(HELM)" $(HELM_ENV) go test ./... -coverprofile cover.out

##@ Charts

.PHONY: chart-sync
chart-sync: generate ## Copy the CRDs into takecare-crds and the pinned versions into the charts.
	hack/chart-sync.sh

.PHONY: chart-check
chart-check: ## Fail if the charts drifted from config/crd or hack/versions.env.
	hack/chart-sync.sh --check

.PHONY: chart-deps
chart-deps: helm ## Download the charts' dependencies (Chart.lock is committed).
	# Classic (https) repositories must be registered before `dependency build`
	# resolves them; OCI dependencies need nothing. Registered in ./bin only.
	$(HELM_RUN) repo add --force-update prometheus-community https://prometheus-community.github.io/helm-charts >/dev/null
	$(HELM_RUN) repo add --force-update open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts >/dev/null
	$(HELM_RUN) dependency build charts/takecare-crds
	$(HELM_RUN) dependency build charts/takecare

.PHONY: chart-docs
chart-docs: ## Generate the charts' README.md from README.md.gotmpl and the values' comments (helm-docs).
	$(DOCKER_RUN) $(HELM_DOCS_IMAGE) --chart-search-root charts --document-dependency-values=false \
		--template-files README.md.gotmpl --skip-version-footer

.PHONY: chart-docs-check
chart-docs-check: chart-docs ## Fail if a chart's README.md drifted from its values.
	@if [ -n "$$(git status --porcelain charts/*/README.md)" ]; then \
		git --no-pager diff charts/*/README.md; echo "run make chart-docs and commit the README"; exit 1; fi

.PHONY: chart-lint
chart-lint: chart-check chart-docs-check chart-deps ## Drift checks, then helm lint, both charts, both profiles.
	$(HELM_RUN) lint charts/takecare-crds
	$(HELM_RUN) lint charts/takecare-crds --set demo.enabled=true
	$(HELM_RUN) lint charts/takecare -f charts/takecare/values-prod.yaml
	$(HELM_RUN) lint charts/takecare -f charts/takecare/values-demo.yaml

.PHONY: chart-smoke
chart-smoke: helm ## Install and upgrade both charts for real on a k3d cluster (hack/chart-smoke.sh).
	hack/chart-smoke.sh all

.PHONY: chart-template
chart-template: chart-deps kubeconform ## Render both profiles and validate them with kubeconform.
	rm -rf "$(CHART_OUT)" && mkdir -p "$(CHART_OUT)"
	$(HELM_RUN) template takecare-crds charts/takecare-crds -n takecare-system --set demo.enabled=true > "$(CHART_OUT)/takecare-crds.yaml"
	# values-prod.yaml refuses to render until its REQUIRED values are set.
	$(HELM_RUN) template takecare charts/takecare -n takecare-system -f charts/takecare/values-prod.yaml \
		--set platform.models[0].baseURL=https://vllm.example.internal/v1 --set platform.models[0].model=example \
		--set platform.observability.metrics.prometheusURL=http://prometheus.monitoring.svc \
		--set kagent.database.postgres.url=postgres://kagent:example@db.example.internal:5432/kagent \
		--set 'addons.kubernetes.namespaces={apps}' --set addons.prometheus.url=http://prometheus.monitoring.svc \
		--set addons.prometheus.egress.namespace=monitoring --set addons.prometheus.egress.podSelector.app=prometheus \
		--set 'addons.reportSlack.destinations[0].name=on-call' --set 'addons.reportSlack.destinations[0].target=C0123456789' \
		> "$(CHART_OUT)/takecare-prod.yaml"
	$(HELM_RUN) template takecare charts/takecare -n takecare-system -f charts/takecare/values-demo.yaml > "$(CHART_OUT)/takecare-demo.yaml"
	# The branch rendered on a cluster that runs Prometheus Operator: the
	# AlertmanagerConfig (spec 02 §1).
	$(HELM_RUN) template takecare charts/takecare -n takecare-system -f charts/takecare/values-demo.yaml \
		--api-versions monitoring.coreos.com/v1alpha1/AlertmanagerConfig > "$(CHART_OUT)/takecare-demo-prometheus-operator.yaml"
	# CRD-defined kinds (TakeCare, Gateway API, agentgateway, kagent,
	# Prometheus Operator's AlertmanagerConfig) have no schema here and are
	# skipped knowingly: their rendered instances are checked by test/chart
	# against the CRDs on an API server.
	"$(KUBECONFORM)" -strict -summary -ignore-missing-schemas \
		-kubernetes-version $(ENVTEST_K8S_VERSION) "$(CHART_OUT)"/*.yaml

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
		$(GITLEAKS_IMAGE) $(or $(GITLEAKS_MODE),dir) --config /src/.gitleaks.toml --redact --no-banner -v /src 2>&1) || { echo "$$out"; exit 1; }; \
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
docker-build: ## Build every image (operator, report server, demo app, corpus server) for the local platform.
	docker build $(DOCKER_BUILD_ARGS) --build-arg COMPONENT=operator -t $(OPERATOR_IMG) .
	docker build $(DOCKER_BUILD_ARGS) --build-arg COMPONENT=mcp-report-slack -t $(REPORT_SLACK_IMG) .
	docker build $(DOCKER_BUILD_ARGS) --build-arg COMPONENT=demo-app -t $(DEMO_APP_IMG) .
	docker build $(DOCKER_BUILD_ARGS) --build-arg COMPONENT=mcp-corpus -t $(MCP_CORPUS_IMG) .

.PHONY: docker-buildx
docker-buildx: ## Build every image for amd64 and arm64 (pushed only with PUSH=true).
	docker buildx build --platform $(IMAGE_PLATFORMS) $(DOCKER_BUILD_ARGS) --build-arg COMPONENT=operator -t $(OPERATOR_IMG) \
		$(if $(filter true,$(PUSH)),--push,) .
	docker buildx build --platform $(IMAGE_PLATFORMS) $(DOCKER_BUILD_ARGS) --build-arg COMPONENT=mcp-report-slack -t $(REPORT_SLACK_IMG) \
		$(if $(filter true,$(PUSH)),--push,) .
	docker buildx build --platform $(IMAGE_PLATFORMS) $(DOCKER_BUILD_ARGS) --build-arg COMPONENT=demo-app -t $(DEMO_APP_IMG) \
		$(if $(filter true,$(PUSH)),--push,) .
	docker buildx build --platform $(IMAGE_PLATFORMS) $(DOCKER_BUILD_ARGS) --build-arg COMPONENT=mcp-corpus -t $(MCP_CORPUS_IMG) \
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
helm: $(HELM)
kubeconform: $(KUBECONFORM)

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

# Release binaries, downloaded once and checked against the sha256 pinned in
# hack/versions.env for the host platform.
$(HELM):
	hack/fetch-tool.sh helm $(HELM_VERSION) \
		"https://get.helm.sh/helm-$(HELM_VERSION)-$(HOST_OS)-$(HOST_ARCH).tar.gz" \
		"$(HELM_SHA256_$(HOST_KEY))" "$(HOST_OS)-$(HOST_ARCH)/helm" "$@"

$(KUBECONFORM):
	hack/fetch-tool.sh kubeconform $(KUBECONFORM_VERSION) \
		"https://github.com/yannh/kubeconform/releases/download/$(KUBECONFORM_VERSION)/kubeconform-$(HOST_OS)-$(HOST_ARCH).tar.gz" \
		"$(KUBECONFORM_SHA256_$(HOST_KEY))" "kubeconform" "$@"
