//go:build windows

package validate

import "testing"

func TestSanitizePath_Windows(t *testing.T) {
	cases := []sanitizeCase{
		// Drive-letter absolute paths.
		{name: "drive upper", input: `C:\Users\u`, want: `C:\Users\u`},
		{name: "drive lower", input: `c:\Users\u`, want: `c:\Users\u`},
		{name: "drive forward slashes", input: `C:/Users/u`, want: `C:\Users\u`},
		{name: "drive root", input: `C:\`, want: `C:\`},

		// UNC and extended-length.
		{name: "unc share", input: `\\srv\share\p`, want: `\\srv\share\p`},
		{name: "unc share root", input: `\\srv\share`, want: `\\srv\share`},
		{name: "extended length", input: `\\?\C:\p`, want: `\\?\C:\p`},

		// Tilde expansion.
		{name: "tilde backslash", input: `~\foo`, homeDir: `C:\H\U`, want: `C:\H\U\foo`},
		{name: "tilde forward", input: `~/foo`, homeDir: `C:\H\U`, want: `C:\H\U\foo`},
		{name: "tilde alone", input: `~`, homeDir: `C:\H\U`, want: `C:\H\U`},
		{name: "tilde nested forward", input: `~/a/b`, homeDir: `C:\H\U`, want: `C:\H\U\a\b`},

		// POSIX-style absolute path rejected on Windows (no drive / UNC prefix).
		{name: "posix absolute rejected", input: `/foo`, wantErr: ErrNotAbsolute},
		{name: "relative back", input: `foo\bar`, wantErr: ErrNotAbsolute},

		// Traversal.
		{name: "traversal bs", input: `C:\a\..\b`, wantErr: ErrTraversal},
		{name: "traversal fs", input: `C:/a/../b`, wantErr: ErrTraversal},
		{name: "traversal mixed", input: `C:\a/..\b`, wantErr: ErrTraversal},

		// Control chars stripped.
		{name: "control chars stripped", input: "C:\\a\x01b", want: `C:\ab`},

		// Reserved device names.
		{name: "reserved nul bare", input: `C:\NUL`, wantErr: ErrReservedName},
		{name: "reserved nul lower", input: `C:\nul`, wantErr: ErrReservedName},
		{name: "reserved con nested", input: `C:\temp\CON`, wantErr: ErrReservedName},
		{name: "reserved nul with ext", input: `C:\foo\NUL.txt`, wantErr: ErrReservedName},
		{name: "reserved aux trailing", input: `C:\aux .log`, wantErr: ErrReservedName},
		{name: "reserved com1", input: `C:\COM1`, wantErr: ErrReservedName},
		{name: "reserved lpt9", input: `C:\LPT9`, wantErr: ErrReservedName},
		{name: "reserved device namespace", input: `\\.\NUL`, wantErr: ErrReservedName},

		// Allowed: supersets and lookalikes of reserved names.
		{name: "allowed null directory", input: `C:\NULL\x`, want: `C:\NULL\x`},
		{name: "allowed console", input: `C:\console\x`, want: `C:\console\x`},
		{name: "allowed com10", input: `C:\COM10\x`, want: `C:\COM10\x`},
		// Extended-length namespace bypasses the reserved-name check.
		{name: "extended ns allows nul", input: `\\?\C:\NUL`, want: `\\?\C:\NUL`},

		// Reserved characters (outside the volume name).
		{name: "reserved char lt", input: `C:\a<b`, wantErr: ErrReservedChar},
		{name: "reserved char gt", input: `C:\a>b`, wantErr: ErrReservedChar},
		{name: "reserved char quote", input: `C:\a"b`, wantErr: ErrReservedChar},
		{name: "reserved char pipe", input: `C:\a|b`, wantErr: ErrReservedChar},
		{name: "reserved char qmark", input: `C:\a?b`, wantErr: ErrReservedChar},
		{name: "reserved char star", input: `C:\a*b`, wantErr: ErrReservedChar},
		{name: "reserved colon in comp", input: `C:\foo\bar:baz`, wantErr: ErrReservedChar},
	}
	runSanitizeCases(t, cases)
}
