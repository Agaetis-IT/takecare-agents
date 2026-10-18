# Spike 001 homemade engine (doc-engine). Images pinned by digest; the
# versions are recorded in spikes/001/versions.env.
ARG GO_IMAGE
ARG RUNTIME_IMAGE
FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/doc-engine ./cmd/doc-engine

FROM ${RUNTIME_IMAGE}
COPY --from=build /out/doc-engine /doc-engine
USER 65532:65532
ENTRYPOINT ["/doc-engine"]
