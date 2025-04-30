#!/bin/bash
set -euf -o pipefail
echo "Running tests with race detector..."
mkdir -p tmp
go test -v -coverprofile=./tmp/cov -race ./...
echo "PASSED TESTS"
echo "Displaying coverage report..."
go tool cover -func ./tmp/cov
echo "DONE"
