# Go-only targets for the platform module. Day-to-day commands (up, down, credentials, ...) are
# mise tasks in the repository root: `mise tasks`.

.PHONY: build vet test image-agent

build:
	go build ./...

vet:
	go vet ./...

# Needs Postgres for the store-backed tests: `mise run test` starts it and sets the URL.
test:
	go test ./...

# The harness versions are pinned once, in mise.toml; mise passes them in.
image-agent:
	@test -n "$(CLAUDE_CODE_VERSION)" -a -n "$(CODEX_VERSION)" || \
		(echo "CLAUDE_CODE_VERSION and CODEX_VERSION are not set; run 'mise run image-agent'" >&2 && exit 1)
	docker build -t terrarium-agent:local -f images/agent/Dockerfile \
		--build-arg CLAUDE_CODE_VERSION=$(CLAUDE_CODE_VERSION) --build-arg CODEX_VERSION=$(CODEX_VERSION) .
