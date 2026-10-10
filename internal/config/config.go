// Package config parses process configuration from the environment.
package config

import "github.com/caarlos0/env/v11"

type Server struct {
	DatabaseURL   string `env:"DATABASE_URL,required"`
	Addr          string `env:"TERRARIUM_ADDR" envDefault:":8080"`
	EngineersFile string `env:"TERRARIUM_ENGINEERS_FILE" envDefault:"/etc/terrarium/engineers.yaml"`
	PresetsFile   string `env:"TERRARIUM_PRESETS_FILE" envDefault:"/etc/terrarium/presets.yaml"`
	// Docker network the sandboxes join. Internal: no route to the internet.
	SandboxNetwork string `env:"TERRARIUM_SANDBOX_NETWORK" envDefault:"terrarium-sandbox"`
	// The server joins the sandbox network to reach sandboxd; requests from it are refused.
	SandboxSubnet string `env:"TERRARIUM_SANDBOX_SUBNET" envDefault:"172.31.250.0/24"`
	// Label value that scopes orphan cleanup to this deployment's sandboxes.
	Instance string `env:"TERRARIUM_INSTANCE" envDefault:"local"`
	// Base URL of the gateway's inference listener as seen from a sandbox.
	GatewayURL string `env:"TERRARIUM_GATEWAY_URL" envDefault:"http://gateway:8080"`
	// host:port of the gateway's CONNECT proxy as seen from a sandbox.
	GatewayProxy string `env:"TERRARIUM_GATEWAY_PROXY" envDefault:"gateway:3128"`
	// Key that derives each session's gateway secret (credentials.DeriveSecret).
	SessionKeyFile string `env:"TERRARIUM_SESSION_KEY_FILE" envDefault:"/run/secrets/session_key"`
	DockerSocket   string `env:"TERRARIUM_DOCKER_SOCKET" envDefault:"/var/run/docker.sock"`
	// Max sessions an engineer may have that are not stopped.
	MaxActiveSessionsPerOwner int `env:"TERRARIUM_MAX_ACTIVE_SESSIONS" envDefault:"4"`
}

type Gateway struct {
	DatabaseURL string `env:"DATABASE_URL,required"`
	Addr        string `env:"TERRARIUM_GATEWAY_ADDR" envDefault:":8080"`
	ProxyAddr   string `env:"TERRARIUM_GATEWAY_PROXY_ADDR" envDefault:":3128"`
	// File holding the Claude Max OAuth token from `claude setup-token`.
	ClaudeTokenFile string `env:"TERRARIUM_CLAUDE_TOKEN_FILE" envDefault:"/run/secrets/claude_oauth_token"`
	// Codex auth.json from a login dedicated to the gateway. Read-write: the gateway is its only refresher.
	CodexAuthFile string `env:"TERRARIUM_CODEX_AUTH_FILE" envDefault:"/var/lib/terrarium/codex/auth.json"`
}

type Migrate struct {
	DatabaseURL string `env:"DATABASE_URL,required"`
}

func Load[T any]() (T, error) {
	return env.ParseAs[T]()
}
