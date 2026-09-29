// This file is a Go port of packages/ai/src/cli.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// pith-ai is the standalone OAuth login CLI: it lists the built-in providers
// that expose an OAuth flow, runs the interactive login and persists the
// resulting credential to auth.json. The Go port keeps the observable command
// surface (help/list/login) and never prints credential material: only the
// authorization URL, device code and progress messages reach the terminal.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/minifish-org/pith/packages/ai"
	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
	"github.com/minifish-org/pith/packages/ai/providers"
)

// DefaultAuthFile is the credential file written by the CLI, matching upstream.
const DefaultAuthFile = "auth.json"

// CLI carries the injectable streams and credential path used by tests.
type CLI struct {
	Stdin    io.Reader
	Stdout   io.Writer
	Stderr   io.Writer
	AuthFile string
}

// NewCLI builds a CLI bound to the process streams and the default auth file.
func NewCLI() *CLI {
	return &CLI{
		Stdin:    os.Stdin,
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
		AuthFile: DefaultAuthFile,
	}
}

func main() {
	if err := NewCLI().Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err.Error())
		os.Exit(1)
	}
}

// OAuthProviders returns the built-in providers that expose an OAuth flow, in
// the built-in collection order.
func OAuthProviders() []ai.Provider {
	all := providers.BuiltinProviders()
	result := make([]ai.Provider, 0, len(all))
	for _, provider := range all {
		if provider.Auth().OAuth != nil {
			result = append(result, provider)
		}
	}
	return result
}

// LoadAuth reads auth.json into a provider-keyed credential map. A missing or
// malformed file yields an empty map, matching upstream's best-effort load.
func LoadAuth(path string) map[string]authtypes.OAuthCredential {
	raw, err := os.ReadFile(path)
	if err != nil {
		return map[string]authtypes.OAuthCredential{}
	}
	result := map[string]authtypes.OAuthCredential{}
	if err := json.Unmarshal(raw, &result); err != nil {
		return map[string]authtypes.OAuthCredential{}
	}
	if result == nil {
		return map[string]authtypes.OAuthCredential{}
	}
	return result
}

// SaveAuth writes the credential map as indented JSON, matching upstream.
func SaveAuth(path string, auth map[string]authtypes.OAuthCredential) error {
	encoded, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0o600)
}

// Run executes one CLI invocation. It returns an error instead of exiting so
// tests can assert behavior without a subprocess.
func (c *CLI) Run(args []string) error {
	command := ""
	if len(args) > 0 {
		command = args[0]
	}
	switch command {
	case "", "help", "--help", "-h":
		c.printUsage()
		return nil
	case "list":
		for _, provider := range OAuthProviders() {
			fmt.Fprintf(c.Stdout, "%-20s %s\n", provider.ID(), provider.Name())
		}
		return nil
	case "login":
		return c.runLogin(args[1:])
	default:
		return fmt.Errorf("Unknown command: %s", command)
	}
}

func (c *CLI) printUsage() {
	lines := []string{
		"Usage: pith-ai <command> [provider]",
		"",
		"Commands:",
		"  login [provider]  Login to an OAuth provider",
		"  list              List available providers",
		"",
		"Providers:",
	}
	for _, provider := range OAuthProviders() {
		lines = append(lines, fmt.Sprintf("  %-20s %s", provider.ID(), provider.Name()))
	}
	fmt.Fprintln(c.Stdout, strings.Join(lines, "\n"))
}

func (c *CLI) runLogin(args []string) error {
	providerID := ""
	if len(args) > 0 {
		providerID = args[0]
	}
	oauthProviders := OAuthProviders()
	if providerID == "" {
		if len(oauthProviders) == 0 {
			return fmt.Errorf("Unknown provider: ")
		}
		for index, provider := range oauthProviders {
			fmt.Fprintf(c.Stdout, "  %d. %s\n", index+1, provider.Name())
		}
		answer, err := c.ask(fmt.Sprintf("Enter number (1-%d): ", len(oauthProviders)))
		if err != nil {
			return err
		}
		choice, err := strconv.Atoi(strings.TrimSpace(answer))
		if err != nil || choice < 1 || choice > len(oauthProviders) {
			return fmt.Errorf("Unknown provider: %s", answer)
		}
		providerID = oauthProviders[choice-1].ID()
	}
	var selected ai.Provider
	for _, provider := range oauthProviders {
		if provider.ID() == providerID {
			selected = provider
			break
		}
	}
	if selected == nil {
		return fmt.Errorf("Unknown provider: %s", providerID)
	}
	return c.login(selected)
}

func (c *CLI) login(provider ai.Provider) error {
	oauth := provider.Auth().OAuth
	if oauth == nil || oauth.Login == nil {
		return fmt.Errorf("Unknown provider: %s", provider.ID())
	}
	reader := bufio.NewReader(c.Stdin)
	interaction := authtypes.NewInteraction(context.Background(), func(_ context.Context, prompt authtypes.AuthPrompt) (string, error) {
		return c.answerPrompt(reader, prompt)
	}, func(event authtypes.AuthEvent) {
		c.notify(event)
	})
	credential, err := oauth.Login(context.Background(), interaction)
	if err != nil {
		return err
	}
	auth := LoadAuth(c.AuthFile)
	auth[provider.ID()] = *credential
	if err := SaveAuth(c.AuthFile, auth); err != nil {
		return err
	}
	fmt.Fprintf(c.Stdout, "\nCredentials saved to %s\n", c.AuthFile)
	return nil
}

func (c *CLI) ask(question string) (string, error) {
	fmt.Fprint(c.Stdout, question)
	reader := bufio.NewReader(c.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (c *CLI) answerPrompt(reader *bufio.Reader, prompt authtypes.AuthPrompt) (string, error) {
	if prompt.Type == authtypes.AuthPromptSelect {
		fmt.Fprintf(c.Stdout, "\n%s\n", prompt.Message)
		for index, option := range prompt.Options {
			fmt.Fprintf(c.Stdout, "  %d. %s\n", index+1, option.Label)
		}
		fmt.Fprintf(c.Stdout, "Enter number (1-%d): ", len(prompt.Options))
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		choice, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || choice < 1 || choice > len(prompt.Options) {
			return "", fmt.Errorf("Invalid selection")
		}
		return prompt.Options[choice-1].ID, nil
	}
	placeholder := ""
	if prompt.Placeholder != nil {
		placeholder = fmt.Sprintf(" (%s)", *prompt.Placeholder)
	}
	fmt.Fprintf(c.Stdout, "%s%s: ", prompt.Message, placeholder)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (c *CLI) notify(event authtypes.AuthEvent) {
	switch event.Type {
	case authtypes.AuthEventAuthURL:
		if event.URL != nil {
			fmt.Fprintf(c.Stdout, "\nOpen this URL in your browser:\n%s\n", *event.URL)
		}
		if event.Instructions != nil {
			fmt.Fprintln(c.Stdout, *event.Instructions)
		}
	case authtypes.AuthEventDeviceCode:
		if event.VerificationURI != nil {
			fmt.Fprintf(c.Stdout, "\nOpen this URL in your browser:\n%s\n", *event.VerificationURI)
		}
		if event.UserCode != nil {
			fmt.Fprintf(c.Stdout, "Enter code: %s\n", *event.UserCode)
		}
	case authtypes.AuthEventInfo, authtypes.AuthEventProgress:
		if event.Message != nil {
			fmt.Fprintln(c.Stdout, *event.Message)
		}
	}
}
