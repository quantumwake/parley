# statefs.ai parley
.PHONY: help build plugin console vendor test test-oracle test-oracle-io test-conformance test-load test-swarm swarm-enroll

help: ## list targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-18s %s\n", $$1, $$2}'

## build
build: ## compile everything (vendored deps)
	GOFLAGS=-mod=vendor go build ./...
plugin: ## build the binary the way the hook wrapper does (cached under ~/.statefs-ai/bin)
	GOFLAGS=-mod=vendor go build -o $${CLAUDE_PLUGIN_DATA:-$$HOME/.statefs-ai}/bin/parley ./cmd/parley
console: ## build the viewer page into cmd/parley/dist (embedded by go build; needs node)
	cd console && npm install --silent && npm run build
vendor: ## refresh vendored deps (after touching go.mod)
	go mod vendor

## test
test: ## unit tests with the race detector (no network)
	GOFLAGS=-mod=vendor go vet ./... && GOFLAGS=-mod=vendor go test -race ./...
test-oracle: ## M1 oracle offline: one real Claude Code session into a file store, replay equals transcript
	scripts/oracle-m1.sh
test-oracle-io: ## M1 oracle against statefs.io (uses ~/.statefs-ai/config.json)
	STATEFS_DIRECTORY=$${STATEFS_DIRECTORY:-https://directory.statefs.io} scripts/oracle-m1.sh
test-conformance: ## store contract against statefs.io (creates and deletes purpose:conformance namespaces; manage key deletes them)
	STATEFS_DIRECTORY=$${STATEFS_DIRECTORY:-https://directory.statefs.io} GOFLAGS=-mod=vendor go test -count=1 -run TestConformanceAgainstStatefsIO ./pkg/store/statefs/
test-load: ## N concurrent haiku sessions for T minutes, one conversation each: make test-load AGENTS=10 MINUTES=3
	python3 scripts/loadtest.py --agents $${AGENTS:-10} --minutes $${MINUTES:-3} --interval $${INTERVAL:-6}
test-swarm: ## N agents with their own identities talking through one shared conversation: make test-swarm AGENTS=a,b,c MINUTES=5 TASK="..."
	@test -n "$(TASK)" || { echo "TASK=\"...\" is required"; exit 2; }
	python3 scripts/swarm.py run --agents $${AGENTS:-a,b,c} --minutes $${MINUTES:-5} --interval $${INTERVAL:-20} --model $${MODEL:-haiku} --task "$(TASK)" $${CHANNEL:+--channel $$CHANNEL}
swarm-enroll: ## enroll one swarm identity: make swarm-enroll NAME=a URL='https://directory.statefs.io/enroll#en_...'
	@test -n "$(NAME)" -a -n "$(URL)" || { echo "NAME= and URL= are required"; exit 2; }
	python3 scripts/swarm.py enroll $(NAME) '$(URL)'
