BIN_DIR := bin
export AGT_DATABASE_URL := postgres://agt:agt@127.0.0.1:54329/agt?sslmode=disable
export AGT_SANDBOX_IMAGE := agt-sandbox:local
export AGT_LISTEN := 127.0.0.1:8787

.PHONY: build up image test demo clean

build:
	mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/agt ./cmd/agt
	go build -o $(BIN_DIR)/agtd ./cmd/agtd

up:
	docker compose -f deploy/compose.yaml up -d --wait

image:
	docker build -t $(AGT_SANDBOX_IMAGE) -f deploy/sandbox.Dockerfile deploy

test: up image
	go test ./...

demo: build up image
	mkdir -p /tmp/agt-demo
	AGT_DATA_DIR=/tmp/agt-demo ./$(BIN_DIR)/agtd >/tmp/agtd.log 2>&1 & echo $$! >/tmp/agtd.pid
	i=0; \
	until curl -sf http://127.0.0.1:8787/healthz >/dev/null || [ $$i -ge 40 ]; do \
		i=$$((i+1)); sleep 0.25; \
	done
	./$(BIN_DIR)/agt fix --repo eval/fixtures/payrepo --provider fake --script eval/scripts/fix_retry.json "make the retry test pass"; \
	status=$$?; \
	kill $$(cat /tmp/agtd.pid) >/dev/null 2>&1 || true; \
	exit $$status

clean:
	rm -rf $(BIN_DIR)
	docker compose -f deploy/compose.yaml down
