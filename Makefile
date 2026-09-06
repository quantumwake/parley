.PHONY: build test plugin
build:
	go build ./...
test:
	go test -race ./...
plugin: ## build the plugin binary into plugin/bin
	go build -o plugin/bin/statefs-ai ./cmd/statefs-ai
