package terminal

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const elvishStartupFile = "elvish-startup.elv"
const elvishStartupLinkRecord = "elvish-startup-link"
const elvishStartupLinkPrefix = ".leapmux-elvish-startup-"

func elvishProfilePath(workingDir string, env []string) (string, error) {
	configHome, _ := lookupLaunchEnv(env, "XDG_CONFIG_HOME")
	if configHome == "" {
		home, _ := lookupLaunchEnv(env, "HOME")
		if home == "" {
			current, err := user.Current()
			if err != nil {
				return "", fmt.Errorf("find the Elvish home directory: %w", err)
			}
			home = current.HomeDir
		}
		configHome = filepath.Join(strings.TrimRight(home, "/"), ".config")
	}
	profile := filepath.Join(configHome, "elvish", "rc.elv")
	if !filepath.IsAbs(profile) {
		profile = filepath.Join(workingDir, profile)
	}
	return filepath.Abs(profile)
}

// Elvish accepts one RC source. The profile and the hook run in the same
// chunk so the editor, its functions, and the REPL share the same bindings.
// Elvish resolves relative imports from the link's directory, where it would
// have loaded the profile. The profile itself is never opened for writing.
func (b *initialCommandBootstrap) elvishStartupSource(workingDir string, env []string) (string, error) {
	profile, err := elvishProfilePath(workingDir, env)
	if err != nil {
		return "", err
	}
	var code []byte
	info, err := os.Stat(profile)
	if err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("the Elvish profile %q is not a regular file", profile)
		}
		code, err = os.ReadFile(profile)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read the Elvish profile: %w", err)
	}
	if !utf8.Valid(code) {
		return "", fmt.Errorf("the Elvish profile %q is not valid UTF-8", profile)
	}
	source := filepath.Join(b.dir, elvishStartupFile)
	next := append(code, elvishInitialCommand...)
	if err := os.WriteFile(source, next, 0o600); err != nil {
		return "", fmt.Errorf("write the Elvish startup source: %w", err)
	}
	if errors.Is(err, os.ErrNotExist) {
		return source, nil
	}

	// Record the path before creating the link so a restarted Worker can
	// remove it even if the original Worker stopped during preparation.
	link := filepath.Join(filepath.Dir(profile), elvishStartupLinkPrefix+b.nonce+".elv")
	if err := os.WriteFile(filepath.Join(b.dir, elvishStartupLinkRecord), []byte(link), 0o600); err != nil {
		return "", fmt.Errorf("record the Elvish startup source: %w", err)
	}
	if err := os.Symlink(source, link); err != nil {
		return "", fmt.Errorf("create an Elvish startup link beside its profile: %w", err)
	}
	return link, nil
}

func removeElvishStartupLink(dir string) error {
	record := filepath.Join(dir, elvishStartupLinkRecord)
	info, err := os.Lstat(record)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	linkBytes, err := os.ReadFile(record)
	if err != nil {
		return err
	}
	link := string(linkBytes)
	if !filepath.IsAbs(link) || !strings.HasPrefix(filepath.Base(link), elvishStartupLinkPrefix) {
		return nil
	}
	info, err = os.Lstat(link)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	target, err := os.Readlink(link)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	expected, err := filepath.Abs(filepath.Join(dir, elvishStartupFile))
	if err != nil {
		return err
	}
	if target != expected {
		return nil
	}
	if err := os.Remove(link); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove the Elvish startup link: %w", err)
	}
	return nil
}

// Elvish evaluates the submitted editor buffer in the interactive scope.
// The hook reads the complete command before it acknowledges the release.
// Remove both one-shot callbacks by identity so existing hooks keep their order.
const elvishInitialCommand = `

if $true {
  use builtin leapmux-startup-builtin
  use os leapmux-startup-os
  var submit = $nil
  set submit = {
    set edit:before-readline = [(for hook $edit:before-readline {
      if (not (leapmux-startup-builtin:eq $hook $submit)) { leapmux-startup-builtin:put $hook }
    })]
    if (not (leapmux-startup-builtin:eq $leapmux-startup-builtin:pid $E:LEAPMUX_INITIAL_COMMAND_PID)) {
      leapmux-startup-builtin:fail 'Elvish startup did not reach the original shell'
    }
    var ready = $E:LEAPMUX_INITIAL_COMMAND_READY
    var source = $E:LEAPMUX_INITIAL_COMMAND_FILE
    var nonce = $E:LEAPMUX_INITIAL_COMMAND_NONCE
    leapmux-startup-builtin:echo $leapmux-startup-builtin:pid $nonce > $ready
    var waits = (leapmux-startup-builtin:num 0)
    while (and (not (leapmux-startup-os:is-regular $source)) (leapmux-startup-os:is-regular $ready) (leapmux-startup-builtin:< $waits 1500)) {
      leapmux-startup-builtin:sleep 20ms
      set waits = (leapmux-startup-builtin:+ $waits 1)
    }
    if (leapmux-startup-os:is-regular $source) {
      var code = (leapmux-startup-builtin:slurp < $source)
      var acknowledgment = $E:LEAPMUX_INITIAL_COMMAND_ACK
      for key [LEAPMUX_INITIAL_COMMAND_FILE LEAPMUX_INITIAL_COMMAND_READY LEAPMUX_INITIAL_COMMAND_ACK LEAPMUX_INITIAL_COMMAND_NONCE LEAPMUX_INITIAL_COMMAND_PID LEAPMUX_INITIAL_COMMAND_PARENT] {
        leapmux-startup-builtin:unset-env $key
      }
      var filter = $nil
      set filter = {|line|
        set edit:add-cmd-filters = [(for hook $edit:add-cmd-filters {
          if (not (leapmux-startup-builtin:eq $hook $filter)) { leapmux-startup-builtin:put $hook }
        })]
        leapmux-startup-builtin:put (not (leapmux-startup-builtin:eq $line $code))
      }
      set edit:add-cmd-filters = [$filter $@edit:add-cmd-filters]
      set edit:current-command = $code
      leapmux-startup-builtin:echo $leapmux-startup-builtin:pid $nonce > $acknowledgment
      edit:return-line
    }
  }
  set edit:before-readline = [$@edit:before-readline $submit]
}
`
