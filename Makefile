.PHONY: test build registry-up registry-down fixtures golden test-integration helm3 compat bench plugin-smoke docs-build docs-serve

VERSION ?= 0.1.0
LDFLAGS = -ldflags "-X main.Version=$(VERSION)"

test:
	go test ./...

build:
	go build $(LDFLAGS) -o bin/forge ./cmd/forge

registry-up:
	@docker ps --format '{{.Names}}' | grep -qx forge-registry || \
		docker run -d --rm -p 5001:5000 --name forge-registry registry:2 >/dev/null

registry-down:
	-docker stop forge-registry forge-chartrepo

fixtures: registry-up
	scripts/fixtures.sh

golden: fixtures
	scripts/golden.sh

test-integration: fixtures
	go test -tags integration -count=1 -v ./test/integration/

helm3:
	scripts/get-helm3.sh

compat: build fixtures helm3
	scripts/compat.sh

bench: build fixtures
	scripts/bench.sh

plugin-smoke:
	scripts/plugin-smoke.sh

.venv-docs: docs/requirements.txt
	python3 -m venv .venv-docs
	.venv-docs/bin/pip install -q -r docs/requirements.txt
	touch .venv-docs

docs-build: .venv-docs
	.venv-docs/bin/mkdocs build --strict

docs-serve: .venv-docs
	.venv-docs/bin/mkdocs serve
