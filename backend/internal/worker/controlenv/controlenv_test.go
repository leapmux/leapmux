package controlenv

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

// unixPathList and windowsPathList let one host test the PATH rules of both
// operating systems.
var (
	unixPathList    = pathList{separator: ":", foldCase: false}
	windowsPathList = pathList{separator: ";", foldCase: true}
)

// absoluteDir returns a synthetic host-native absolute directory without
// creating filesystem entries.
func absoluteDir(_ *testing.T, parts ...string) string {
	return testutil.NativeAbsPath("/" + strings.Join(parts, "/"))
}

func TestApply(t *testing.T) {
	t.Parallel()

	t.Run("puts the CLI directory first on an existing PATH", func(t *testing.T) {
		t.Parallel()
		cliDir := absoluteDir(t, "cli-1")
		control := []string{"LEAPMUX_CONTROL_SOCK=unix:/s", CLIVariable + "=" + filepath.Join(cliDir, "leapmux")}

		env := Apply([]string{"PATH=/usr/bin" + hostPathList.separator + "/bin", "HOME=/home/u"}, control)

		assert.Equal(t, []string{cliDir + hostPathList.separator + "/usr/bin" + hostPathList.separator + "/bin"}, envutil.ValuesFor(env, "PATH"),
			"the directory comes first, and the PATH that the process would get otherwise follows it unchanged")
		assert.Contains(t, env, "HOME=/home/u")
		assert.Equal(t, control, env[len(env)-len(control):], "the fresh variables come last, in their order")
	})

	t.Run("gives a spawn without remote control no entry and no variable", func(t *testing.T) {
		t.Parallel()
		env := Apply([]string{"PATH=/usr/bin", "HOME=/home/u"}, nil)

		assert.Equal(t, []string{"PATH=/usr/bin", "HOME=/home/u"}, env)
		assert.False(t, envutil.HasKey(env, CLIVariable))
	})

	t.Run("strips every inherited control variable even without fresh ones", func(t *testing.T) {
		t.Parallel()
		env := Apply([]string{"LEAPMUX_CONTROL_SOCK=unix:/parent", "LEAPMUX_CONTROL_TAB_ID=parent-tab", "PATH=/usr/bin"}, nil)

		assert.Equal(t, []string{"PATH=/usr/bin"}, env,
			"a process the Worker starts must not act as the tab of a parent Worker")
	})

	t.Run("replaces the inherited context with the fresh one", func(t *testing.T) {
		t.Parallel()
		parentDir := absoluteDir(t, "parent")
		cliDir := absoluteDir(t, "child")
		sep := hostPathList.separator
		environ := []string{
			"PATH=" + parentDir + sep + "/usr/bin",
			CLIVariable + "=" + filepath.Join(parentDir, "leapmux"),
			"LEAPMUX_CONTROL_SOCK=unix:/parent",
		}

		env := Apply(environ, []string{"LEAPMUX_CONTROL_SOCK=unix:/child", CLIVariable + "=" + filepath.Join(cliDir, "leapmux")})

		assert.Equal(t, []string{cliDir + sep + "/usr/bin"}, envutil.ValuesFor(env, "PATH"),
			"the parent's directory leaves PATH with its variable, and the fresh one comes first")
		assert.Equal(t, []string{"unix:/child"}, envutil.ValuesFor(env, "LEAPMUX_CONTROL_SOCK"))
		assert.Equal(t, []string{filepath.Join(cliDir, "leapmux")}, envutil.ValuesFor(env, CLIVariable))
	})

	t.Run("removes the inherited directory from a spawn without remote control", func(t *testing.T) {
		t.Parallel()
		parentDir := absoluteDir(t, "parent")
		sep := hostPathList.separator

		env := Apply([]string{"PATH=" + parentDir + sep + "/usr/bin", CLIVariable + "=" + filepath.Join(parentDir, "leapmux")}, nil)

		assert.Equal(t, []string{"PATH=/usr/bin"}, env,
			"the link of the parent Worker is part of the parent's context, so it goes with it")
	})

	t.Run("is idempotent", func(t *testing.T) {
		t.Parallel()
		cliDir := absoluteDir(t, "cli-1")
		control := []string{CLIVariable + "=" + filepath.Join(cliDir, "leapmux")}

		once := Apply([]string{"PATH=/usr/bin"}, control)
		twice := Apply(once, control)

		assert.Equal(t, once, twice, "the directory appears once, however often a path applies the context")
	})

	t.Run("leaves an unset PATH unset", func(t *testing.T) {
		t.Parallel()
		control := []string{CLIVariable + "=" + filepath.Join(absoluteDir(t, "cli-1"), "leapmux")}

		env := Apply([]string{"HOME=/home/u"}, control)

		assert.False(t, envutil.HasKey(env, "PATH"),
			"a PATH that held only the directory would replace the default search path")
		assert.Equal(t, append([]string{"HOME=/home/u"}, control...), env, "the variable still gives the executable")
	})

	t.Run("keeps the empty entry of an empty PATH", func(t *testing.T) {
		t.Parallel()
		cliDir := absoluteDir(t, "cli-1")

		env := Apply([]string{"PATH="}, []string{CLIVariable + "=" + filepath.Join(cliDir, "leapmux")})

		assert.Equal(t, []string{cliDir + hostPathList.separator}, envutil.ValuesFor(env, "PATH"),
			"a shell reads an empty PATH as the working directory, and the prepend must not change that")
	})

	t.Run("ignores a relative CLI path", func(t *testing.T) {
		t.Parallel()
		env := Apply([]string{"PATH=/usr/bin"}, []string{CLIVariable + "=leapmux"})

		assert.Equal(t, []string{"/usr/bin"}, envutil.ValuesFor(env, "PATH"),
			"a relative directory would resolve against the working directory of each command")
	})

	t.Run("never changes its arguments", func(t *testing.T) {
		t.Parallel()
		cliDir := absoluteDir(t, "cli-1")
		environ := []string{"LEAPMUX_CONTROL_SOCK=unix:/parent", "PATH=/usr/bin"}
		control := []string{CLIVariable + "=" + filepath.Join(cliDir, "leapmux")}
		environCopy, controlCopy := slices.Clone(environ), slices.Clone(control)

		_ = Apply(environ[:1:1], control)
		_ = Apply(environ, control)

		assert.Equal(t, environCopy, environ)
		assert.Equal(t, controlCopy, control)
	})
}

func TestPathListEditPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		style   pathList
		env     []string
		remove  string
		prepend string
		want    []string
	}{
		{
			name:    "prepends to a Unix PATH",
			style:   unixPathList,
			env:     []string{"A=1", "PATH=/usr/bin:/bin", "B=2"},
			prepend: "/run/cli-1",
			want:    []string{"A=1", "PATH=/run/cli-1:/usr/bin:/bin", "B=2"},
		},
		{
			name:    "prepends with the Windows separator and keeps the Windows spelling of the name",
			style:   windowsPathList,
			env:     []string{"Path=C:\\Windows;C:\\Tools"},
			prepend: `C:\Data\run\cli-1`,
			want:    []string{`Path=C:\Data\run\cli-1;C:\Windows;C:\Tools`},
		},
		{
			name:    "reads a variable name and a path regardless of case on Windows",
			style:   windowsPathList,
			env:     []string{`PATH=C:\Old\CLI-0;C:\Windows`},
			remove:  `c:\old\cli-0`,
			prepend: `C:\Data\run\cli-1`,
			want:    []string{`PATH=C:\Data\run\cli-1;C:\Windows`},
		},
		{
			name:    "matches the name case-sensitively on Unix",
			style:   unixPathList,
			env:     []string{"Path=/usr/bin"},
			prepend: "/run/cli-1",
			want:    []string{"Path=/usr/bin"},
		},
		{
			name:    "leaves an unset PATH unset",
			style:   unixPathList,
			env:     []string{"HOME=/home/u"},
			prepend: "/run/cli-1",
			want:    []string{"HOME=/home/u"},
		},
		{
			name:    "keeps the empty entry of an empty PATH",
			style:   unixPathList,
			env:     []string{"PATH="},
			prepend: "/run/cli-1",
			want:    []string{"PATH=/run/cli-1:"},
		},
		{
			name:    "keeps every empty entry in the middle of PATH",
			style:   unixPathList,
			env:     []string{"PATH=/usr/bin::/bin"},
			prepend: "/run/cli-1",
			want:    []string{"PATH=/run/cli-1:/usr/bin::/bin"},
		},
		{
			name:    "edits the last of duplicate entries and drops the earlier ones",
			style:   unixPathList,
			env:     []string{"PATH=/shadowed", "A=1", "PATH=/usr/bin"},
			prepend: "/run/cli-1",
			want:    []string{"A=1", "PATH=/run/cli-1:/usr/bin"},
		},
		{
			name:    "moves the directory to the front instead of adding it twice",
			style:   unixPathList,
			env:     []string{"PATH=/usr/bin:/run/cli-1:/bin"},
			prepend: "/run/cli-1",
			want:    []string{"PATH=/run/cli-1:/usr/bin:/bin"},
		},
		{
			name:   "removes every entry of the inherited directory",
			style:  unixPathList,
			env:    []string{"PATH=/run/cli-0:/usr/bin:/run/cli-0"},
			remove: "/run/cli-0",
			want:   []string{"PATH=/usr/bin"},
		},
		{
			name:   "keeps the inherited directory when it is the only entry",
			style:  unixPathList,
			env:    []string{"PATH=/run/cli-0"},
			remove: "/run/cli-0",
			want:   []string{"PATH=/run/cli-0"},
		},
		{
			name:    "replaces an inherited directory that is the only entry",
			style:   unixPathList,
			env:     []string{"PATH=/run/cli-0"},
			remove:  "/run/cli-0",
			prepend: "/run/cli-1",
			want:    []string{"PATH=/run/cli-1"},
		},
		{
			name:    "leaves PATH alone for a directory that holds the separator",
			style:   unixPathList,
			env:     []string{"PATH=/usr/bin"},
			prepend: "/odd:dir/cli-1",
			want:    []string{"PATH=/usr/bin"},
		},
		{
			name:    "leaves PATH alone for a Windows directory that holds the separator",
			style:   windowsPathList,
			env:     []string{`Path=C:\Windows`},
			prepend: `C:\odd;dir\cli-1`,
			want:    []string{`Path=C:\Windows`},
		},
		{
			name:  "returns env unchanged with nothing to do",
			style: unixPathList,
			env:   []string{"PATH=/a", "PATH=/b"},
			want:  []string{"PATH=/a", "PATH=/b"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			original := slices.Clone(tt.env)

			got := tt.style.editPath(tt.env, tt.remove, tt.prepend)

			assert.Equal(t, tt.want, got)
			assert.Equal(t, original, tt.env, "editPath never edits its argument")
		})
	}
}

