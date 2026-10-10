COMPOSE := docker compose -f deploy/compose.yaml
# Real credentials come from the macOS keychain into the gateway's compose secret only.
CLAUDE_TOKEN = $$(security find-generic-password -a "$$USER" -s terrarium-claude-oauth -w 2>/dev/null)
CODEX_HOME_DIR := $(HOME)/.terrarium/codex

.PHONY: up deps-up down reset migrate logs ps build test vet image-agent token codex-login

up: image-agent ## Start postgres, server and gateway
	@mkdir -p $(CODEX_HOME_DIR)
	@test -f $(HOME)/.terrarium/session.key || (umask 077 && openssl rand -hex 32 > $(HOME)/.terrarium/session.key)
	TERRARIUM_CLAUDE_OAUTH_TOKEN="$(CLAUDE_TOKEN)" $(COMPOSE) up -d --build --wait postgres server gateway

deps-up: ## Start postgres only
	$(COMPOSE) up -d --wait postgres

down:
	$(COMPOSE) down

reset: ## Drop all state (database volume)
	$(COMPOSE) down -v

migrate:
	$(COMPOSE) run --rm --build migrate

logs:
	$(COMPOSE) logs -f server gateway

ps:
	$(COMPOSE) ps

build:
	go build ./...

vet:
	go vet ./...

test:
	go test ./...

image-agent: ## Build the sandbox image
	docker build -t terrarium-agent:local -f images/agent/Dockerfile .

token: ## Create an engineer API token: make token NAME=alice
	@test -n "$(NAME)" || (echo "usage: make token NAME=<engineer>" && exit 1)
	@test -f config/engineers.yaml || printf 'engineers:\n' > config/engineers.yaml
	@tok=$$(openssl rand -hex 32); \
	 printf '  - name: %s\n    token_sha256: %s\n' "$(NAME)" "$$(printf %s "$$tok" | shasum -a 256 | cut -d' ' -f1)" >> config/engineers.yaml; \
	 echo "API token for $(NAME) (shown once): $$tok"

codex-login: ## Log in to ChatGPT for the gateway's own Codex credential (separate from ~/.codex)
	@mkdir -p $(CODEX_HOME_DIR)
	@printf 'cli_auth_credentials_store = "file"\n' > $(CODEX_HOME_DIR)/config.toml
	CODEX_HOME=$(CODEX_HOME_DIR) codex login --device-auth
