#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
compose_file="$repository_root/deploy/staging/aws-emulator.compose.yml"
emulator_port=${ITBEM_AWS_EMULATOR_PORT:-14566}
container_name="itbem-agent-qualification-$$"
container_may_exist=0
report_file=''
emulator_image=$(awk '$1 == "image:" { print $2; exit }' "$compose_file" | tr -d '\r')

case "$emulator_port" in
  ''|*[!0-9]*)
    echo "ITBEM_AWS_EMULATOR_PORT must be a numeric loopback port" >&2
    exit 1
    ;;
esac
if [ "$emulator_port" -lt 1024 ] || [ "$emulator_port" -gt 65535 ]; then
  echo "ITBEM_AWS_EMULATOR_PORT must be between 1024 and 65535" >&2
  exit 1
fi
case "$emulator_image" in
  ghcr.io/getmoto/motoserver:*@sha256:*) ;;
  *)
    echo "The qualification emulator image must be pinned by version and digest" >&2
    exit 1
    ;;
esac

cleanup() {
  [ -z "$report_file" ] || rm -f -- "$report_file"
  [ "$container_may_exist" -eq 1 ] || return 0
  docker rm -f "$container_name" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

# Docker Desktop can expose its CLI pipe before the Linux engine is actually
# ready. A bare `docker run` may then block for a long time and make a local
# qualification look like a queue or provider failure. Probe the engine with
# a bounded, local-only command before any container is created. GNU timeout
# waits for and reaps this direct probe, including after its kill grace period;
# it never touches an existing container.
require_docker_engine() {
  if ! command -v timeout >/dev/null 2>&1; then
    echo "The Linux timeout utility is required for a bounded Docker engine probe." >&2
    exit 1
  fi
  probe_status=0
  timeout --kill-after=1s 15s docker version --format '{{.Server.Version}}' >/dev/null 2>&1 || probe_status=$?
  if [ "$probe_status" -eq 0 ]; then
    return 0
  fi
  if [ "$probe_status" -eq 124 ] || [ "$probe_status" -eq 137 ]; then
    echo "Docker engine did not become ready within 15 seconds; start Docker Desktop and retry the isolated qualification." >&2
  else
    echo "Docker engine is unavailable; start Docker Desktop and retry the isolated qualification." >&2
  fi
  exit 1
}

require_docker_engine

if docker container inspect "$container_name" >/dev/null 2>&1; then
  echo "The generated fixture container name is already occupied; refusing to touch it." >&2
  exit 1
fi

# Mark immediately before the create attempt, rather than after it succeeds:
# Docker can create a named container and still return an error while setting
# it up. The preflight above has already proven the engine responsive, so the
# trap can safely remove that possible partial container.
container_may_exist=1
docker run -d \
  --name "$container_name" \
  --read-only \
  --tmpfs /tmp:size=64m,mode=1777 \
  --cap-drop ALL \
  --security-opt no-new-privileges:true \
  --env MOTO_PORT=4566 \
  --publish "127.0.0.1:$emulator_port:4566" \
  "$emulator_image" >/dev/null

ready=0
attempt=0
while [ "$attempt" -lt 60 ]; do
  if docker exec "$container_name" python -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:4566/', timeout=2).read()" >/dev/null 2>&1; then
    ready=1
    break
  fi
  attempt=$((attempt + 1))
  sleep 1
done
if [ "$ready" -ne 1 ]; then
  docker logs "$container_name" >&2 || true
  echo "The loopback AWS emulator did not become healthy" >&2
  exit 1
fi

cd "$repository_root"
report_file=$(mktemp)
if AWS_ACCESS_KEY_ID=test \
AWS_SECRET_ACCESS_KEY=test \
AWS_SESSION_TOKEN= \
AWS_PROFILE= \
AWS_SHARED_CREDENTIALS_FILE=/dev/null \
AWS_CONFIG_FILE=/dev/null \
AWS_REGION=us-east-1 \
ITBEM_LOCALSTACK_E2E=1 \
ITBEM_GATEWAY_TRANSPORT_E2E=1 \
ITBEM_LOCALSTACK_ENDPOINT="http://127.0.0.1:$emulator_port" \
go test ./internal/automationagent ./controllers/automation -run '^Test(LocalStackTransportRoundTrip|LocalStackRedeliveryReusesDurableResultWithoutProviderRepeat|LocalGatewayFiveLaneTransportRoundTrip)$' -race -count=1 -timeout 180s -json > "$report_file"; then
  :
else
  cat "$report_file" >&2
  exit 1
fi
python3 scripts/verify_go_test_evidence.py "$report_file" \
  TestLocalStackTransportRoundTrip \
  TestLocalStackRedeliveryReusesDurableResultWithoutProviderRepeat \
  TestLocalGatewayFiveLaneTransportRoundTrip \
  TestLocalGatewayFiveLaneTransportRoundTrip/orchestration \
  TestLocalGatewayFiveLaneTransportRoundTrip/engineering \
  TestLocalGatewayFiveLaneTransportRoundTrip/review \
  TestLocalGatewayFiveLaneTransportRoundTrip/qa \
  TestLocalGatewayFiveLaneTransportRoundTrip/release

printf '\nLive AWS-emulator, five-lane HTTPS transport and durable redelivery qualification passed. Full engineering delivery remains a separate gate.\n'
