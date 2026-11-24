package terminal

import (
	"fmt"
	"os"
	"path/filepath"
)

func (b *initialCommandBootstrap) poshStartup(env []string) ([]string, error) {
	home, err := startupHomeDirectory(env)
	if err != nil {
		return nil, fmt.Errorf("find the posh home directory: %w", err)
	}
	path := filepath.Join(b.dir, ".profile")
	if err := os.WriteFile(path, []byte(poshInitialStartup), 0o600); err != nil {
		return nil, fmt.Errorf("write the posh startup profile: %w", err)
	}
	return append(env, "HOME="+b.dir, "LEAPMUX_INITIAL_COMMAND_POSH_HOME="+home), nil
}

// A posh login reads the system profile and then HOME/.profile. Its login
// startup has no ENV hook, so the private login file restores the original
// home before reading the personal profile.
const poshInitialStartup = `
HOME=$LEAPMUX_INITIAL_COMMAND_POSH_HOME
export HOME
unset LEAPMUX_INITIAL_COMMAND_POSH_HOME
` + posixPersonalLoginProfile + posixInitialHandoff
