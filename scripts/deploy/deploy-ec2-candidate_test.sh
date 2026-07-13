#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_SCRIPT="${SCRIPT_DIR}/deploy-ec2-candidate.sh"
TEST_ROOT="$(mktemp -d)"
trap 'rm -rf "$TEST_ROOT"' EXIT

BIN_DIR="${TEST_ROOT}/bin"
mkdir -p "$BIN_DIR"

cat >"${BIN_DIR}/sudo" <<'SH'
#!/usr/bin/env bash
exec "$@"
SH

cat >"${BIN_DIR}/sleep" <<'SH'
#!/usr/bin/env bash
exit 0
SH

cat >"${BIN_DIR}/docker" <<'SH'
#!/usr/bin/env bash
set -Eeuo pipefail
printf '%s\n' "$*" >>"$FAKE_DOCKER_LOG"

command_name="${1:-}"
shift || true
case "$command_name" in
  inspect)
    target="${*: -1}"
    if [[ "$target" == "itbem-events-backend" && "$FAKE_ACTIVE_EXISTS" == "true" ]]; then
      [[ "$*" == *"--format"* ]] && printf '%s\n' 'sha256:previous'
      exit 0
    fi
    exit 1
    ;;
  run)
    if [[ "$*" == *"--name itbem-events-backend "* && "${FAKE_FINAL_RUN_FAIL:-false}" == "true" ]]; then
      exit 1
    fi
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
SH

cat >"${BIN_DIR}/curl" <<'SH'
#!/usr/bin/env bash
set -Eeuo pipefail
url="${*: -1}"
case "${FAKE_HEALTH_MODE:-healthy}" in
  candidate-fails)
    [[ "$url" == *":$FAKE_CANDIDATE_PORT/health" ]] && exit 1
    ;;
  promoted-fails)
    if [[ "$url" == *":$FAKE_BACKEND_PORT/health" ]] &&
       ! grep -qx 'start itbem-events-backend' "$FAKE_DOCKER_LOG"; then
      exit 1
    fi
    ;;
esac
if [[ "${FAKE_ROLLBACK_LEGACY:-false}" == "true" &&
      "$url" == *":$FAKE_BACKEND_PORT/health" ]] &&
   grep -qx 'start itbem-events-backend' "$FAKE_DOCKER_LOG"; then
  printf '{"data":{"status":"ok","db":"ok","redis":"ok"}}\n'
  exit 0
fi
if [[ -n "${FAKE_ROLLBACK_ENVIRONMENT:-}" &&
      "$url" == *":$FAKE_BACKEND_PORT/health" ]] &&
   grep -qx 'start itbem-events-backend' "$FAKE_DOCKER_LOG"; then
  printf '{"data":{"status":"ok","db":"ok","redis":"ok","environment":"%s"}}\n' \
    "$FAKE_ROLLBACK_ENVIRONMENT"
  exit 0
fi
printf '{"data":{"status":"ok","db":"ok","redis":"ok","environment":"%s"}}\n' \
  "${FAKE_HEALTH_ENVIRONMENT:-production}"
exit 0
SH

chmod +x "${BIN_DIR}/sudo" "${BIN_DIR}/sleep" "${BIN_DIR}/docker" "${BIN_DIR}/curl"

run_deploy() {
  local case_name="$1"
  local health_mode="$2"
  local expected_status="$3"
  local case_dir="${TEST_ROOT}/${case_name}"
  local status

  mkdir -p "$case_dir"
  : >"${case_dir}/docker.log"
  printf '%s\n' 'PORT=8080' >"${case_dir}/backend.env"

  set +e
  PATH="${BIN_DIR}:$PATH" \
    FAKE_DOCKER_LOG="${case_dir}/docker.log" \
    FAKE_ACTIVE_EXISTS=true \
    FAKE_HEALTH_MODE="$health_mode" \
    FAKE_HEALTH_ENVIRONMENT="${FAKE_HEALTH_ENVIRONMENT:-production}" \
    FAKE_ROLLBACK_LEGACY="${FAKE_ROLLBACK_LEGACY:-false}" \
    FAKE_ROLLBACK_ENVIRONMENT="${FAKE_ROLLBACK_ENVIRONMENT:-}" \
    FAKE_BACKEND_PORT=8080 \
    FAKE_CANDIDATE_PORT=18080 \
    IMAGE_TAG=itbem-events-backend:test \
    BACKEND_PORT=8080 \
    CANDIDATE_PORT=18080 \
    DEPLOY_ID="$case_name" \
    ENV_FILE="${case_dir}/backend.env" \
    bash "$DEPLOY_SCRIPT" >"${case_dir}/stdout" 2>"${case_dir}/stderr"
  status=$?
  set -e

  if [[ "$status" -ne "$expected_status" ]]; then
    printf 'case %s: expected status %s, got %s\n' "$case_name" "$expected_status" "$status" >&2
    cat "${case_dir}/stderr" >&2
    return 1
  fi
}

run_deploy candidate_failure candidate-fails 1
if grep -q '^rename itbem-events-backend ' "${TEST_ROOT}/candidate_failure/docker.log"; then
  echo "candidate failure touched the active container" >&2
  exit 1
fi

FAKE_HEALTH_ENVIRONMENT=staging run_deploy wrong_environment healthy 1
if grep -q '^rename itbem-events-backend ' "${TEST_ROOT}/wrong_environment/docker.log"; then
  echo "wrong environment health response touched the active container" >&2
  exit 1
fi

run_deploy promoted_failure promoted-fails 1
grep -qx 'rename itbem-events-backend itbem-events-backend-previous-promoted_failure' \
  "${TEST_ROOT}/promoted_failure/docker.log"
grep -qx 'rename itbem-events-backend-previous-promoted_failure itbem-events-backend' \
  "${TEST_ROOT}/promoted_failure/docker.log"
grep -qx 'start itbem-events-backend' "${TEST_ROOT}/promoted_failure/docker.log"

FAKE_ROLLBACK_LEGACY=true run_deploy legacy_rollback promoted-fails 1
grep -qx 'start itbem-events-backend' "${TEST_ROOT}/legacy_rollback/docker.log"
if grep -q 'Rollback container failed its health gate' "${TEST_ROOT}/legacy_rollback/stderr"; then
  echo "legacy rollback health response was not accepted" >&2
  exit 1
fi

FAKE_ROLLBACK_ENVIRONMENT=staging run_deploy wrong_rollback_environment promoted-fails 1
grep -q 'Rollback container failed its health gate' \
  "${TEST_ROOT}/wrong_rollback_environment/stderr"

run_deploy success healthy 0
grep -qx 'rm itbem-events-backend-previous-success' "${TEST_ROOT}/success/docker.log"

echo "deploy candidate/rollback safety tests passed"
