# check=skip=InvalidDefaultArgInFrom
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0

# Base images are pinned in hack/versions.env and passed by the Makefile
# (make docker-build, make docker-buildx); there is no default on purpose.
# COMPONENT names the binary under cmd/ (operator, mcp-report-slack, demo-app,
# mcp-corpus, mcp-map, mcp-front): one Dockerfile, one image per component.
ARG GO_BUILD_IMAGE
ARG RUNTIME_IMAGE
ARG GO_LICENSES_VERSION
ARG COMPONENT=operator

# Full license texts (and NOTICE files) of every third-party module of every
# command, once per build on the build platform: each image ships the union
# (docs/licensing.md). The Go caches are BuildKit cache mounts, shared by the
# six images of one build.
FROM --platform=$BUILDPLATFORM ${GO_BUILD_IMAGE} AS licenses
ARG GO_LICENSES_VERSION
WORKDIR /workspace
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go install github.com/google/go-licenses/v2@${GO_LICENSES_VERSION} && \
    go-licenses save ./cmd/... --ignore github.com/agaetis-it/takecare-agents --save_path /workspace/licenses && \
    mkdir -p /workspace/licenses

# Cross-compile on the build platform instead of emulating the target one.
FROM --platform=$BUILDPLATFORM ${GO_BUILD_IMAGE} AS builder
ARG TARGETOS
ARG TARGETARCH
ARG COMPONENT
WORKDIR /workspace
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o component ./cmd/${COMPONENT}

FROM ${RUNTIME_IMAGE}
WORKDIR /
COPY --from=builder /workspace/component /component
COPY --from=licenses /workspace/licenses /licenses
USER 65532:65532
ENTRYPOINT ["/component"]
