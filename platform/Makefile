COMPOSE := docker compose -f deploy/compose.yaml
# Real credentials come from the macOS keychain into the gateway's compose secret only.
CLAUDE_TOKEN = $$(security find-generic-password -a "$$USER" -s terrarium-claude-oauth -w 2>/dev/null)
GITHUB_TOKEN = $$(security find-generic-password -a "$$USER" -s terrarium-github -w 2>/dev/null)
CODEX_HOME_DIR := $(HOME)/.terrarium/codex

.PHONY: up deps-up down reset migrate logs ps build test vet image-agent codex-login credentials

up: image-agent ## Start postgres, server, gateway and web UI
	@mkdir -p $(CODEX_HOME_DIR)
	@test -f $(HOME)/.terrarium/session.key || (umask 077 && openssl rand -hex 32 > $(HOME)/.terrarium/session.key)
	$(COMPOSE) up -d --wait postgres
	@# Old server code must not act on rows whose meaning a migration changes.
	$(COMPOSE) stop server
	$(COMPOSE) run --rm --build migrate
	@test -f $(HOME)/.terrarium/web.secret || (umask 077 && openssl rand -hex 32 > $(HOME)/.terrarium/web.secret)
	TERRARIUM_CLAUDE_OAUTH_TOKEN="$(CLAUDE_TOKEN)" TERRARIUM_GITHUB_TOKEN="$(GITHUB_TOKEN)" \
	TERRARIUM_GIT_USER_NAME="$$(git config --global user.name)" TERRARIUM_GIT_USER_EMAIL="$$(git config --global user.email)" \
	TERRARIUM_WEB_SECRET="$$(cat $(HOME)/.terrarium/web.secret)" \
		$(COMPOSE) up -d --build --wait postgres server gateway web

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

codex-login: ## Log in to ChatGPT for the gateway's own Codex credential (separate from ~/.codex)
	@mkdir -p $(CODEX_HOME_DIR)
	@printf 'cli_auth_credentials_store = "file"\n' > $(CODEX_HOME_DIR)/config.toml
	CODEX_HOME=$(CODEX_HOME_DIR) codex login --device-auth

credentials: ## Interactively set up Claude, Codex and GitHub credentials on the host
	go run ./cmd/credentials
