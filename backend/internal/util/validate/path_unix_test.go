//go:build unix

package validate

import "testing"

func TestSanitizePath_Unix(t *testing.T) {
	cases := []sanitizeCase{
		// Absolute paths (homeDir irrelevant).
		{name: "absolute path", input: "/home/user", want: "/home/user"},
		{name: "absolute macOS", input: "/Users/john", want: "/Users/john"},
		{name: "root path", input: "/", want: "/"},

		// Tilde expansion with homeDir.
		{name: "tilde alone", input: "~", homeDir: "/home/user", want: "/home/user"},
		{name: "tilde with slash", input: "~/", homeDir: "/home/user", want: "/home/user"},
		{name: "tilde subdir", input: "~/projects", homeDir: "/home/user", want: "/home/user/projects"},
		{name: "tilde nested", input: "~/projects/myapp", homeDir: "/home/user", want: "/home/user/projects/myapp"},
		{name: "tilde trailing slash", input: "~/projects/", homeDir: "/home/user", want: "/home/user/projects"},
		{name: "tilde double slashes", input: "~//projects", homeDir: "/home/user", want: "/home/user/projects"},
		{name: "tilde dot component", input: "~/./projects", homeDir: "/home/user", want: "/home/user/projects"},

		// Tilde rejected without homeDir.
		{name: "tilde no homeDir", input: "~", wantErr: ErrNoHomeDir},
		{name: "tilde subdir no homeDir", input: "~/projects", wantErr: ErrNoHomeDir},

		// Relative paths.
		{name: "relative path", input: "home/user", wantErr: ErrNotAbsolute},
		{name: "dot-relative", input: "./foo", wantErr: ErrNotAbsolute},
		{name: "bare name", input: "foo", wantErr: ErrNotAbsolute},

		// Windows absolute paths must be rejected on POSIX.
		{name: "windows drive rejected", input: `C:\Users\u`, wantErr: ErrNotAbsolute},

		// Path traversal.
		{name: "traversal mid", input: "/home/../etc/passwd", wantErr: ErrTraversal},
		{name: "traversal end", input: "/home/user/..", wantErr: ErrTraversal},
		{name: "traversal only", input: "/..", wantErr: ErrTraversal},
		{name: "tilde traversal", input: "~/../etc/passwd", homeDir: "/home/user", wantErr: ErrTraversal},

		// Control character stripping.
		{name: "control chars stripped", input: "/home/\x01user", want: "/home/user"},
		{name: "DEL stripped", input: "/home/\x7Fuser", want: "/home/user"},
		{name: "tilde control chars", input: "~/\x01projects", homeDir: "/home/user", want: "/home/user/projects"},
		{name: "dollar preserved", input: "/home/$USER", want: "/home/$USER"},
		{name: "percent preserved", input: "/home/%user", want: "/home/%user"},

		// Normalization.
		{name: "trailing slash", input: "/home/user/", want: "/home/user"},
		{name: "double slashes", input: "/home//user", want: "/home/user"},
		{name: "dot components", input: "/home/./user", want: "/home/user"},
		{name: "whitespace trimmed", input: "  /home/user  ", want: "/home/user"},
	}
	runSanitizeCases(t, cases)
}
