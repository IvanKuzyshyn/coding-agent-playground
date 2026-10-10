// Command credentials sets up the host-side credentials the gateway uses: the Claude token and
// GitHub token in the macOS keychain, and the gateway's own Codex login. It runs on the host,
// never in a container, because it needs the browser, the keychain and the vendor CLIs.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
)

const (
	claudeKeychain = "terrarium-claude-oauth"
	githubKeychain = "terrarium-github"
	githubPATURL   = "https://github.com/settings/personal-access-tokens/new"
)

type tool struct {
	name       string
	configured func() bool
	setup      func() error
}

func main() {
	tools := []tool{
		{"Claude", func() bool { return keychainHas(claudeKeychain) }, setupClaude},
		{"Codex", codexConfigured, setupCodex},
		{"GitHub", func() bool { return keychainHas(githubKeychain) }, setupGitHub},
	}
	var options []huh.Option[int]
	for i, t := range tools {
		options = append(options, huh.NewOption(t.name, i).Selected(true))
	}
	var selected []int
	if err := huh.NewMultiSelect[int]().
		Title("Configure credentials for").
		Options(options...).
		Value(&selected).
		Run(); err != nil {
		fail(err)
	}

	failed := false
	for _, i := range selected {
		t := tools[i]
		fmt.Printf("\n== %s ==\n", t.name)
		if t.configured() {
			again := false
			if err := huh.NewConfirm().Title(t.name + " is already configured. Set it up again?").Value(&again).Run(); err != nil {
				fail(err)
			}
			if !again {
				continue
			}
		}
		if err := t.setup(); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", t.name, err)
			failed = true
			continue
		}
		fmt.Printf("%s configured.\n", t.name)
	}
	if failed {
		os.Exit(1)
	}
}

func setupClaude() error {
	if err := requireCLI("claude", "install Claude Code: https://docs.claude.com/claude-code"); err != nil {
		return err
	}
	fmt.Println("Opening the Claude login. Copy the token it prints at the end.")
	if err := run(exec.Command("claude", "setup-token")); err != nil {
		return err
	}
	token, err := askSecret("Paste the Claude token")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(token, "sk-ant-oat01-") {
		return errors.New("not a Claude OAuth token (expected sk-ant-oat01-...)")
	}
	return keychainSet(claudeKeychain, token)
}

func codexHome() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".terrarium", "codex")
}

func codexConfigured() bool {
	_, err := os.Stat(filepath.Join(codexHome(), "auth.json"))
	return err == nil
}

// setupCodex logs in with a dedicated CODEX_HOME so the gateway owns its refresh token and never
// shares it with the host's ~/.codex.
func setupCodex() error {
	if err := requireCLI("codex", "install Codex: brew install codex"); err != nil {
		return err
	}
	dir := codexHome()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("cli_auth_credentials_store = \"file\"\n"), 0o600); err != nil {
		return err
	}
	cmd := exec.Command("codex", "login", "--device-auth")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+dir)
	return run(cmd)
}

// setupGitHub copies the gh login's token into the keychain; without gh it falls back to a pasted
// fine-grained personal access token.
func setupGitHub() error {
	var token string
	if _, err := exec.LookPath("gh"); err == nil {
		if exec.Command("gh", "auth", "status", "--hostname", "github.com").Run() != nil {
			if err := run(exec.Command("gh", "auth", "login", "--web", "--hostname", "github.com", "--git-protocol", "https")); err != nil {
				return err
			}
		}
		out, err := exec.Command("gh", "auth", "token", "--hostname", "github.com").Output()
		if err != nil {
			return fmt.Errorf("gh auth token: %w", err)
		}
		token = strings.TrimSpace(string(out))
	} else {
		fmt.Printf("gh is not installed. Create a fine-grained token with Contents and Pull requests\n"+
			"read/write for the repositories agents may use: %s\n", githubPATURL)
		_ = exec.Command("open", githubPATURL).Run()
		var err error
		if token, err = askSecret("Paste the GitHub token"); err != nil {
			return err
		}
	}
	if token == "" {
		return errors.New("empty token")
	}
	return keychainSet(githubKeychain, token)
}

func requireCLI(name, hint string) error {
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("%s CLI not found; %s", name, hint)
	}
	return nil
}

func run(cmd *exec.Cmd) error {
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", strings.Join(cmd.Args, " "), err)
	}
	return nil
}

func askSecret(title string) (string, error) {
	var v string
	err := huh.NewInput().Title(title).EchoMode(huh.EchoModePassword).Value(&v).Run()
	return strings.TrimSpace(v), err
}

func keychainHas(service string) bool {
	return exec.Command("security", "find-generic-password", "-a", os.Getenv("USER"), "-s", service).Run() == nil
}

// keychainSet passes the token on security's stdin rather than argv so it never shows up in the
// process list.
func keychainSet(service, token string) error {
	if strings.ContainsAny(token, " \"'\\\n") {
		return errors.New("token contains unexpected characters")
	}
	cmd := exec.Command("security", "-i")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("add-generic-password -U -a %s -s %s -w %s\n", os.Getenv("USER"), service, token))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("keychain: %v: %s", err, out)
	}
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
