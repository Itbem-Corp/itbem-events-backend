#!/usr/bin/env bash
set -Eeuo pipefail

: "${IMAGE_TAG:?IMAGE_TAG is required}"
: "${BACKEND_PORT:?BACKEND_PORT is required}"
: "${CANDIDATE_PORT:?CANDIDATE_PORT is required}"
: "${DEPLOY_ID:?DEPLOY_ID is required}"
: "${ENV_FILE:?ENV_FILE is required}"

[[ "$BACKEND_PORT" =~ ^[0-9]+$ ]] || { echo "BACKEND_PORT must be numeric" >&2; exit 2; }
[[ "$CANDIDATE_PORT" =~ ^[0-9]+$ ]] || { echo "CANDIDATE_PORT must be numeric" >&2; exit 2; }
[[ "$BACKEND_PORT" != "$CANDIDATE_PORT" ]] || { echo "candidate port must differ from backend port" >&2; exit 2; }
[[ "$DEPLOY_ID" =~ ^[A-Za-z0-9._-]+$ ]] || { echo "DEPLOY_ID contains unsupported characters" >&2; exit 2; }

APP_NAME="itbem-events-backend"
CANDIDATE_NAME="${APP_NAME}-candidate"
PREVIOUS_NAME="${APP_NAME}-previous-${DEPLOY_ID}"
ROLLBACK_TAG="${APP_NAME}:rollback"

d() {
  sudo docker "$@"
}

cleanup() {
  d rm -f "$CANDIDATE_NAME" >/dev/null 2>&1 || true
  rm -f "$ENV_FILE"
}
trap cleanup EXIT

wait_for_health() {
  local port="$1"
  local attempts="${2:-90}"
  local environment_mode="${3:-strict}"
  local url="http://127.0.0.1:${port}/health"
  [[ "$environment_mode" == "strict" || "$environment_mode" == "legacy-rollback" ]] || {
    echo "unsupported health environment mode" >&2
    return 2
  }
  for ((attempt = 1; attempt <= attempts; attempt++)); do
    if curl --fail --silent --show-error --max-time 5 "$url" | python3 -c '
import json
import sys

try:
    payload = json.load(sys.stdin)
    data = payload.get("data") if isinstance(payload, dict) else None
    dependencies = {
        "status": "ok",
        "db": "ok",
        "redis": "ok",
    }
    valid = isinstance(data, dict) and all(data.get(key) == value for key, value in dependencies.items())
    if valid and sys.argv[1] == "strict":
        valid = data.get("environment") == "production"
    elif valid:
        valid = "environment" not in data or data.get("environment") == "production"
except (AttributeError, json.JSONDecodeError, TypeError):
    valid = False
raise SystemExit(0 if valid else 1)
' "$environment_mode"; then
      return 0
    fi
    sleep 2
  done
  return 1
}

rollback_previous() {
  if [[ "${HAD_PREVIOUS:-false}" != "true" ]]; then
    echo "No previous container is available for rollback" >&2
    return 1
  fi
  d rm -f "$APP_NAME" >/dev/null 2>&1 || true
  d rename "$PREVIOUS_NAME" "$APP_NAME"
  d start "$APP_NAME" >/dev/null
  # The one-time previous image may predate data.environment. Accept only an
  # absent marker during rollback; any explicit non-production value fails.
  if ! wait_for_health "$BACKEND_PORT" 45 legacy-rollback; then
    echo "Rollback container failed its health gate" >&2
    d logs --tail 200 "$APP_NAME" >&2 || true
    return 1
  fi
  echo "Previous backend restored successfully"
}

# Validate the exact image and production dependency set without touching the
# currently serving container. Candidate traffic is bound to loopback only.
d rm -f "$CANDIDATE_NAME" >/dev/null 2>&1 || true
d run -d \
  --name "$CANDIDATE_NAME" \
  --restart no \
  --init \
  --env-file "$ENV_FILE" \
  --log-opt max-size=20m \
  --log-opt max-file=5 \
  -p "127.0.0.1:${CANDIDATE_PORT}:${BACKEND_PORT}" \
  "$IMAGE_TAG" >/dev/null

if ! wait_for_health "$CANDIDATE_PORT"; then
  echo "Candidate failed its health gate; active backend was not touched" >&2
  d logs --tail 200 "$CANDIDATE_NAME" >&2 || true
  exit 1
fi
d rm -f "$CANDIDATE_NAME" >/dev/null

HAD_PREVIOUS=false
if d inspect "$APP_NAME" >/dev/null 2>&1; then
  HAD_PREVIOUS=true
  old_image_id="$(d inspect --format '{{.Image}}' "$APP_NAME")"
  d tag "$old_image_id" "$ROLLBACK_TAG"
  d rename "$APP_NAME" "$PREVIOUS_NAME"
  d stop --time 20 "$PREVIOUS_NAME" >/dev/null
fi

if ! d run -d \
  --name "$APP_NAME" \
  --restart always \
  --init \
  --env-file "$ENV_FILE" \
  --log-opt max-size=20m \
  --log-opt max-file=5 \
  -p "${BACKEND_PORT}:${BACKEND_PORT}" \
  "$IMAGE_TAG" >/dev/null; then
  echo "Promoted container could not start; rolling back" >&2
  rollback_previous
  exit 1
fi

if ! wait_for_health "$BACKEND_PORT" 60; then
  echo "Promoted container failed its health gate; rolling back" >&2
  d logs --tail 200 "$APP_NAME" >&2 || true
  rollback_previous
  exit 1
fi

# A candidate can be healthy while a later host/container restart exposes stale
# runtime state. Assert the serving container is exactly the promoted image and
# env, then restart that same container once and re-run the dependency gate.
expected_db_host="$(sed -n 's/^DB_HOST=//p' "$ENV_FILE" | tail -n 1)"
[[ -n "$expected_db_host" ]] || { echo 'Promoted runtime is missing DB_HOST' >&2; exit 1; }
[[ "$(d inspect --format '{{.Config.Image}}' "$APP_NAME")" == "$IMAGE_TAG" ]] || {
  echo 'Serving container image does not match the promoted immutable image' >&2
  exit 1
}
d inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$APP_NAME" | \
  grep -Fqx "DB_HOST=$expected_db_host" || {
  echo 'Serving container DB_HOST does not match the promoted runtime configuration' >&2
  exit 1
}
d restart "$APP_NAME" >/dev/null
if ! wait_for_health "$BACKEND_PORT" 45; then
  echo 'Serving container failed health after restart-resilience verification' >&2
  d logs --tail 200 "$APP_NAME" >&2 || true
  exit 1
fi

if [[ "$HAD_PREVIOUS" == "true" ]]; then
  d rm "$PREVIOUS_NAME" >/dev/null
fi

echo "Backend deployment promoted successfully: ${IMAGE_TAG}"
