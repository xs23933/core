#!/usr/bin/env bash
set -euo pipefail

# Verify the published baseline without a replace to the current source checkout.
scaffold_tmp=$(mktemp -d)
trap 'rm -rf "$scaffold_tmp"' EXIT
go build -o "$scaffold_tmp/corectl" ./cmd/corectl
for kind in http grpc; do
  "$scaffold_tmp/corectl" new "$kind" -m "example.com/scaffold/$kind" -t "$kind" -o "$scaffold_tmp"
  (
    cd "$scaffold_tmp/$kind"
    export GOWORK=off
    if [ "$kind" = grpc ]; then
      protoc --go_out=. --go_opt="module=example.com/scaffold/$kind" \
        --go-grpc_out=. --go-grpc_opt="module=example.com/scaffold/$kind" proto/user/v1/user.proto
    fi
    go mod tidy
    "$scaffold_tmp/corectl" gen enums --check
    go test ./... -count=1 -timeout=60s
    go vet ./...
    go build -o server ./cmd
  )
done
