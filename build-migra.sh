#!/bin/bash
set -euxo pipefail

docker build -t ghcr.io/stack11/trek/migra:latest - < Dockerfile.migra
id="$(docker run --rm -d ghcr.io/stack11/trek/migra:latest sleep infinity)"
function cleanup() {
    docker kill "$id" > /dev/null 2>&1 || true
}
trap cleanup EXIT
docker cp "$id":/app/out/migra internal/embed/bin/migra
cleanup
