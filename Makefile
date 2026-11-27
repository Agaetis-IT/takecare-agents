# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0

# Every pinned version lives in hack/versions.env (spec 04).
include hack/versions.env

MODULE := github.com/agaetis-it/takecare-agents
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# The images follow takecare-agents-<component> (issue #22).
OPERATOR_IMG ?= $(IMAGE_REGISTRY)/takecare-agents-operator:$(VERSION)
REPORT_SLACK_IMG ?= $(IMAGE_REGISTRY)/takecare-agents-mcp-report-slack:$(VERSION)
# The end-to-end tests' fakes (spec 04): built for the e2e, never released.
FAKE_LLM_IMG ?= $(IMAGE_REGISTRY)/takecare-agents-fake-llm:$(VERSION)
FAKE_SLACK_IMG ?= $(IMAGE_REGISTRY)/takecare-agents-fake-slack:$(VERSION)
# The demo's images (spec 03): the application and the corpus MCP server.
DEMO_APP_IMG ?= $(IMAGE_REGISTRY)/takecare-agents-demo-app:$(VERSION)
MCP_CORPUS_IMG ?= $(IMAGE_REGISTRY)/takecare-agents-mcp-corpus:$(VERSION)
# The Map server (spec 08).
MCP_MAP_IMG ?= $(IMAGE_REGISTRY)/takecare-agents-mcp-map:$(VERSION)
# The mTLS front of a workload cluster (ADR 0016).
MCP_FRONT_IMG ?= $(IMAGE_REGISTRY)/takecare-agents-mcp-front:$(VERSION)
# The history server (spec 11).
MCP_HISTORY_IMG ?= $(IMAGE_REGISTRY)/takecare-agents-mcp-history:$(VERSION)
# The code server (spec 12).
MCP_CODE_IMG ?= $(IMAGE_REGISTRY)/takecare-agents-mcp-code:$(VERSION)
# docker-buildx pushes only when PUSH=true (the release workflow, from PR 6).
PUSH ?= false

LOCALBIN ?= $(CURDIR)/bin
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen-$(CONTROLLER_TOOLS_VERSION)
GOLANGCI_LINT ?= $(LOCALBIN)/golangci-lint-$(GOLANGCI_LINT_VERSION)
SETUP_ENVTEST ?= $(LOCALBIN)/setup-envtest-$(SETUP_ENVTEST_VERSION)
GO_LICENSES ?= $(LOCALBIN)/go-licenses-$(GO_LICENSES_VERSION)
HELM ?= $(LOCALBIN)/helm-$(HELM_VERSION)
KUBECONFORM ?= $(LOCALBIN)/kubeconform-$(KUBECONFORM_VERSION)
SYFT ?= $(LOCALBIN)/syft-$(SYFT_VERSION)
# Host platform, for the binaries downloaded from GitHub releases.
HOST_OS := $(shell go env GOOS)
HOST_ARCH := $(shell go env GOARCH)
HOST_KEY := $(shell echo $(HOST_OS)_$(HOST_ARCH) | tr a-z A-Z)
CHART_OUT ?= $(CURDIR)/.chart-out
# The workload-cluster chart refuses to render without its REQUIRED values.
CLUSTER_CHART_SETS = --set cluster.name=example --set 'cluster.namespaces={apps}' \
	--set tls.secretName=front-tls --set tls.clientCASecretName=front-client-ca \
	--set 'tls.clientNames={admin.example.internal}'
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

# Each package's test binary gets TEST_TIMEOUT. Go's default of 10 minutes
# is too close: on CI (2026-11-19), internal/controller/intendant ran in
# 502 s. A package that dies on the timeout hides the other packages'
# results, a stale golden included.
TEST_TIMEOUT ?= 20m

