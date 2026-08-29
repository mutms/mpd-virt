#!/usr/bin/env bash
# Build and publish the arm64 mpd base image for Apple containers.
#
# NOTE: it is necessary to `container registry login ghcr.io` first

set -euo pipefail
# Run from container/ whatever the caller's cwd: the build context "."
# and the Containerfile path below are relative to this directory.
cd "$(dirname "$0")"

IMAGE="${IMAGE:-ghcr.io/mutms/mpd-virt-container-apple}"
TAG="13.6.2"

if [ -n "$(git status --porcelain)" ]; then
    echo "error: working tree is not clean — commit or stash first" >&2
    exit 1
fi

echo "publishing $IMAGE:$TAG"

# --pull --no-cache: a release is always built on the freshly pulled base
# with current packages, never from stale local layers.
container build --pull --no-cache --build-arg VERSION="$TAG" -t "$IMAGE:$TAG" -f Containerfile .
container image push "$IMAGE:$TAG"

echo "published $IMAGE:$TAG"
