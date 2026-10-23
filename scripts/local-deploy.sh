#!/usr/bin/env bash
# Local CI/CD for the company deployment.
#
# Builds linux/amd64 images from the current checkout, ships them to the
# deploy host, tags them as the compose-expected names and recreates the
# app/frontend containers. No GitHub Actions involved.
#
# Usage:
#   scripts/local-deploy.sh [version]
#
# Environment overrides:
#   DEPLOY_HOST   ssh target          (default root@47.98.141.87)
#   DEPLOY_DIR    remote compose dir  (default /opt/weknora)
#   APP_IMAGE     remote app tag      (default ghcr.io/bo961386926/weknora-app:company-v0.8.3)
#   UI_IMAGE      remote ui tag       (default ghcr.io/bo961386926/weknora-ui:company-v0.8.3)
#   PLATFORM      target platform     (default linux/amd64)
#   WITH_ANYDOC   1 to build anydoc   (default 0 for faster builds)
#   APK_MIRROR_ARG  Debian mirror     (default mirrors.aliyun.com)
#   GOPROXY_ARG     Go module proxy   (default https://goproxy.cn,direct)
#   PUBLIC_URL    health check origin (default https://kb.qh-cloud.com)
#   SKIP_BUILD=1  reuse local images tagged with <version>

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

HOST="${DEPLOY_HOST:-root@47.98.141.87}"
DIR="${DEPLOY_DIR:-/opt/weknora}"
APP_TAG="${APP_IMAGE:-ghcr.io/bo961386926/weknora-app:company-v0.8.3}"
UI_TAG="${UI_IMAGE:-ghcr.io/bo961386926/weknora-ui:company-v0.8.3}"
PLATFORM="${PLATFORM:-linux/amd64}"
WITH_ANYDOC="${WITH_ANYDOC:-0}"
APK_MIRROR_ARG="${APK_MIRROR_ARG:-mirrors.aliyun.com}"
GOPROXY_ARG="${GOPROXY_ARG:-https://goproxy.cn,direct}"
PUBLIC_URL="${PUBLIC_URL:-https://kb.qh-cloud.com}"
VERSION="${1:-$(git rev-parse --short HEAD)}"

echo "[local-deploy] version=${VERSION} platform=${PLATFORM} host=${HOST}"

if [[ "${SKIP_BUILD:-0}" != "1" ]]; then
  echo "[local-deploy] building app image"
  docker buildx build --platform "$PLATFORM" --load \
    -f docker/Dockerfile.app \
    --build-arg "WITH_ANYDOC=${WITH_ANYDOC}" \
    --build-arg "APK_MIRROR_ARG=${APK_MIRROR_ARG}" \
    --build-arg "GOPROXY_ARG=${GOPROXY_ARG}" \
    -t "weknora-app:${VERSION}" .

  echo "[local-deploy] building frontend image"
  docker buildx build --platform "$PLATFORM" --load \
    -f frontend/Dockerfile -t "weknora-ui:${VERSION}" frontend/
fi

staging="$(mktemp -d)"
trap 'rm -rf "$staging"' EXIT

for pair in "weknora-app:${VERSION} ${APP_TAG}" "weknora-ui:${VERSION} ${UI_TAG}"; do
  src="${pair%% *}"
  dst="${pair##* }"
  archive="${staging}/$(echo "$src" | tr '/:' '__').tar.gz"
  echo "[local-deploy] shipping ${src} -> ${dst}"
  docker save "$src" | gzip -1 > "$archive"
  scp -C "$archive" "$HOST:/tmp/"
  remote="/tmp/$(basename "$archive")"
  ssh "$HOST" "docker load -i ${remote} && docker tag ${src} ${dst} && rm -f ${remote}"
done

echo "[local-deploy] recreating app and frontend"
ssh "$HOST" "cd ${DIR} && docker compose up -d --no-build app frontend"

echo "[local-deploy] waiting for app health"
status=""
for _ in $(seq 1 30); do
  status="$(ssh "$HOST" "docker inspect -f '{{.State.Health.Status}}' WeKnora-app 2>/dev/null" || true)"
  [[ "$status" == "healthy" ]] && break
  sleep 5
done
if [[ "$status" != "healthy" ]]; then
  echo "[local-deploy] app did not become healthy (last status: ${status:-unknown})" >&2
  exit 1
fi

echo "[local-deploy] container healthy; checking ${PUBLIC_URL}/health"
curl -fsS "${PUBLIC_URL}/health" >/dev/null
echo "[local-deploy] done"
