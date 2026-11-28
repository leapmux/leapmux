package terminal

import "fmt"

func (b *initialCommandBootstrap) poshStartup(env []string) ([]string, error) {
	home, err := startupHomeDirectory(env)
	if err != nil {
		return nil, fmt.Errorf("find the posh home directory: %w", err)
	}
	if _, err := b.writeFile(".profile", poshInitialStartup, "posh startup profile"); err != nil {
		return nil, err
	}
	return append(env, "HOME="+b.dir, initialCommandEnvPoshHome+"="+home), nil
}

// A posh login reads the system profile and then HOME/.profile. Its login
// startup has no ENV hook, so the private login file restores the original
// home before reading the personal profile.
const poshInitialStartup = `
HOME=$LEAPMUX_INITIAL_COMMAND_POSH_HOME
export HOME
unset LEAPMUX_INITIAL_COMMAND_POSH_HOME
` + posixPersonalLoginProfile + posixInitialHandoff
