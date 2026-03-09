BINARY   := k8s-mcp-server
SRC_DIR  := src
BUILD    := CGO_ENABLED=0 go build -buildvcs=false -ldflags="-w -s"

.PHONY: build test vet fmt fmt-check lint check docker deploy clean hooks

build:
	cd $(SRC_DIR) && $(BUILD) -o $(BINARY) .

test:
	cd $(SRC_DIR) && CGO_ENABLED=0 go test -buildvcs=false ./...

vet:
	cd $(SRC_DIR) && CGO_ENABLED=0 go vet -buildvcs=false ./...

fmt:
	cd $(SRC_DIR) && gofmt -w .

fmt-check:
	@cd $(SRC_DIR) && test -z "$$(gofmt -l .)" || \
		(echo "Files not formatted:"; gofmt -l .; exit 1)

lint:
	@command -v golangci-lint >/dev/null 2>&1 && \
		(cd $(SRC_DIR) && golangci-lint run) || \
		echo "golangci-lint not installed, skipping"

check: fmt-check vet test build

docker:
	docker compose build

deploy:
	./redeploy.sh

clean:
	rm -f $(SRC_DIR)/$(BINARY)

hooks:
	@echo "Installing pre-commit hook..."
	@mkdir -p .git/hooks
	@cp scripts/pre-commit .git/hooks/pre-commit
	@chmod +x .git/hooks/pre-commit
	@echo "Done. Hook installed at .git/hooks/pre-commit"
