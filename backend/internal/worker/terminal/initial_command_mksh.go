package terminal

func (b *initialCommandBootstrap) mkshStartup(env []string) ([]string, error) {
	source, err := b.writeFile("mksh-interactive", mkshInitialStartup, "mksh startup hook")
	if err != nil {
		return nil, err
	}
	// mksh uses the value of ENV as the path. An indirection through a second
	// variable would make it try to open the variable expression itself.
	return append(saveOriginalStartupEnvironment(env), "ENV="+source), nil
}

// mksh reads its default rc file when ENV is unset or empty. It opens a
// relative startup path from the working directory, so keep the same lookup
// when sourcing the file explicitly.
const mkshInitialStartup = restoreStartupEnvironment + posixLoginProfiles + `
__leapmux_mksh_profile=${ENV:-${HOME:+$HOME/.mkshrc}}
if [ -n "$__leapmux_mksh_profile" ]; then
  case "$__leapmux_mksh_profile" in
    /*) ;;
    *) __leapmux_mksh_profile=./$__leapmux_mksh_profile ;;
  esac
  if [ -r "$__leapmux_mksh_profile" ]; then
    . "$__leapmux_mksh_profile"
  fi
fi
unset __leapmux_mksh_profile
` + posixInitialHandoff
