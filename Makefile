# statefs.ai parley
.PHONY: help build plugin install uninstall console vendor release release-check check check-identity check-mcp check-search check-console check-all soak-wake test test-oracle test-oracle-io test-conformance test-load test-swarm swarm-enroll

help: ## list targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-18s %s\n", $$1, $$2}'

## build
build: ## compile everything (vendored deps)
	GOFLAGS=-mod=vendor go build ./...
plugin: ## build the binary the way the hook wrapper does (cached under ~/.statefs-ai/bin)
	GOFLAGS=-mod=vendor go build -o $${CLAUDE_PLUGIN_DATA:-$$HOME/.statefs-ai}/bin/parley ./cmd/parley
console: ## build the viewer page into cmd/parley/dist (embedded by go build; needs node)
	cd console && npm install --silent && npm run build
install: ## build parley and link it onto your PATH (DIR=/somewhere/bin to choose where)
	GOFLAGS=-mod=vendor go build -o $${CLAUDE_PLUGIN_DATA:-$$HOME/.statefs-ai}/bin/parley ./cmd/parley
	@$${CLAUDE_PLUGIN_DATA:-$$HOME/.statefs-ai}/bin/parley install-path $${DIR:+--dir $$DIR}
	@$${CLAUDE_PLUGIN_DATA:-$$HOME/.statefs-ai}/bin/parley version
uninstall: ## remove the parley launcher from your PATH (identities and recorded data are untouched)
	@p="$$(command -v parley 2>/dev/null)"; \
	if [ -z "$$p" ]; then echo "parley is not on your PATH"; \
	elif grep -q "parley launcher" "$$p" 2>/dev/null || [ -L "$$p" ]; then rm -f "$$p" && echo "removed $$p"; \
	else echo "$$p is not the parley launcher; leaving it alone"; fi
vendor: ## refresh vendored deps (after touching go.mod)
	go mod vendor

## check
check: ## offline gate: vet, race tests, manifests, versions agree
	scripts/checks.sh offline
check-identity: ## every enrolled identity exchanges a token, with its caps
	scripts/checks.sh identity
soak-wake: ## soak the real binary's wait/wake delivery (3 sessions, kills, 60 posts, ~25 s); fails on any lost post. SEED=n CHAOS=0 tune it
	bin=$$(mktemp -d)/parley && GOFLAGS=-mod=vendor go build -o $$bin ./cmd/parley && PARLEY_SOAK_BIN=$$bin PARLEY_SOAK_SEED=$${SEED:-1} PARLEY_SOAK_CHAOS=$${CHAOS:-1} GOFLAGS=-mod=vendor go test -tags soak -run TestWakeSoak -v -count=1 ./pkg/plugin
check-mcp: ## the MCP server handshakes, lists its tools, and calls one
	scripts/checks.sh mcp
check-search: ## labels, find, and a create/join/post/read/delete round trip
	scripts/checks.sh search
check-console: ## the console API answers with the shape the viewer needs
	scripts/checks.sh console
check-all: ## every check above, stopping at the first failure (IDENTITY=<name> to pick who)
	scripts/checks.sh all

## test
test: ## unit tests with the race detector (no network)
	GOFLAGS=-mod=vendor go vet ./... && GOFLAGS=-mod=vendor go test -race ./...
test-oracle: ## M1 oracle offline: one real Claude Code session into a file store, replay equals transcript
	scripts/oracle-m1.sh
test-oracle-io: ## M1 oracle against the enrolled directory (STATEFS_DIRECTORY or config.json required)
	@test -n "$$STATEFS_DIRECTORY" -o -f "$$HOME/.statefs-ai/config.json" || { echo "enroll this machine first (or set STATEFS_DIRECTORY)"; exit 2; }
	scripts/oracle-m1.sh
test-conformance: ## store contract against the enrolled directory (creates purpose:conformance namespaces)
	@test -n "$$STATEFS_DIRECTORY" || { echo "STATEFS_DIRECTORY is required; do not point this at production unless you intend to"; exit 2; }
	GOFLAGS=-mod=vendor go test -count=1 -run TestConformanceAgainstStatefsIO ./pkg/store/statefs/
test-load: ## N concurrent haiku sessions for T minutes, one conversation each: make test-load AGENTS=10 MINUTES=3
	python3 scripts/loadtest.py --agents $${AGENTS:-10} --minutes $${MINUTES:-3} --interval $${INTERVAL:-6}
test-swarm: ## N agents, N identities, one shared conversation (docs/reference/02_swarm.md): make test-swarm AGENTS=a,b,c MINUTES=5 EXAMPLE=parallel-docs (or TASK="...")
	@test -n "$(TASK)" -o -n "$(EXAMPLE)" || { echo "TASK=\"...\" or EXAMPLE=naming|parallel-docs|code-review|estimation|debate is required"; exit 2; }
	python3 -u scripts/swarm.py run --agents $${AGENTS:-a,b,c} --minutes $${MINUTES:-5} --interval $${INTERVAL:-20} --model $${MODEL:-haiku} --task "$(TASK)" $${EXAMPLE:+--example $$EXAMPLE} $${CHANNEL:+--channel $$CHANNEL}
release: ## build, test, push, reinstall the plugin, then run a swarm: make release MSG="what changed"
	@test -n "$(MSG)" || { echo 'MSG="what changed" is required'; exit 2; }
	scripts/release-and-test.sh "$(MSG)" $${EXAMPLE:+--example $$EXAMPLE} $${AGENTS:+--agents $$AGENTS}
release-check: ## everything the release does up to the push, and nothing that leaves this machine
	scripts/release-and-test.sh --no-push
swarm-enroll: ## enroll one swarm identity: make swarm-enroll NAME=a URL='<enrollment url>'
	@test -n "$(NAME)" -a -n "$(URL)" || { echo "NAME= and URL= are required"; exit 2; }
	python3 scripts/swarm.py enroll $(NAME) '$(URL)'
