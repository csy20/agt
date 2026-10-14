BIN_DIR := bin
export AGT_DATABASE_URL := postgres://agt:agt@127.0.0.1:54329/agt?sslmode=disable
export AGT_IMAGE_PYTHON := agt-sandbox:python
export AGT_IMAGE_GO := agt-sandbox:go
export AGT_IMAGE_NODE := agt-sandbox:node
export AGT_SANDBOX_IMAGE := $(AGT_IMAGE_PYTHON)
export AGT_LISTEN := 127.0.0.1:8787

# go build -o uses the name exactly and does not append .exe.
EXE :=
ifeq ($(OS),Windows_NT)
EXE := .exe
endif

.PHONY: build up image test demo clean

build:
	mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/agt$(EXE) ./cmd/agt
	go build -o $(BIN_DIR)/agtd$(EXE) ./cmd/agtd

up:
	docker compose -f deploy/compose.yaml up -d --wait

image:
	docker build -t $(AGT_IMAGE_PYTHON) -f deploy/python.Dockerfile deploy
	docker build -t $(AGT_IMAGE_GO) -f deploy/go.Dockerfile deploy
	docker build -t $(AGT_IMAGE_NODE) -f deploy/node.Dockerfile deploy

test: up image
	go test ./...

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
