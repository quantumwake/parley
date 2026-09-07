.PHONY: build test plugin
build:
	go build ./...
test:
	go test -race ./...
plugin: ## build the binary the way the hook wrapper does (cached under ~/.statefs-ai/bin)
	GOFLAGS=-mod=vendor go build -o $${CLAUDE_PLUGIN_DATA:-$$HOME/.statefs-ai}/bin/parley ./cmd/parley
vendor: ## refresh vendored deps (needed after touching go.mod)
	go mod vendor
