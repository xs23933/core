#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

# Repeat existing concurrency regressions under single- and multi-CPU scheduling.
go test -race -count=5 -cpu=1,4 -shuffle=on -timeout=10m \
  -run 'Test.*(Concurrent|Stress|Shutdown|LimitedErrGroup|ReplaceHandle|HTTP3Publication|CanceledGRPCAdmission|EarlyForceStop)' \
  . ./websocket ./middleware/metrics
