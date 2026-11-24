#!/usr/bin/env bash
set -euo pipefail

output_dir="${1:-testdata/certs}"
mkdir -p "$output_dir"
openssl req -x509 -newkey rsa:2048 -nodes -days 7 \
  -keyout "$output_dir/backend-key.pem" \
  -out "$output_dir/backend-cert.pem" \
  -subj "/CN=slb-test-backend" \
  -addext "subjectAltName=DNS:slb-test-backend"
echo "generated test certificate and key in $output_dir"
