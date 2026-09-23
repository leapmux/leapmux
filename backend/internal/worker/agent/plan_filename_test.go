package agent

import (
	"strings"
	"testing"

	"github.com/leapmux/leapmux/util/validate"
	"github.com/stretchr/testify/assert"
)

func TestSanitizePlanFilenameTitle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		title string
		want  string
	}{
		{
			name:  "lowercases ASCII and joins with hyphens",
			title: "Add Login Feature",
			want:  "add-login-feature",
		},
		{
			name:  "drops filesystem-reserved characters",
			title: `A/B\C:D*E?F"G<H>I|J`,
			want:  "abcdefghij",
		},
		{
			name:  "drops punctuation without inserting separators",
			title: "user's plan v2.0",
			want:  "users-plan-v20",
		},
		{
			name:  "preserves existing hyphens",
			title: "well-known issue",
			want:  "well-known-issue",
		},
		{
			name:  "collapses runs of hyphens and spaces",
			title: "Plan -- foo   bar",
			want:  "plan-foo-bar",
		},
		{
			name:  "trims leading and trailing separators",
			title: "  !!! Plan Name.  ",
			want:  "plan-name",
		},
		{
			name:  "trims leading and trailing hyphens",
			title: "---plan---",
			want:  "plan",
		},
		{
			name:  "trims mixed leading and trailing punctuation and hyphens",
			title: "-!- plan -!-",
			want:  "plan",
		},
		{
			name:  "falls back when empty",
			title: " \t\r\n ",
			want:  "untitled-plan",
		},
		{
			name:  "falls back when only special characters",
			title: "!@#$%^&*()",
			want:  "untitled-plan",
		},
		{
			name:  "preserves CJK letters (no case to fold)",
			title: "설계 문서 渲染修复",
			want:  "설계-문서-渲染修复",
		},
		{
			name:  "lowercases non-ASCII letters where applicable",
			title: "ÄPFEL Über",
			want:  "äpfel-über",
		},
		{
			name:  "strips control characters",
			title: "Plan\t\x00  Name\n\r",
			want:  "plan-name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SanitizePlanFilenameTitle(tt.title))
		})
	}
}

// A plan stem must never resolve to a DOS device.
//
// `writePlanFile` joins this stem with ".md" and opens it directly, and Windows
// resolves a device name in any directory and with any extension -- so `con.md`
// reaches the console device rather than a file and the plan is lost. The retry
// suffix does not help either: `con.2.md` still reduces to CON.
func TestSanitizePlanFilenameTitleAvoidsWindowsDeviceNames(t *testing.T) {
	t.Parallel()

	for _, title := range []string{"CON", "Aux", "prn", "NUL", "COM1", "lpt9"} {
		t.Run(title, func(t *testing.T) {
			t.Parallel()
			stem := SanitizePlanFilenameTitle(title)
			assert.Falsef(t, validate.IsReservedDeviceName(stem),
				"%q produced the reserved stem %q", title, stem)
			// Still recognisable: the title is kept and only disambiguated.
			assert.Containsf(t, stem, strings.ToLower(title), "%q lost its text", title)
		})
	}
}

// An ordinary title keeps its stem exactly. The device guard must not rewrite a
// name that was never a device.
func TestSanitizePlanFilenameTitleLeavesAnOrdinaryTitleAlone(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "control-flow", SanitizePlanFilenameTitle("Control Flow"))
	// "console" merely STARTS with a device name; only the whole stem counts.
	assert.Equal(t, "console", SanitizePlanFilenameTitle("Console"))
}