.PHONY: test
test: setup-envtest chart-deps ## Run unit, golden, envtest and chart tests.
	hack/power-window-test.sh
	hack/demo-images-test.sh
	hack/ci-changes-test.sh
	hack/changelog-assemble-test.sh
	@# setup-envtest installs its binaries read-only: writable, a worktree's bin/ can be removed.
	"$(SETUP_ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(abspath $(LOCALBIN))" >/dev/null && chmod -R u+w "$(LOCALBIN)/k8s"
	@# The path is captured on its own, so that a failed setup-envtest stops
	@# here, and absolute: go test runs each package in its own directory.
	assets="$$("$(SETUP_ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(abspath $(LOCALBIN))" -p path)" \
		|| { echo "setup-envtest gave no envtest path" >&2; exit 1; }; \
	case "$$assets" in /*) ;; *) echo "envtest path is not absolute: $$assets" >&2; exit 1 ;; esac; \
	KUBEBUILDER_ASSETS="$$assets" HELM="$(HELM)" $(HELM_ENV) go test ./... -coverprofile cover.out -timeout $(TEST_TIMEOUT)

##@ Charts

.PHONY: chart-sync
chart-sync: generate ## Copy the CRDs into takecare-crds and the pinned versions into the charts.
	hack/chart-sync.sh

.PHONY: chart-check
chart-check: ## Fail if the charts drifted from config/crd or hack/versions.env.
	hack/chart-sync.sh --check

.PHONY: chart-deps

# Nothing is downloaded when the archives in charts/*/charts already match
# Chart.lock (`dependency list` says ok): the CI caches them, keyed by the
# pins. Otherwise the build is retried, three attempts 20 s apart: third
# parties' registries fail by intermittence. Classic (https) repositories must
# be registered before `dependency build` resolves them; OCI dependencies need
# nothing. Registered in ./bin only.
chart-deps: helm ## Download the charts' dependencies (Chart.lock is committed), only when missing.
	@set -e; for c in charts/takecare-crds charts/takecare; do \
	  if $(HELM_RUN) dependency list $$c | awk 'NR > 1 && NF { if ($$NF != "ok") bad = 1 } END { exit bad }'; then \
	    echo "$$c: dependencies present, nothing to download"; continue; fi; \
	  for i in 1 2 3; do \
	    if $(HELM_RUN) repo add --force-update prometheus-community https://prometheus-community.github.io/helm-charts >/dev/null && \
	      $(HELM_RUN) repo add --force-update open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts >/dev/null && \
	      $(HELM_RUN) dependency build $$c; then break; fi; \
	    if [ $$i = 3 ]; then echo "$$c: dependency build failed 3 times" >&2; exit 1; fi; \
	    echo "$$c: dependency build failed (attempt $$i of 3), retrying in 20 s" >&2; sleep 20; \
	  done; \
	done

.PHONY: chart-docs
chart-docs: ## Generate the charts' README.md from README.md.gotmpl and the values' comments (helm-docs).
	$(DOCKER_RUN) $(HELM_DOCS_IMAGE) --chart-search-root charts --document-dependency-values=false \
		--template-files README.md.gotmpl --skip-version-footer

.PHONY: chart-docs-check
chart-docs-check: chart-docs ## Fail if a chart's README.md drifted from its values.
	@if [ -n "$$(git status --porcelain charts/*/README.md)" ]; then \
		git --no-pager diff charts/*/README.md; echo "run make chart-docs and commit the README"; exit 1; fi

.PHONY: chart-versions-check
chart-versions-check: ## Fail unless the three charts carry one version, as appVersion too.
	@n=$$(sed -nE 's/^(version|appVersion): *//p' charts/takecare-crds/Chart.yaml charts/takecare/Chart.yaml charts/takecare-cluster/Chart.yaml | sort -u | wc -l | tr -d ' '); \
	test "$$n" = 1 || { echo "the three charts' version and appVersion differ: one version for all (docs/release.md step 2)"; exit 1; }

.PHONY: chart-lint
chart-lint: chart-check chart-docs-check chart-versions-check chart-deps ## Drift checks, then helm lint, the three charts, both profiles.
	$(HELM_RUN) lint charts/takecare-crds
	$(HELM_RUN) lint charts/takecare-crds --set demo.enabled=true
	$(HELM_RUN) lint charts/takecare -f charts/takecare/values-prod.yaml
	$(HELM_RUN) lint charts/takecare -f charts/takecare/values-demo.yaml
	$(HELM_RUN) lint charts/takecare-cluster $(CLUSTER_CHART_SETS)

.PHONY: chart-smoke
chart-smoke: helm ## Install and upgrade both charts for real on a k3d cluster (hack/chart-smoke.sh).
	hack/chart-smoke.sh all

.PHONY: chart-template
chart-template: chart-deps kubeconform ## Render both profiles and the workload-cluster chart, and validate them with kubeconform.
	rm -rf "$(CHART_OUT)" && mkdir -p "$(CHART_OUT)"
	$(HELM_RUN) template takecare-crds charts/takecare-crds -n takecare-system --set demo.enabled=true > "$(CHART_OUT)/takecare-crds.yaml"
	# values-prod.yaml refuses to render until its REQUIRED values are set.
	$(HELM_RUN) template takecare charts/takecare -n takecare-system -f charts/takecare/values-prod.yaml \
		--set platform.models[0].baseURL=https://vllm.example.internal/v1 --set platform.models[0].model=example \
		--set platform.observability.metrics.prometheusURL=http://prometheus.monitoring.svc \
		--set kagent.database.postgres.urlFile=/etc/kagent-db/url \
		--set 'addons.kubernetes.namespaces={apps}' --set addons.prometheus.url=http://prometheus.monitoring.svc \
		--set addons.prometheus.egress.namespace=monitoring --set addons.prometheus.egress.podSelector.app=prometheus \
		--set 'addons.reportSlack.destinations[0].name=on-call' --set 'addons.reportSlack.destinations[0].target=C0123456789' \
		--set addons.map.git.url=https://git.example.internal/ops/map.git --set addons.map.git.credentialSecret.name=map-token \
		> "$(CHART_OUT)/takecare-prod.yaml"
	$(HELM_RUN) template takecare charts/takecare -n takecare-system -f charts/takecare/values-demo.yaml > "$(CHART_OUT)/takecare-demo.yaml"
	$(HELM_RUN) template takecare-cluster charts/takecare-cluster -n takecare-cluster $(CLUSTER_CHART_SETS) > "$(CHART_OUT)/takecare-cluster.yaml"
	# The values takecare init writes from the demo's answers (spec 13 §10);
	# kagent's database is not one of its questions.
	$(HELM_RUN) template takecare charts/takecare -n takecare-system -f internal/cli/testdata/init-demo/takecare/values.yaml \
		--set kagent.database.postgres.urlFile=/etc/kagent-db/url > "$(CHART_OUT)/takecare-init-demo.yaml"
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

# The release's charts: chart version = app version (spec 04); Chart.yaml
# keeps its development version. The packages are linted as published.
CHART_PKG ?= bin/charts
.PHONY: chart-package
chart-package: chart-deps ## Package the three charts at RELEASE_VERSION into CHART_PKG and lint the packages.
	@test -n "$(RELEASE_VERSION)" || { echo "RELEASE_VERSION is required (the tag without its v)"; exit 1; }
	rm -rf "$(CHART_PKG)" && mkdir -p "$(CHART_PKG)"
	$(HELM_RUN) package charts/takecare-crds charts/takecare charts/takecare-cluster --version "$(RELEASE_VERSION)" --app-version "$(RELEASE_VERSION)" -d "$(CHART_PKG)"
	$(HELM_RUN) lint "$(CHART_PKG)/takecare-crds-$(RELEASE_VERSION).tgz" --set demo.enabled=true
	$(HELM_RUN) lint "$(CHART_PKG)/takecare-$(RELEASE_VERSION).tgz" -f charts/takecare/values-prod.yaml
	$(HELM_RUN) lint "$(CHART_PKG)/takecare-$(RELEASE_VERSION).tgz" -f charts/takecare/values-demo.yaml
	$(HELM_RUN) lint "$(CHART_PKG)/takecare-cluster-$(RELEASE_VERSION).tgz" $(CLUSTER_CHART_SETS)

.PHONY: power-window-test
power-window-test: ## Check hack/power-window.sh (did the machine sleep during a run) against its fixture.
	hack/power-window-test.sh

.PHONY: demo-images-test
demo-images-test: ## Check that make demo-up stops when building or importing its images fails (stubs, no cluster).
	hack/demo-images-test.sh

.PHONY: ci-changes-test
ci-changes-test: ## Check which pull requests skip CI's heavy jobs (hack/ci-changes.sh).
	hack/ci-changes-test.sh

.PHONY: changelog-assemble-test
changelog-assemble-test: ## Check hack/changelog-assemble.sh and hack/changelog-check.sh against their fixture.
	hack/changelog-assemble-test.sh

.PHONY: release-notes-test
release-notes-test: ## Check hack/release-notes.sh against its fixture.
	hack/release-notes-test.sh

.PHONY: sbom-test
sbom-test: ## Check that hack/sbom.sh refuses an empty, Go-less or wrong-architecture SBOM.
	hack/sbom-test.sh

.PHONY: takecare-dist
takecare-dist: syft go-licenses ## Build the takecare command's release archives, SBOMs and checksums at RELEASE_VERSION into bin/dist.
	@test -n "$(RELEASE_VERSION)" || { echo "RELEASE_VERSION is required (the tag without its v)"; exit 1; }
	SYFT="$(SYFT)" GO_LICENSES="$(GO_LICENSES)" hack/takecare-dist.sh "$(RELEASE_VERSION)"

.PHONY: site-deploy-test
site-deploy-test: ## Check which runs publish the site (hack/site-deploy.sh): a final tag on main, or by hand from main or such a tag.
	hack/site-deploy-test.sh

##@ Lint

.PHONY: lint
lint: lint-go lint-sh lint-md lint-spdx scenarios-check changelog-check ## Run every linter.

.PHONY: lint-go
lint-go: golangci-lint ## Run golangci-lint.
	"$(GOLANGCI_LINT)" run

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

.PHONY: scenarios-check
scenarios-check: ## Check the scenario files of test/scenarios: schema, Map keys, corpus records, regular expressions (spec 14).
	go run ./test/scenarios/player check test/scenarios

.PHONY: changelog-check
changelog-check: ## Check the changelog fragments, and that a change of api/, internal/, cmd/, charts/, config/ or demo/ adds one (changes/unreleased/README.md).
	hack/changelog-check.sh

.PHONY: dco
dco: ## Check the DCO sign-off of every commit of DCO_BASE..DCO_HEAD (default origin/main..HEAD; merges excepted).
	hack/check-dco.sh $(or $(DCO_BASE),origin/main) $(or $(DCO_HEAD),HEAD)

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

SITE_VENV := $(LOCALBIN)/site-venv-pyyaml-$(PYYAML_VERSION)

$(SITE_VENV):
	python3 -m venv "$@" && "$@/bin/pip" install --quiet --disable-pip-version-check "pyyaml==$(PYYAML_VERSION)"

.PHONY: site
site: $(SITE_VENV) ## Build the product site into site/dist (French and English; the Reference from config/crd and the charts).
	"$(SITE_VENV)/bin/python" site/src/build.py --out site/dist $(if $(SITE_URL),--site-url $(SITE_URL))

.PHONY: build
build: ## Build the operator binary.
	go build -trimpath -o bin/operator ./cmd/operator

.PHONY: run
run: ## Run the operator against the current kube context.
	go run ./cmd/operator --metrics-secure=false --metrics-bind-address=:8080

DOCKER_BUILD_ARGS := --build-arg GO_BUILD_IMAGE=$(GO_BUILD_IMAGE) --build-arg RUNTIME_IMAGE=$(RUNTIME_IMAGE) \
	--build-arg GO_LICENSES_VERSION=$(GO_LICENSES_VERSION)
# LOCAL_LABELS marks the images the demo, the e2e and the smoke build on a
# machine (hack/demo.sh, hack/e2e.sh, hack/chart-smoke.sh), so that
# make clean-local finds them and nothing else; empty for a release build.
LOCAL_LABELS ?=

# DOCKER_BUILD COMPONENT: a plain docker build, or, once the Actions cache's
# token is exposed (the e2e and chart-smoke workflows do it, with a buildx
# builder), a buildx build loaded into Docker that shares the image job's
# layers through the Actions cache, one scope per image (BUILDX_CACHE).
DOCKER_BUILD = $(if $(ACTIONS_RUNTIME_TOKEN),docker buildx build --load $(call BUILDX_CACHE,$(1)),docker build)

.PHONY: docker-build
docker-build: ## Build every image (operator, report server, demo app, corpus server, Map server, front, history server, code server) for the local platform.
	$(call DOCKER_BUILD,operator) $(DOCKER_BUILD_ARGS) $(LOCAL_LABELS) --build-arg COMPONENT=operator -t $(OPERATOR_IMG) .
	$(call DOCKER_BUILD,mcp-report-slack) $(DOCKER_BUILD_ARGS) $(LOCAL_LABELS) --build-arg COMPONENT=mcp-report-slack -t $(REPORT_SLACK_IMG) .
	$(call DOCKER_BUILD,demo-app) $(DOCKER_BUILD_ARGS) $(LOCAL_LABELS) --build-arg COMPONENT=demo-app -t $(DEMO_APP_IMG) .
	$(call DOCKER_BUILD,mcp-corpus) $(DOCKER_BUILD_ARGS) $(LOCAL_LABELS) --build-arg COMPONENT=mcp-corpus -t $(MCP_CORPUS_IMG) .
	$(call DOCKER_BUILD,mcp-map) $(DOCKER_BUILD_ARGS) $(LOCAL_LABELS) --build-arg COMPONENT=mcp-map -t $(MCP_MAP_IMG) .
	$(call DOCKER_BUILD,mcp-front) $(DOCKER_BUILD_ARGS) $(LOCAL_LABELS) --build-arg COMPONENT=mcp-front -t $(MCP_FRONT_IMG) .
	$(call DOCKER_BUILD,mcp-history) $(DOCKER_BUILD_ARGS) $(LOCAL_LABELS) --build-arg COMPONENT=mcp-history -t $(MCP_HISTORY_IMG) .
	$(call DOCKER_BUILD,mcp-code) $(DOCKER_BUILD_ARGS) $(LOCAL_LABELS) --build-arg COMPONENT=mcp-code -t $(MCP_CODE_IMG) .

.PHONY: clean-local
clean-local: ## List the images the demo, the e2e and the smoke built here; CLEAN_LOCAL=remove removes them (never the current tree's).
	hack/clean-local.sh

.PHONY: docker-build-e2e
docker-build-e2e: ## Build the end-to-end tests' fakes (model provider, Slack) for the local platform.
	$(call DOCKER_BUILD,fake-llm) $(DOCKER_BUILD_ARGS) $(LOCAL_LABELS) --build-arg COMPONENT=fake-llm -t $(FAKE_LLM_IMG) .
	$(call DOCKER_BUILD,fake-slack) $(DOCKER_BUILD_ARGS) $(LOCAL_LABELS) --build-arg COMPONENT=fake-slack -t $(FAKE_SLACK_IMG) .

# In GitHub Actions, once the runtime token is exposed (the ci workflow does
# it), the layers go to the Actions cache, one scope per image.
comma := ,
BUILDX_CACHE = $(if $(ACTIONS_RUNTIME_TOKEN),--cache-from type=gha$(comma)scope=$(1) --cache-to type=gha$(comma)mode=max$(comma)scope=$(1))

.PHONY: docker-buildx
docker-buildx: ## Build every image for IMAGE_PLATFORMS (amd64 and arm64; pushed only with PUSH=true).
	docker buildx build --platform $(IMAGE_PLATFORMS) $(DOCKER_BUILD_ARGS) --build-arg COMPONENT=operator $(call BUILDX_CACHE,operator) -t $(OPERATOR_IMG) \
		$(if $(filter true,$(PUSH)),--push,) .
	docker buildx build --platform $(IMAGE_PLATFORMS) $(DOCKER_BUILD_ARGS) --build-arg COMPONENT=mcp-report-slack $(call BUILDX_CACHE,mcp-report-slack) -t $(REPORT_SLACK_IMG) \
		$(if $(filter true,$(PUSH)),--push,) .
	docker buildx build --platform $(IMAGE_PLATFORMS) $(DOCKER_BUILD_ARGS) --build-arg COMPONENT=demo-app $(call BUILDX_CACHE,demo-app) -t $(DEMO_APP_IMG) \
		$(if $(filter true,$(PUSH)),--push,) .
	docker buildx build --platform $(IMAGE_PLATFORMS) $(DOCKER_BUILD_ARGS) --build-arg COMPONENT=mcp-corpus $(call BUILDX_CACHE,mcp-corpus) -t $(MCP_CORPUS_IMG) \
		$(if $(filter true,$(PUSH)),--push,) .
	docker buildx build --platform $(IMAGE_PLATFORMS) $(DOCKER_BUILD_ARGS) --build-arg COMPONENT=mcp-map $(call BUILDX_CACHE,mcp-map) -t $(MCP_MAP_IMG) \
		$(if $(filter true,$(PUSH)),--push,) .
	docker buildx build --platform $(IMAGE_PLATFORMS) $(DOCKER_BUILD_ARGS) --build-arg COMPONENT=mcp-front $(call BUILDX_CACHE,mcp-front) -t $(MCP_FRONT_IMG) \
		$(if $(filter true,$(PUSH)),--push,) .
	docker buildx build --platform $(IMAGE_PLATFORMS) $(DOCKER_BUILD_ARGS) --build-arg COMPONENT=mcp-history $(call BUILDX_CACHE,mcp-history) -t $(MCP_HISTORY_IMG) \
		$(if $(filter true,$(PUSH)),--push,) .
	docker buildx build --platform $(IMAGE_PLATFORMS) $(DOCKER_BUILD_ARGS) --build-arg COMPONENT=mcp-code $(call BUILDX_CACHE,mcp-code) -t $(MCP_CODE_IMG) \
		$(if $(filter true,$(PUSH)),--push,) .


##@ End-to-end (spec 04; never changes your kube context)

.PHONY: guard-corpus
guard-corpus: ## The guard corpus through the pinned gateway (ADR 0020; needs Docker): real outputs unchanged, samples masked whole.
	go test -count=1 -v -tags guardcorpus -run 'TestGuardCorpus' ./internal/controller/ranger/

.PHONY: e2e e2e-down fakellm-record
e2e: ## The demo on k3d with the fake model and fake Slack, and the complete Doc path checked.
	hack/e2e.sh run
e2e-down: ## Delete the end-to-end cluster.
	hack/e2e.sh down
fakellm-record: ## Replace the fake model's fixtures with a real model's conversation (FAKELLM_UPSTREAM_KEY).
	hack/e2e.sh record

##@ Demo (spec 03; never changes your kube context)

.PHONY: demo-up demo-incident demo-verdict demo-bench demo-bench-intendant demo-bench-kine demo-screens demo-screens-stop demo-reset demo-down
demo-up: ## Create the k3d demo and install everything (TAKECARE_MODEL_API_KEY; DEMO_IMAGES=build until the first release).
	hack/demo.sh up
demo-incident: ## Start the proxy-timeout incident and print the expected alert window.
	hack/demo.sh incident
demo-verdict: ## Wait for the Doc's diagnosis of the incident and its usage (read one scrape wait after the end), print them with the scenario's verdict lines, then the Kiné's review.
	hack/demo.sh verdict
N ?= 5
demo-bench: ## Accuracy bench: N silent investigations of the ongoing incident, scored against test/scenarios/<scenario>.yaml (the Doc's cooldown is 1 s meanwhile; BENCH_MIN=k fails under k; BENCH_BASELINE=report compares).
	hack/bench.sh $(N)
demo-bench-intendant: ## Intendant bench: N surveys of the demo against the bench's Map, scored against test/bench/intendant/expected.yaml (BENCH_MIN=k fails under k).
	hack/bench-intendant.sh $(N)
demo-bench-kine: ## Kiné bench: N silent reviews at Practitioner of a fixed investigation, scored against test/bench/kine/expected.yaml (BENCH_MIN=k fails under k; BENCH_CHAIN=1 reviews live Doc runs, not counted; BENCH_MAX_COST=usd stops before the cap).
	hack/bench-kine.sh $(N)
demo-screens: ## Port-forward Prometheus, Alertmanager and Jaeger, and print the URLs.
	hack/demo.sh screens
demo-screens-stop: ## Stop the port-forwards demo-screens started.
	hack/demo.sh screens-stop
demo-reset: ## Back to healthy, the incident's investigations deleted.
	hack/demo.sh reset
demo-down: ## Delete the demo cluster and its working files.
	hack/demo.sh down

##@ Tools

.PHONY: controller-gen golangci-lint setup-envtest go-licenses
controller-gen: $(CONTROLLER_GEN)
golangci-lint: $(GOLANGCI_LINT)
setup-envtest: $(SETUP_ENVTEST)
go-licenses: $(GO_LICENSES)
helm: $(HELM)
kubeconform: $(KUBECONFORM)
syft: $(SYFT)

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

$(SYFT):
	hack/fetch-tool.sh syft $(SYFT_VERSION) \
		"https://github.com/anchore/syft/releases/download/$(SYFT_VERSION)/syft_$(SYFT_VERSION:v%=%)_$(HOST_OS)_$(HOST_ARCH).tar.gz" \
		"$(SYFT_SHA256_$(HOST_KEY))" "syft" "$@"
