package agent

import (
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// piResumeHandleFixture mirrors testdata/pi_resume_handle_conformance.json.
type piResumeHandleFixture struct {
	HomeDir struct {
		Posix   string `json:"posix"`
		Windows string `json:"windows"`
	} `json:"homeDir"`
	Cases []piResumeHandleCase `json:"cases"`
}

type piResumeHandleCase struct {
	Input   piResumeHandleInput `json:"input"`
	Browser piResumeHandleVerdict
	Worker  struct {
		Posix   piResumeHandleVerdict `json:"posix"`
		Windows piResumeHandleVerdict `json:"windows"`
	} `json:"worker"`
	Why string `json:"why"`
}

type piResumeHandleVerdict struct {
	Valid   bool   `json:"valid"`
	Refusal string `json:"refusal"`
}

// piResumeHandleInput is built as head + text*repeat + tail, so an over-cap
// case stays short in the JSON.
type piResumeHandleInput struct {
	Head   string `json:"head"`
	Text   string `json:"text"`
	Repeat int    `json:"repeat"`
	Tail   string `json:"tail"`
}

func (s piResumeHandleInput) build() string {
	repeat := s.Repeat
	if repeat == 0 {
		repeat = 1
	}
	return s.Head + strings.Repeat(s.Text, repeat) + s.Tail
}

// piResumeHandleRefusalMarkers maps a fixture refusal token to a substring of
// THIS side's message for that rule. The browser carries its own map, so the
// fixture stays language-neutral.
var piResumeHandleRefusalMarkers = map[string]string{
	"too_long":            "must be at most",
	"not_absolute":        "path must be absolute",
	"traversal":           "path traversal not allowed",
	"leading_hyphen":      "must not start with a hyphen",
	"forbidden_character": "contains invalid characters",
	"invisible_character": "contains invisible characters",
	"whitespace_at_edge":  "must not start or end with whitespace",
}

// TestPiResumeHandleConformance is the worker half of
// testdata/pi_resume_handle_conformance.json. The browser suite
// (`piValidateResumeHandle conformance`, in the Pi plugin's own test file)
// reads the same file, so a one-sided edit to either implementation turns that
// side red.
//
// The verdict is per HOST, because `filepath.IsAbs` is: a POSIX session path is
// absolute on POSIX and relative on Windows, and a drive path is the reverse.
// This reads the column for runtime.GOOS.
func TestPiResumeHandleConformance(t *testing.T) {
	t.Parallel()

	const path = "../../../../testdata/pi_resume_handle_conformance.json"
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "read %s", path)

	var fixture piResumeHandleFixture
	require.NoError(t, json.Unmarshal(raw, &fixture))
	// A fixture that silently loads zero cases would make this test pass while
	// asserting nothing -- the one failure mode a shared fixture must not have.
	require.NotEmpty(t, fixture.Cases)

	homeDir := fixture.HomeDir.Posix
	if runtime.GOOS == "windows" {
		homeDir = fixture.HomeDir.Windows
	}

	pi := piProvider{}
	for _, c := range fixture.Cases {
		t.Run(c.Why, func(t *testing.T) {
			want := c.Worker.Posix
			if runtime.GOOS == "windows" {
				want = c.Worker.Windows
			}
			handle := c.Input.build()
			err := resumeHandleErr(pi, handle, homeDir)
			if want.Valid {
				assert.Emptyf(t, want.Refusal, "case %q is valid, so its refusal must be empty", c.Why)
				assert.NoErrorf(t, err, "case %q must be accepted on %s", c.Why, runtime.GOOS)
				return
			}
			require.Errorf(t, err, "case %q must be refused on %s", c.Why, runtime.GOOS)
			marker, ok := piResumeHandleRefusalMarkers[want.Refusal]
			require.Truef(t, ok, "case %q carries an unknown refusal token %q", c.Why, want.Refusal)
			assert.Containsf(t, err.Error(), marker, "case %q must report the %s rule", c.Why, want.Refusal)
		})
	}
}

// TestPiResumeHandleBrowserNeverRefusesMoreThanTheWorker pins the asymmetry the
// fixture is allowed to hold, in the ONE direction that is safe.
//
// The browser may accept a handle the worker refuses: it does not know the
// worker's host, so it leaves the reserved device names and the spelling of
// "absolute" to the side that does, and the worker reports the refusal. The
// reverse is the failure this whole rule exists to remove -- a value the field
// refuses never reaches a worker to be judged, so a browser refusal that no
// worker shares removes a legitimate resume with no way to reach it.
func TestPiResumeHandleBrowserNeverRefusesMoreThanTheWorker(t *testing.T) {
	t.Parallel()

	const path = "../../../../testdata/pi_resume_handle_conformance.json"
	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var fixture piResumeHandleFixture
	require.NoError(t, json.Unmarshal(raw, &fixture))
	require.NotEmpty(t, fixture.Cases)

	for _, c := range fixture.Cases {
		if c.Browser.Valid {
			continue
		}
		assert.Falsef(t, c.Worker.Posix.Valid,
			"case %q: the browser refuses it, so no worker may accept it", c.Why)
		assert.Falsef(t, c.Worker.Windows.Valid,
			"case %q: the browser refuses it, so no worker may accept it", c.Why)
	}
}