// A long PATH, as a developer machine carries, keeps every entry and its order.
func TestPathListEditPathKeepsALongPath(t *testing.T) {
	t.Parallel()
	entries := make([]string, 2000)
	for i := range entries {
		entries[i] = "/opt/tool-" + strings.Repeat("x", i%7) + "/bin"
	}

	got := unixPathList.editPath([]string{"PATH=" + strings.Join(entries, ":")}, "", "/run/cli-1")

	require.Len(t, got, 1)
	assert.Equal(t, "PATH=/run/cli-1:"+strings.Join(entries, ":"), got[0])
}

func TestCLIDirectory(t *testing.T) {
	t.Parallel()
	dir := absoluteDir(t, "cli-1")

	assert.Equal(t, dir, unixPathList.cliDirectory([]string{CLIVariable + "=" + filepath.Join(dir, "leapmux")}))
	assert.Empty(t, unixPathList.cliDirectory(nil), "no variable gives no directory")
	assert.Empty(t, unixPathList.cliDirectory([]string{CLIVariable + "="}), "an empty value gives no directory")
	assert.Empty(t, unixPathList.cliDirectory([]string{CLIVariable + "=leapmux"}), "a relative value gives no directory")
	assert.Empty(t, unixPathList.cliDirectory([]string{"leapmux_control_cli=" + filepath.Join(dir, "leapmux")}),
		"on Unix the name matches case-sensitively, as the prefix strip does")
	assert.Equal(t, dir, windowsPathList.cliDirectory([]string{"leapmux_control_cli=" + filepath.Join(dir, "leapmux.exe")}),
		"on Windows the name matches case-insensitively")
	assert.Equal(t, dir, unixPathList.cliDirectory([]string{
		CLIVariable + "=" + filepath.Join(absoluteDir(t, "older"), "leapmux"),
		CLIVariable + "=" + filepath.Join(dir, "leapmux"),
	}), "the last value wins, as it does for exec")
}

func TestApply_WindowsCaseFoldAndPathNormalization(t *testing.T) {
	t.Parallel()
	oldDir := absoluteDir(t, "cli-old")
	newDir := absoluteDir(t, "cli-new")
	environ := []string{
		"leapmux_control_token=secret",
		"LeapMux_Control_CLI=" + filepath.Join(oldDir, "leapmux.exe"),
		`Path="` + oldDir + `\";C:\Windows\System32`,
	}
	control := []string{
		CLIVariable + "=" + filepath.Join(newDir, "leapmux.exe"),
	}

	got := apply(environ, control, windowsPathList)
	assert.False(t, envutil.HasKey(got, "leapmux_control_token"), "mixed-case inherited control var must be stripped on Windows")
	assert.Equal(t, []string{newDir + `;C:\Windows\System32`}, envutil.ValuesFor(got, "Path"),
		"quoted and trailing-slash inherited CLI directory must be removed from Windows PATH")
}
