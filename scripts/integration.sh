#!/bin/sh
# Starts a throwaway Prometheus in Docker with this repo's prometheus.yml and runs the
# integration test against it. Usage: scripts/integration.sh
set -eu
cd "$(dirname "$0")/.."

name=timemore-dot-prometheus-exporter-it
port=${PROMETHEUS_PORT:-19090}
docker rm -f "$name" >/dev/null 2>&1 || true
docker run -d --rm --name "$name" -p "127.0.0.1:$port:9090" \
  -v "$PWD/prometheus.yml:/etc/prometheus/prometheus.yml:ro" \
  prom/prometheus:v3.5.0 \
  --config.file=/etc/prometheus/prometheus.yml \
  --storage.tsdb.path=/prometheus \
  --web.enable-remote-write-receiver >/dev/null
trap 'docker rm -f "$name" >/dev/null 2>&1 || true' EXIT

i=0
until curl -fs "http://127.0.0.1:$port/-/ready" >/dev/null; do
  i=$((i + 1))
  [ "$i" -lt 60 ] || { echo "Prometheus did not become ready"; docker logs "$name"; exit 1; }
  sleep 1
done

PROMETHEUS_URL="http://127.0.0.1:$port" go test -tags integration -count=1 -run TestIntegration -v .
