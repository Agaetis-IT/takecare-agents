# check=skip=InvalidDefaultArgInFrom
# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: LicenseRef-Agaetis-Proprietary

# Base images are pinned in hack/versions.env and passed by the Makefile
# (make docker-build, make docker-buildx); there is no default on purpose.
# COMPONENT names the binary under cmd/ (operator, mcp-report-slack): one
# Dockerfile, one image per component.
ARG GO_BUILD_IMAGE
ARG RUNTIME_IMAGE
ARG GO_LICENSES_VERSION
ARG COMPONENT=operator

# Cross-compile on the build platform instead of emulating the target one.
FROM --platform=$BUILDPLATFORM ${GO_BUILD_IMAGE} AS builder
ARG TARGETOS
ARG TARGETARCH
ARG GO_LICENSES_VERSION
ARG COMPONENT
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o component ./cmd/${COMPONENT}
# Full license texts (and NOTICE files) of every third-party module in the
# binary, shipped in the image (docs/licensing.md). go-licenses itself is built
# for the build platform; only the analysis targets the image's platform.
RUN go install github.com/google/go-licenses/v2@${GO_LICENSES_VERSION} && \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} go-licenses save ./cmd/${COMPONENT} \
    --ignore github.com/agaetis-it/takecare-agents --save_path /workspace/licenses

FROM ${RUNTIME_IMAGE}
WORKDIR /
COPY --from=builder /workspace/component /component
COPY --from=builder /workspace/licenses /licenses
USER 65532:65532
ENTRYPOINT ["/component"]
