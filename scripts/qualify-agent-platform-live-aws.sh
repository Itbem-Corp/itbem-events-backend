#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
compose_file="$repository_root/deploy/staging/aws-emulator.compose.yml"
emulator_port=${ITBEM_AWS_EMULATOR_PORT:-14566}
container_name="itbem-agent-qualification-$$"
emulator_image=$(awk '$1 == "image:" { print $2; exit }' "$compose_file")

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
  docker rm -f "$container_name" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

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
AWS_ACCESS_KEY_ID=test \
AWS_SECRET_ACCESS_KEY=test \
AWS_REGION=us-east-1 \
ITBEM_AWS_EMULATOR_E2E=1 \
ITBEM_AWS_EMULATOR_ENDPOINT="http://127.0.0.1:$emulator_port" \
go test ./internal/automationagent -run '^TestAWSEmulator' -count=1
