#!/usr/bin/env bash
# Builds the homemade engine (engine/), imports it into k3d and deploys it
# instances given as <name>:<identity>:<tools json>.
# Usage: scripts/engine.sh [doc-engine:doc-service:manifests/engine/tools-doc-service.json ...]
source "$(dirname "$0")/lib.sh"
docker build -q --build-arg GO_IMAGE="$GO_BUILD_IMAGE" --build-arg RUNTIME_IMAGE="$ENGINE_RUNTIME_IMAGE" \
  -t "$DOC_ENGINE_IMAGE" "${SPIKE_DIR}/engine" >/dev/null
k3d image import "$DOC_ENGINE_IMAGE" -c "$CLUSTER_NAME" >/dev/null
kubectl apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Namespace
metadata: { name: takecare-system }
YAML
(($#)) || set -- doc-engine:doc-service:manifests/engine/tools-doc-service.json
for spec in "$@"; do
  IFS=: read -r ENGINE_NAME IDENTITY tools <<<"$spec"
  export ENGINE_NAME IDENTITY ENGINE_CONFIG_SHA
  kubectl -n takecare-system create configmap "$ENGINE_NAME" \
    --from-file=tools.json="${SPIKE_DIR}/${tools}" \
    --from-file=instructions.md="${SPIKE_DIR}/manifests/doc/instructions.md" \
    --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  ENGINE_CONFIG_SHA="$(cat "${SPIKE_DIR}/${tools}" "${SPIKE_DIR}/manifests/doc/instructions.md" \
    | shasum -a 256 | cut -c1-16)-$(docker inspect -f '{{.Id}}' "$DOC_ENGINE_IMAGE" | cut -c8-19)"
  render "${SPIKE_DIR}/manifests/engine/doc-engine.yaml" ENGINE_NAME IDENTITY DOC_ENGINE_IMAGE MODEL ENGINE_CONFIG_SHA | kubectl apply -f - >/dev/null
  kubectl -n takecare-system rollout status "deploy/${ENGINE_NAME}" --timeout=120s
done
