BIN_DIR := bin
export AGT_DATABASE_URL := postgres://agt:agt@127.0.0.1:54329/agt?sslmode=disable
export AGT_IMAGE_PYTHON := agt-sandbox:python
export AGT_IMAGE_GO := agt-sandbox:go
export AGT_IMAGE_NODE := agt-sandbox:node
export AGT_SANDBOX_IMAGE := $(AGT_IMAGE_PYTHON)
export AGT_LISTEN := 127.0.0.1:8787

GIT_REV := $(shell git rev-parse --short=12 HEAD)
GIT_DIRTY := $(shell git status --porcelain | grep -q . && echo -dirty)
BUILD_STAMP := $(shell date -u +%Y%m%dT%H%M%SZ)
LDFLAGS := -X github.com/csy20/agt/internal/version.Build=local+$(GIT_REV)$(GIT_DIRTY).$(BUILD_STAMP)

# go build -o uses the name exactly and does not append .exe.
EXE :=
ifeq ($(OS),Windows_NT)
EXE := .exe
endif

.PHONY: build up image test unit package demo clean

# Dependency-free packages. See scripts/unit-packages.txt.
UNIT_PKGS := $(shell sed '/^[[:space:]]*\#/d;/^[[:space:]]*$$/d' scripts/unit-packages.txt)

build:
	mkdir -p $(BIN_DIR)
	go build -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/agt$(EXE) ./cmd/agt
	go build -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/agtd$(EXE) ./cmd/agtd

up:
	docker compose -f deploy/compose.yaml up -d --wait

image:
	docker build -t $(AGT_IMAGE_PYTHON) -f deploy/python.Dockerfile deploy
	docker build -t $(AGT_IMAGE_GO) -f deploy/go.Dockerfile deploy
	docker build -t $(AGT_IMAGE_NODE) -f deploy/node.Dockerfile deploy

# test is the integration job. It starts Postgres, builds the three images,
# and fails when Docker, an image, or Postgres is missing. AGT_INTEGRATION=1
# turns the old skips into failures. The timeout covers the scripted repair batch.
# AGT_DATABASE_URL here is the maintenance database. Tests create agt_test_*
# databases and refuse to reset or drop database agt. make demo keeps the
# shared development URL exported above.
test: up image
	AGT_INTEGRATION=1 AGT_DATABASE_URL=postgres://agt:agt@127.0.0.1:54329/postgres?sslmode=disable \
		go test -timeout 20m ./...

# unit does not start Postgres or Docker. It clears the integration variables
# that this Makefile exports. The sandbox package then skips
# TestSandboxRunsTestsAndStaysClosed. That skip is not container support.
unit:
	env -u AGT_INTEGRATION -u AGT_DATABASE_URL -u AGT_IMAGE_PYTHON -u AGT_IMAGE_GO -u AGT_IMAGE_NODE -u AGT_SANDBOX_IMAGE \
		go test $(UNIT_PKGS)

package:
	sh scripts/package.sh

demo: build up image
	mkdir -p /tmp/agt-demo
	chmod 700 /tmp/agt-demo
	AGT_DATA_DIR=/tmp/agt-demo ./$(BIN_DIR)/agtd$(EXE) >/tmp/agtd.log 2>&1 & echo $$! >/tmp/agtd.pid
	i=0; \
	until curl -sf -D - http://127.0.0.1:8787/healthz | grep -q '^X-Agt-Product: agt' || [ $$i -ge 40 ]; do \
		i=$$((i+1)); sleep 0.25; \
	done
	AGT_DATA_DIR=/tmp/agt-demo ./$(BIN_DIR)/agt$(EXE) fix --repo eval/fixtures/payrepo --profile python --test-arg python --test-arg -m --test-arg unittest --test-arg tests.test_retry --provider fake --script eval/scripts/fix_retry.json "make the retry test pass"; \
	status=$$?; \
	kill $$(cat /tmp/agtd.pid) >/dev/null 2>&1 || true; \
	exit $$status

clean:
	rm -rf $(BIN_DIR)
	docker compose -f deploy/compose.yaml down
