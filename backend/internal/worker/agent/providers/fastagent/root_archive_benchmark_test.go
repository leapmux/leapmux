package fastagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

func BenchmarkFastagentRootArchive(b *testing.B) {
	for _, size := range []int{1 << 20, 20 << 20, 60 << 20} {
		for _, rewritten := range []bool{false, true} {
			mode := "unchanged"
			if rewritten {
				mode = "rewritten"
			}
			b.Run(fmt.Sprintf("%dMiB/%s", size>>20, mode), func(b *testing.B) {
				home, cwd := b.TempDir(), b.TempDir()
				directory := filepath.Join(home, "sessions", "root")
				if err := os.MkdirAll(directory, 0o700); err != nil {
					b.Fatal(err)
				}
				snapshot := fmt.Sprintf("{\"session_id\":\"root\",\"execution\":{\"resumable\":true,\"child_link\":null},\"continuation\":{\"cwd\":%q,\"active_agent\":\"agent\",\"agents\":{\"agent\":{\"history_file\":\"history_agent.json\"}}}}", cwd)
				if err := os.WriteFile(filepath.Join(directory, "session.json"), []byte(snapshot), 0o600); err != nil {
					b.Fatal(err)
				}
				historical := "{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"text\":\"old input\"}]}," +
					"{\"role\":\"assistant\",\"timestamp\":\"2026-10-26T00:00:00Z\",\"tool_calls\":{\"historical\":{\"method\":\"tools/call\",\"params\":{\"name\":\"bash\",\"arguments\":{\"command\":\"large output\"}}}},\"stop_reason\":\"toolUse\"}," +
					"{\"role\":\"user\",\"tool_results\":{\"historical\":{\"content\":[{\"type\":\"text\",\"text\":\"" + strings.Repeat("x", size) + "\"}],\"isError\":false}}}," +
					"{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"old final\"}],\"stop_reason\":\"endTurn\"}"
				current := "{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"text\":\"current input\"}]}," +
					"{\"role\":\"assistant\",\"timestamp\":\"2026-10-26T00:01:00Z\",\"tool_calls\":{\"current\":{\"method\":\"tools/call\",\"params\":{\"name\":\"bash\",\"arguments\":{\"command\":\"current output\"}}}},\"stop_reason\":\"toolUse\"}," +
					"{\"role\":\"user\",\"tool_results\":{\"current\":{\"content\":[{\"type\":\"text\",\"text\":\"current output\"}],\"isError\":false}}}," +
					"{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"current final\"}],\"stop_reason\":\"endTurn\"}"
				before := []byte("{\"messages\":[" + historical + "]}")
				after := []byte("{\"messages\":[" + historical + "," + current + "]}")
				path := filepath.Join(directory, "history_agent.json")
				if err := os.WriteFile(path, before, 0o600); err != nil {
					b.Fatal(err)
				}
				expected, err := fastagentInputDigest([]json.RawMessage{json.RawMessage(`{"type":"text","text":"current input"}`)}, true)
				if err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				for b.Loop() {
					if rewritten {
						b.StopTimer()
						if err := os.WriteFile(path, before, 0o600); err != nil {
							b.Fatal(err)
						}
						b.StartTimer()
					}
					histories, err := readFastagentRootArchive(home, "root", cwd)
					if err != nil {
						b.Fatal(err)
					}
					baseline, err := captureFastagentRootReceipt(histories)
					if err != nil {
						b.Fatal(err)
					}
					if rewritten {
						b.StopTimer()
						if err := os.WriteFile(path, after, 0o600); err != nil {
							b.Fatal(err)
						}
						b.StartTimer()
					}
					histories, err = readFastagentRootArchive(home, "root", cwd)
					if err != nil {
						b.Fatal(err)
					}
					_, projectionErr := projectFastagentRootArchive(baseline, histories, "benchmark-capture", expected, agent.MessageCompletionComplete)
					if rewritten && projectionErr != nil {
						b.Fatal(projectionErr)
					}
					if !rewritten && projectionErr == nil {
						b.Fatal("an unchanged archive supplied an unexpected current suffix")
					}
				}
			})
		}
	}
}
