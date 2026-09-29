// This file is a Go port of packages/ai/src/auth/context.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package auth

import (
	"os"
	"path/filepath"
	"strings"

	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
)

// EnvAuthContext adapts process environment variables and the filesystem to
// the auth context contract. It is the injectable seam used by tests.
type EnvAuthContext struct {
	// LookupEnv defaults to os.LookupEnv when nil.
	LookupEnv func(name string) (string, bool)
	// Stat defaults to os.Stat when nil.
	Stat func(path string) (os.FileInfo, error)
	// HomeDir defaults to os.UserHomeDir when nil.
	HomeDir func() (string, error)
}

// DefaultProviderAuthContext is the default auth context: env vars from the
// process environment, file existence via the filesystem.
//
// Ports `defaultProviderAuthContext` from packages/ai/src/auth/context.ts.
func DefaultProviderAuthContext() authtypes.AuthContext {
	return &EnvAuthContext{}
}

// Env reads an environment variable, treating whitespace-only values as unset.
func (c *EnvAuthContext) Env(name string) (*string, error) {
	lookup := c.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	value, ok := lookup(name)
	if !ok || strings.TrimSpace(value) == "" {
		return nil, nil
	}
	return &value, nil
}

// FileExists reports whether path names an existing file. A leading `~` is
// expanded to the user home directory.
func (c *EnvAuthContext) FileExists(path string) (bool, error) {
	stat := c.Stat
	if stat == nil {
		stat = os.Stat
	}
	resolved := path
	if strings.HasPrefix(resolved, "~") {
		home, err := c.homeDir()
		if err != nil {
			return false, nil
		}
		resolved = filepath.Join(home, strings.TrimPrefix(resolved, "~"))
	}
	if _, err := stat(resolved); err != nil {
		return false, nil
	}
	return true, nil
}

func (c *EnvAuthContext) homeDir() (string, error) {
	if c.HomeDir != nil {
		return c.HomeDir()
	}
	return os.UserHomeDir()
}
