package reasonix

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReasonixOutputReceiptConformance(t *testing.T) {
	t.Parallel()
	var fixture struct {
		Cases []struct {
			Name       string `json:"name"`
			Text       string `json:"text"`
			Normalized string `json:"normalized"`
			Tool       string `json:"tool"`
			CallID     string `json:"callId"`
			Path       string `json:"path"`
			Headline   string `json:"headline"`
			FailedPath string `json:"failedPath"`
		} `json:"cases"`
	}
	data, err := os.ReadFile(testutil.RepoPath(t, "testdata", "reasonix_output_receipt_conformance.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.Cases)
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.Normalized, reasonixContentWithoutProvenance(tc.Text))
			assert.Equal(t, tc.Path, reasonixOutputFilePointer(tc.Text, tc.Tool, tc.CallID))
			assert.Equal(t, tc.FailedPath, reasonixFailedOutputFilePointer(tc.Text, tc.Tool, tc.CallID, tc.Headline))
		})
	}
}

func TestReasonixFailedOutputReceiptRejectsUntrustedFiles(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"outside root", "unrelated ancestor", "cycle", "foreign metadata", "unsafe parent", "malformed metadata", "directory symlink", "file symlink", "missing file", "directory file", "unterminated line", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			sessionPath := filepath.Join(dir, "session.jsonl")
			directory := filepath.Join(dir, "session.outputs")
			const headline = "native failure"
			body := []byte("error: " + headline + "\ndetails")
			ctx := t.Context()
			switch scenario {
			case "outside root":
				directory = filepath.Join(t.TempDir(), "session.outputs")
			case "unrelated ancestor", "cycle", "foreign metadata", "unsafe parent", "malformed metadata":
				directory = filepath.Join(dir, "parent.outputs")
				meta := reasonixBranchMeta{ID: "session", ParentID: "parent"}
				if scenario == "cycle" {
					meta.ParentID = "session"
				}
				if scenario == "foreign metadata" {
					meta.ID = "another-session"
				}
				if scenario == "unsafe parent" {
					meta.ParentID = "../parent"
				}
				data, err := json.Marshal(meta)
				require.NoError(t, err)
				if scenario == "malformed metadata" {
					data = []byte(`{broken`)
				}
				if scenario != "unrelated ancestor" {
					require.NoError(t, os.WriteFile(sessionPath+".meta", data, 0o600))
				}
			case "unterminated line":
				body = []byte("error: " + headline)
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if scenario == "directory symlink" {
				target := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(target, "call.txt"), body, 0o600))
				if err := os.Symlink(target, directory); err != nil {
					t.Skipf("the test filesystem cannot create a symlink: %v", err)
				}
			} else {
				require.NoError(t, os.MkdirAll(directory, 0o700))
				path := filepath.Join(directory, "call.txt")
				switch scenario {
				case "file symlink":
					target := filepath.Join(t.TempDir(), "other.txt")
					require.NoError(t, os.WriteFile(target, body, 0o600))
					if err := os.Symlink(target, path); err != nil {
						t.Skipf("the test filesystem cannot create a symlink: %v", err)
					}
				case "directory file":
					require.NoError(t, os.Mkdir(path, 0o700))
				case "missing file":
				default:
					require.NoError(t, os.WriteFile(path, body, 0o600))
				}
			}
			receipt := "[bash output kept out of context: 10002 lines, 64 KB]\nFull output: " + filepath.Join(directory, "call.txt") + "\n" + reasonixOutputInstructions + "\n"
			assert.Empty(t, reasonixAuthenticateFailedOutput(ctx, sessionPath, receipt, "bash", "call", headline))
		})
	}
}
