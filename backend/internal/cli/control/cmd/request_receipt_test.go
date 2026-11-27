package cmd

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
)

func TestAgentRequestStatusResult_RenderTextFormatting(t *testing.T) {
	var buf bytes.Buffer
	res := agentRequestStatusResult{
		AgentID:        "ag-1234",
		RequestID:      "req-1",
		InputID:        "in-1",
		Status:         "completed",
		TurnOutcome:    "completed",
		AcceptedSeq:    42,
		WaitingForUser: true,
		QueueError:     "retry failed",
		Replies:        []string{"first \x1b[31mreply\x1b[0m", "second reply"},
	}
	require.NoError(t, res.RenderText(&buf))
	out := buf.String()
	assert.Contains(t, out, "status:      completed\n")
	assert.Contains(t, out, "outcome:     completed\n")
	assert.Contains(t, out, "accepted_seq: 42\n")
	assert.Contains(t, out, "waiting:     waiting_for_user\n")
	assert.Contains(t, out, "queue_error: retry failed\n")
	assert.Contains(t, out, "\nfirst reply\n\nsecond reply\n")
}

func TestWrapRetryReceipt_PreservesHubHintsAndAvoidsDuplicateHints(t *testing.T) {
	peer := &control.HubClient{HubURL: "https://hub.example.test"}

	t.Run("does not block withHubHintForPeer on Connect unauthenticated error", func(t *testing.T) {
		unauth := connect.NewError(connect.CodeUnauthenticated, "token expired")
		wrapped := wrapRetryReceipt(unauth, retryStageDuringSend, retryReceipt{
			RequestID:    "req-auth",
			QueryCommand: "leapmux agent request-status ag-1 req-auth",
		})

		var eager *cli.Error
		assert.False(t, errors.As(wrapped, &eager), "wrapRetryReceipt must not eagerly convert to *cli.Error")

		withHint := withHubHintForPeer(peer, wrapped)
		e := cli.FromError(withHint)
		assert.Equal(t, cli.CodeNotLoggedIn, e.Code)
		require.NotNil(t, e.Receipt)
		assert.Equal(t, dispositionNotSent, e.Receipt.Disposition)
		assert.Equal(t, "req-auth", e.Receipt.RequestID)
		assert.Contains(t, strings.Join(e.Hints, "\n"), `leapmux auth login --hub https://hub.example.test`)
		assert.Contains(t, strings.Join(e.Hints, "\n"), "disposition: not_sent (request_id: req-auth)")
	})

	t.Run("does not block withHubHintForPeer on Connect version mismatch error", func(t *testing.T) {
		unimpl := connect.NewError(connect.CodeUnimplemented, "method unknown")
		wrapped := wrapRetryReceipt(unimpl, retryStageDuringSend, retryReceipt{
			RequestID: "req-ver",
		})
		assert.True(t, needsVersionDiagnostic(wrapped))

		e := cli.FromError(withHubHintForPeer(peer, wrapped))
		assert.Equal(t, cli.CodeVersionMismatch, e.Code)
		require.NotNil(t, e.Receipt)
		assert.Contains(t, strings.Join(e.Hints, "\n"), `leapmux version --hub https://hub.example.test`)
	})

	t.Run("does not duplicate HintProvider hints when classified", func(t *testing.T) {
		inner := &stubHintedErr{
			err:   connect.NewError(connect.CodeUnavailable, "worker down"),
			hints: []string{"re-pair worker credential"},
		}
		wrapped := wrapRetryReceipt(inner, retryStageDuringSend, retryReceipt{
			RequestID:    "req-hp",
			RetryCommand: "leapmux agent send ag-1 hi --request-id req-hp",
		})
		e := cli.FromError(wrapped)
		assert.Equal(t, cli.CodeUnavailable, e.Code)
		require.NotNil(t, e.Receipt)
		assert.Equal(t, dispositionOutcomeUnknown, e.Receipt.Disposition)
		assert.Equal(t, 1, countStrings(e.Hints, "re-pair worker credential"))
	})

	t.Run("does not mutate an underlying *cli.Error", func(t *testing.T) {
		orig := cli.Newf(cli.CodeUnavailable, "channel dropped").WithHint("orig hint")
		wrapped := wrapRetryReceipt(orig, retryStageDuringSend, retryReceipt{RequestID: "req-immut"})
		e := cli.FromError(wrapped)
		require.NotNil(t, e.Receipt)
		assert.Nil(t, orig.Receipt)
		assert.Equal(t, []string{"orig hint"}, orig.Hints)
	})
}

func TestWrapRetryReceipt_ConflictStageHandling(t *testing.T) {
	conflict := cli.Newf(cli.CodeConflict, "conflict occurred")

	t.Run("during send with explicit request-id attaches request-id collision hint", func(t *testing.T) {
		wrapped := wrapRetryReceipt(conflict, retryStageDuringSend, retryReceipt{
			RequestID:    "req-1",
			Explicit:     true,
			QueryCommand: "leapmux agent request-status ag-1 req-1",
		})
		e := cli.FromError(wrapped)
		assert.Equal(t, cli.CodeConflict, e.Code)
		assert.Nil(t, e.Receipt)
		assert.Contains(t, strings.Join(e.Hints, "\n"), `--request-id "req-1"`)
	})

	t.Run("before send does not attach misleading request-id collision hint", func(t *testing.T) {
		noProviders := cli.Newf(cli.CodeConflict, "no agent providers are installed")
		wrapped := wrapRetryReceipt(noProviders, retryStageBeforeSend, retryReceipt{
			RequestID:    "req-2",
			Explicit:     true,
			RetryCommand: "leapmux agent open --request-id req-2",
		})
		e := cli.FromError(wrapped)
		assert.Equal(t, cli.CodeConflict, e.Code)
		require.NotNil(t, e.Receipt)
		assert.Equal(t, dispositionNotSent, e.Receipt.Disposition)
		assert.NotContains(t, strings.Join(e.Hints, "\n"), "belongs to a request with other arguments")
	})

	t.Run("during wait preserves receipt and query_command without collision hint", func(t *testing.T) {
		schemaErr := cli.Newf(cli.CodeConflict, "agent ag-1 did not return output matching --output-schema").
			WithExitCode(cli.ExitWaitEnded)
		wrapped := wrapRetryReceipt(schemaErr, retryStageDuringWait, retryReceipt{
			RequestID:    "req-3",
			Explicit:     true,
			QueryCommand: "leapmux agent request-status ag-1 req-3",
			RetryCommand: "leapmux agent send ag-1 --wait --request-id req-3 hi",
		})
		e := cli.FromError(wrapped)
		assert.Equal(t, cli.CodeConflict, e.Code)
		assert.Equal(t, cli.ExitWaitEnded, e.ExitCode())
		require.NotNil(t, e.Receipt)
		assert.Equal(t, dispositionInProgress, e.Receipt.Disposition)
		assert.Equal(t, "leapmux agent request-status ag-1 req-3", e.Receipt.QueryCommand)
		assert.NotContains(t, strings.Join(e.Hints, "\n"), "belongs to a request with other arguments")
		assert.Contains(t, strings.Join(e.Hints, "\n"), "check status: leapmux agent request-status ag-1 req-3")
	})

	t.Run("during send with auto-generated request-id and no created agent returns raw error", func(t *testing.T) {
		wrapped := wrapRetryReceipt(conflict, retryStageDuringSend, retryReceipt{
			RequestID: "req-auto",
			Explicit:  false,
		})
		assert.Same(t, conflict, wrapped)
	})

	t.Run("during send with auto-generated request-id and created agent preserves receipt", func(t *testing.T) {
		wrapped := wrapRetryReceipt(conflict, retryStageDuringSend, retryReceipt{
			RequestID:      "req-auto-created",
			Explicit:       false,
			CreatedAgentID: "ag-created",
			QueryCommand:   "leapmux agent request-status ag-created req-auto-created",
		})
		e := cli.FromError(wrapped)
		assert.Equal(t, cli.CodeConflict, e.Code)
		require.NotNil(t, e.Receipt)
		assert.Equal(t, dispositionOutcomeUnknown, e.Receipt.Disposition)
		assert.Contains(t, strings.Join(e.Hints, "\n"), "agent ag-created was already created")
		assert.NotContains(t, strings.Join(e.Hints, "\n"), "belongs to a request with other arguments")
	})
}

func TestWrapRetryReceipt_ExitStatusPassesThroughUnwrapped(t *testing.T) {
	exitErr := &cli.ExitStatus{Code: cli.ExitWaitEnded}
	wrapped := wrapRetryReceipt(exitErr, retryStageDuringWait, retryReceipt{
		RequestID:    "req-exit",
		RetryCommand: "leapmux terminal run tm-1 --wait --exit --request-id req-exit ls",
	})
	assert.Same(t, exitErr, wrapped)
}

func TestWrapRetryReceipt_CreatedAgentIDNeverReportsNotSent(t *testing.T) {
	t.Run("before-send failure after tab creation reports in_progress", func(t *testing.T) {
		wrapped := wrapRetryReceipt(cli.Newf(cli.CodeNotFound, "agent closed"), retryStageBeforeSend, retryReceipt{
			RequestID:      "req-open-1",
			CreatedAgentID: "ag-created",
			QueryCommand:   "leapmux agent request-status ag-created req-open-1",
			RetryCommand:   "leapmux agent open --message hi --wait --request-id req-open-1",
		})
		e := cli.FromError(wrapped)
		require.NotNil(t, e.Receipt)
		assert.Equal(t, dispositionInProgress, e.Receipt.Disposition)
		hints := strings.Join(e.Hints, "\n")
		assert.Contains(t, hints, "agent ag-created was already created")
		assert.Contains(t, hints, "safe retry:")
	})

	t.Run("invalid_argument during send after tab creation reports in_progress", func(t *testing.T) {
		wrapped := wrapRetryReceipt(cli.Newf(cli.CodeInvalidArgument, "unsupported attachment"), retryStageDuringSend, retryReceipt{
			RequestID:      "req-open-2",
			CreatedAgentID: "ag-created",
			QueryCommand:   "leapmux agent request-status ag-created req-open-2",
			RetryCommand:   "leapmux agent fork ag-src --attach bad.bin --request-id req-open-2",
		})
		e := cli.FromError(wrapped)
		require.NotNil(t, e.Receipt)
		assert.Equal(t, dispositionInProgress, e.Receipt.Disposition)
		assert.Contains(t, strings.Join(e.Hints, "\n"), "agent ag-created was already created")
	})

	t.Run("explicit conflict during send after tab creation still gives request-id collision hint", func(t *testing.T) {
		wrapped := wrapRetryReceipt(cli.Newf(cli.CodeConflict, "input conflict"), retryStageDuringSend, retryReceipt{
			RequestID:      "req-open-3",
			Explicit:       true,
			CreatedAgentID: "ag-created",
			QueryCommand:   "leapmux agent request-status ag-created req-open-3",
		})
		e := cli.FromError(wrapped)
		assert.Nil(t, e.Receipt)
		assert.Contains(t, strings.Join(e.Hints, "\n"), `--request-id "req-open-3"`)
	})
}

func captureTestInvocation(t *testing.T, use string, flags func(*pflag.FlagSet), args ...string) *cli.Invocation {
	t.Helper()
	var captured *cli.Invocation
	leaf := cli.NewLeaf(cli.Leaf{
		Use:   use,
		Short: "test",
		Flags: flags,
		Run: func(inv *cli.Invocation) (any, error) {
			captured = inv
			return nil, nil
		},
	})
	root := cli.Group("leapmux", "test", leaf)
	var out, errOut bytes.Buffer
	err := cli.Run(context.Background(), root, append([]string{strings.Fields(use)[0]}, args...), strings.NewReader(""), &out, &errOut)
	require.NoError(t, err)
	require.NotNil(t, captured)
	return captured
}

func TestRetryCommandBuilders_IncludeAllSchemaWorkspaceAndTerminalWaitFlags(t *testing.T) {
	t.Run("agent send includes output-schema, schema-retries, and POSIX quoting", func(t *testing.T) {
		inv := captureTestInvocation(t, "send <agent> [text]", func(fs *pflag.FlagSet) {
			fs.StringArray("attach", nil, "")
			fs.String("at", "", "")
			fs.Bool("steer", false, "")
			fs.Bool("preempt", false, "")
			fs.Bool("all", false, "")
			fs.Duration("timeout", 0, "")
			addOutputSchemaFlags(fs)
		}, "ag-1", "--output-schema", "my schema.json", "--schema-retries", "4", "--timeout", "30s", "--", "-leading dash 'quote'")

		got := buildAgentSendRetryCmd(inv, "ag-1", "-leading dash 'quote'", true, "req-1")
		assert.Equal(t,
			`leapmux agent send ag-1 --timeout 30s --output-schema 'my schema.json' --schema-retries 4 --wait --request-id req-1 -- '-leading dash '\''quote'\'''`,
			got,
		)
	})

	t.Run("agent open and fork include output-schema, schema-retries, and workspace", func(t *testing.T) {
		openInv := captureTestInvocation(t, "open", func(fs *pflag.FlagSet) {
			for _, name := range []string{
				"provider", "model", "effort", "permission-mode", "title", "name",
				"message", "file", "resume", "dir", "worktree", "use-worktree",
				"checkout", "new-branch", "base", "workspace", "tile", "worker",
				"before", "after",
			} {
				fs.String(name, "", "")
			}
			fs.StringArray("option", nil, "")
			fs.StringArray("attach", nil, "")
			for _, name := range []string{"first", "last", "focus", "no-focus", "wait", "all"} {
				fs.Bool(name, false, "")
			}
			fs.Duration("timeout", 0, "")
			addOutputSchemaFlags(fs)
		}, "--workspace", "ws-1", "--message", "hi", "--wait", "--output-schema", `{"type":"object"}`, "--schema-retries", "3")

		openCmd := buildAgentOpenRetryCmd(openInv, "req-open")
		assert.Contains(t, openCmd, "--workspace ws-1")
		assert.Contains(t, openCmd, `--output-schema '{"type":"object"}'`)
		assert.Contains(t, openCmd, "--schema-retries 3")
		assert.Contains(t, openCmd, "--request-id req-open")

		forkInv := captureTestInvocation(t, "fork <agent>", func(fs *pflag.FlagSet) {
			for _, name := range []string{
				"model", "effort", "permission-mode", "title", "name",
				"message", "file", "dir", "worktree", "use-worktree",
				"checkout", "new-branch", "base", "workspace", "tile", "worker",
				"before", "after",
			} {
				fs.String(name, "", "")
			}
			fs.StringArray("option", nil, "")
			fs.StringArray("attach", nil, "")
			for _, name := range []string{"first", "last", "focus", "no-focus", "wait", "all"} {
				fs.Bool(name, false, "")
			}
			fs.Duration("timeout", 0, "")
			addOutputSchemaFlags(fs)
		}, "ag-src", "--workspace", "ws-2", "--wait", "--output-schema", "schema.json", "--schema-retries", "1")

		forkCmd := buildAgentForkRetryCmd(forkInv, "req-fork")
		assert.Contains(t, forkCmd, "leapmux agent fork ag-src")
		assert.Contains(t, forkCmd, "--workspace ws-2")
		assert.Contains(t, forkCmd, "--output-schema schema.json")
		assert.Contains(t, forkCmd, "--schema-retries 1")
		assert.Contains(t, forkCmd, "--request-id req-fork")
	})

	t.Run("terminal run includes change and after-offset", func(t *testing.T) {
		inv := captureTestInvocation(t, "run <terminal> [command]", func(fs *pflag.FlagSet) {
			addTerminalWaitConditionFlags(fs, true)
		}, "tm-1", "make test", "--change", "--after-offset", "1024", "--timeout", "1m")

		got := buildTerminalRunRetryCmd(inv, "tm-1", "make test", true, "req-tm")
		assert.Equal(t,
			"leapmux terminal run tm-1 --wait --change --after-offset 1024 --timeout 1m0s --request-id req-tm 'make test'",
			got,
		)
	})
}

func findSubcommand(t *testing.T, parent *cobra.Command, name string) *cobra.Command {
	t.Helper()
	for _, sub := range parent.Commands() {
		if sub.Name() == name {
			return sub
		}
	}
	t.Fatalf("subcommand %q not found under %q", name, parent.Name())
	return nil
}

func captureInvocationFromRealLeaf(t *testing.T, realLeaf *cobra.Command, args ...string) *cli.Invocation {
	t.Helper()
	var captured *cli.Invocation
	leaf := cli.NewLeaf(cli.Leaf{
		Use:   realLeaf.Use,
		Short: realLeaf.Short,
		Flags: func(fs *pflag.FlagSet) {
			realLeaf.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) {
				fs.AddFlag(f)
			})
		},
		Run: func(inv *cli.Invocation) (any, error) {
			captured = inv
			return nil, nil
		},
	})
	root := cli.Group("leapmux", "test", leaf)
	var out, errOut bytes.Buffer
	err := cli.Run(context.Background(), root, append([]string{realLeaf.Name()}, args...), strings.NewReader(""), &out, &errOut)
	require.NoError(t, err)
	require.NotNil(t, captured)
	return captured
}

// TestRetryCommandBuilders_CoverEveryCommandFlag verifies that every local
// non-persistent flag on each retryable command is either emitted by its retry
// builder or explicitly documented as excluded. Adding a new flag to one of
// these commands without updating its builder (or exclusion table) fails here.
func TestRetryCommandBuilders_CoverEveryCommandFlag(t *testing.T) {
	type commandCase struct {
		name           string
		leaf           func() *cobra.Command
		positionalArgs []string
		// excluded lists local flags intentionally omitted from the flag loop,
		// mapped to the reason they are excluded.
		excluded map[string]string
		build    func(inv *cli.Invocation) string
	}

	cases := []commandCase{
		{
			name:           "agent send",
			leaf:           func() *cobra.Command { return findSubcommand(t, newAgentCommand(), "send") },
			positionalArgs: []string{"ag-1", "hello"},
			excluded: map[string]string{
				requestIDFlag: "emitted from the requestID parameter rather than the flag value",
				"file":        "message content is already read and inlined as the positional text argument",
				"workspace":   "agentID is already resolved to its canonical tab ID before building the retry command",
			},
			build: func(inv *cli.Invocation) string {
				return buildAgentSendRetryCmd(inv, "ag-1", "hello", boolFlag(inv, "wait"), "req-send")
			},
		},
		{
			name: "agent open",
			leaf: func() *cobra.Command { return findSubcommand(t, newAgentCommand(), "open") },
			excluded: map[string]string{
				requestIDFlag: "emitted from the requestID parameter rather than the flag value",
			},
			build: func(inv *cli.Invocation) string {
				return buildAgentOpenRetryCmd(inv, "req-open")
			},
		},
		{
			name:           "agent fork",
			leaf:           func() *cobra.Command { return findSubcommand(t, newAgentCommand(), "fork") },
			positionalArgs: []string{"ag-src"},
			excluded: map[string]string{
				requestIDFlag: "emitted from the requestID parameter rather than the flag value",
			},
			build: func(inv *cli.Invocation) string {
				return buildAgentForkRetryCmd(inv, "req-fork")
			},
		},
		{
			name:           "terminal run",
			leaf:           func() *cobra.Command { return findSubcommand(t, newTerminalCommand(), "run") },
			positionalArgs: []string{"tm-1", "echo ok"},
			excluded: map[string]string{
				requestIDFlag: "emitted from the requestID parameter rather than the flag value",
				"file":        "command text is already read and inlined as the positional command argument",
			},
			build: func(inv *cli.Invocation) string {
				return buildTerminalRunRetryCmd(inv, "tm-1", "echo ok", boolFlag(inv, "wait"), "req-run")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leaf := tc.leaf()
			localFlags := leaf.LocalNonPersistentFlags()

			// Guard against stale entries in the exclusion map.
			for name := range tc.excluded {
				require.NotNil(t, localFlags.Lookup(name), "excluded flag --%s no longer exists on %s", name, tc.name)
			}

			args := append([]string(nil), tc.positionalArgs...)
			type expectedFlag struct {
				name  string
				value string // empty for bool flags
			}
			var expected []expectedFlag

			localFlags.VisitAll(func(f *pflag.Flag) {
				if f.Name == "help" {
					return
				}
				if _, skip := tc.excluded[f.Name]; skip {
					return
				}
				switch f.Value.Type() {
				case "bool":
					args = append(args, "--"+f.Name)
					expected = append(expected, expectedFlag{name: f.Name})
				case "string", "stringArray", "stringSlice":
					val := "val-" + f.Name
					args = append(args, "--"+f.Name, val)
					expected = append(expected, expectedFlag{name: f.Name, value: val})
				case "duration":
					args = append(args, "--"+f.Name, "42s")
					expected = append(expected, expectedFlag{name: f.Name, value: "42s"})
				case "int", "int64", "uint", "uint32", "uint64":
					args = append(args, "--"+f.Name, "7")
					expected = append(expected, expectedFlag{name: f.Name, value: "7"})
				default:
					t.Fatalf("unhandled flag type %q for --%s on %s", f.Value.Type(), f.Name, tc.name)
				}
			})

			require.NotEmpty(t, expected, "%s must have non-excluded flags to verify", tc.name)
			inv := captureInvocationFromRealLeaf(t, leaf, args...)
			got := tc.build(inv)
			tokens := strings.Fields(got)

			assert.Contains(t, tokens, "--"+requestIDFlag, "retry command must emit --%s: %s", requestIDFlag, got)
			for _, exp := range expected {
				flagTok := "--" + exp.name
				idx := slices.Index(tokens, flagTok)
				require.NotEqual(t, -1, idx, "retry command for %s missed flag %s:\n  %s", tc.name, flagTok, got)
				if exp.value != "" {
					require.Less(t, idx+1, len(tokens), "flag %s had no value in: %s", flagTok, got)
					assert.Equal(t, exp.value, tokens[idx+1], "flag %s value mismatch in: %s", flagTok, got)
				}
			}
		})
	}
}

func TestAgentRequestStatus_SkipsListMessagesWhenQueued(t *testing.T) {
	agID := fullTestID("ag-queued")
	reqID := "queued-req-1"
	inputID := requestScopedID(reqID, requestScopeAgentSend, agID)
	listMessagesCalls := 0

	startPhase3BIPC(t,
		func(method string, _ []byte) (proto.Message, error) {
			tab := &leapmuxv1.WorkspaceTab{
				Tab:      &leapmuxv1.TabIdent{TabId: agID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
				WorkerId: "worker-A",
			}
			switch method {
			case leapmuxv1connect.WorkspaceServiceLocateTabProcedure:
				return &leapmuxv1.LocateTabResponse{Tab: tab}, nil
			case leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure:
				return &leapmuxv1.GetWorkerResponse{Worker: &leapmuxv1.Worker{Id: "worker-A", Online: true}}, nil
			default:
				return &leapmuxv1.ListTabsResponse{Tabs: []*leapmuxv1.WorkspaceTab{tab}}, nil
			}
		},
		func(method string, body []byte) (proto.Message, error) {
			switch method {
			case leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure:
				var req leapmuxv1.ListAgentInputQueueRequest
				require.NoError(t, proto.Unmarshal(body, &req))
				assert.Equal(t, inputID, req.GetReplyInputId())
				return &leapmuxv1.ListAgentInputQueueResponse{
					Snapshot: &leapmuxv1.AgentInputQueueSnapshot{
						Items: []*leapmuxv1.QueuedAgentInput{{
							Id:    inputID,
							State: leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED,
						}},
					},
				}, nil
			case leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure:
				listMessagesCalls++
				return nil, assert.AnError
			default:
				return nil, assert.AnError
			}
		},
	)

	// Pass whitespace around request-id to verify trimming derives the exact same inputID.
	status := runControl(t, "agent", "request-status", agID, "  "+reqID+"  ").Data(t)
	assert.Equal(t, "queued", status["status"])
	assert.Equal(t, reqID, status["request_id"])
	assert.Equal(t, inputID, status["input_id"])
	assert.Equal(t, 0, listMessagesCalls, "ListAgentMessages must be skipped while input is still in queue")
}

func TestAgentRequestStatus_CanonicalTransitionAndUserMessageRowPreservesReplies(t *testing.T) {
	agID := fullTestID("ag-canon")
	reqID := "canon-req-1"
	inputID := requestScopedID(reqID, requestScopeAgentSend, agID)
	pageCalls := 0
	var seenCanonical []*bool

	startPhase3BIPC(t,
		func(method string, _ []byte) (proto.Message, error) {
			tab := &leapmuxv1.WorkspaceTab{
				Tab:      &leapmuxv1.TabIdent{TabId: agID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
				WorkerId: "worker-A",
			}
			switch method {
			case leapmuxv1connect.WorkspaceServiceLocateTabProcedure:
				return &leapmuxv1.LocateTabResponse{Tab: tab}, nil
			case leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure:
				return &leapmuxv1.GetWorkerResponse{Worker: &leapmuxv1.Worker{Id: "worker-A", Online: true}}, nil
			default:
				return &leapmuxv1.ListTabsResponse{Tabs: []*leapmuxv1.WorkspaceTab{tab}}, nil
			}
		},
		func(method string, body []byte) (proto.Message, error) {
			switch method {
			case leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure:
				return &leapmuxv1.ListAgentInputQueueResponse{
					Snapshot: &leapmuxv1.AgentInputQueueSnapshot{},
					InputReply: &leapmuxv1.AgentInputTurnReply{
						AcceptedSeq: 20,
						Outcome:     leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
					},
				}, nil
			case leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure:
				var req leapmuxv1.ListAgentMessagesRequest
				require.NoError(t, proto.Unmarshal(body, &req))
				assert.Equal(t, inputID, req.GetReplyInputId())
				assert.True(t, req.GetIncludeReplyText())
				seenCanonical = append(seenCanonical, req.ReplyCursorCanonical)
				pageCalls++
				if pageCalls == 1 {
					// Pre-canonical page 1 with HasMore=true.
					stale := "stale pre-canonical"
					return &leapmuxv1.ListAgentMessagesResponse{
						ReplyCanonical:  false,
						HasMore:         true,
						NextReplyCursor: 21,
						Messages: []*leapmuxv1.AgentChatMessage{{
							Seq:       21,
							Source:    leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
							ReplyText: &stale,
						}},
					}, nil
				}
				// Page 2 transitions to canonical=true, restarting from canonical rows
				// including the user message row (MARK_TYPE_USER_MESSAGE) followed by two agent replies.
				r1 := "first canonical reply"
				r2 := "final canonical reply"
				return &leapmuxv1.ListAgentMessagesResponse{
					ReplyCanonical: true,
					HasMore:        false,
					Messages: []*leapmuxv1.AgentChatMessage{
						{
							Seq:      20,
							Source:   leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
							MarkType: leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE,
						},
						{
							Seq:       21,
							Source:    leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
							ReplyText: &r1,
						},
						{
							Seq:       22,
							Source:    leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
							ReplyText: &r2,
						},
					},
				}, nil
			default:
				return nil, assert.AnError
			}
		},
	)

	status := runControl(t, "agent", "request-status", agID, reqID, "--all").Data(t)
	assert.Equal(t, "completed", status["status"])
	assert.Equal(t, []any{"first canonical reply", "final canonical reply"}, status["replies"])
	require.Len(t, seenCanonical, 2)
	assert.Nil(t, seenCanonical[0], "first page sends nil ReplyCursorCanonical")
	require.NotNil(t, seenCanonical[1])
	assert.False(t, *seenCanonical[1], "second page echoes previous ReplyCanonical=false")
}

func TestTerminalRunWaitOverIPC_ExitStatusPrintsReceiptNoticesAndKeepsExitSix(t *testing.T) {
	t.Run("timeout prints in_progress receipt and keeps exit six", func(t *testing.T) {
		id := fullTestID("term-wait-to")
		w := &terminalWorker{
			info: &leapmuxv1.TerminalInfo{
				TerminalId: id,
				Status:     leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY,
				Cols:       80,
				Rows:       24,
			},
		}
		startTerminalWorker(t, w, id)

		o := runControl(t, "terminal", "run", id, "make test", "--wait", "--text", "PASS", "--timeout", "1ms", "--request-id", "term-req-1")
		assert.Equal(t, cli.ExitWaitEnded, o.Code, "stderr: %s", o.Stderr)
		assert.Contains(t, o.Stdout, `"reached":false`)
		assert.Contains(t, o.Stdout, `"request_id":"term-req-1"`)
		assert.Contains(t, o.Stdout, `"disposition":"in_progress"`)
		assert.Contains(t, o.Stderr, "disposition: in_progress (request_id: term-req-1)")
		assert.Contains(t, o.Stderr, "safe retry: leapmux terminal run "+id+" --wait --text PASS --timeout 1ms --request-id term-req-1 'make test'")

		inputs := w.recordedInputs()
		require.Len(t, inputs, 1)
		assert.Equal(t, "term-req-1", inputs[0].GetRequestId())
	})

	t.Run("exited shell keeps exit six without in_progress receipt", func(t *testing.T) {
		id := fullTestID("term-wait-rec")
		w := &terminalWorker{
			info: &leapmuxv1.TerminalInfo{
				TerminalId: id,
				Status:     leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED,
				Cols:       80,
				Rows:       24,
			},
		}
		startTerminalWorker(t, w, id)

		o := runControl(t, "terminal", "run", id, "make test", "--wait", "--text", "PASS", "--request-id", "term-req-exited")
		assert.Equal(t, cli.ExitWaitEnded, o.Code, "stderr: %s", o.Stderr)
		assert.Contains(t, o.Stdout, `"reached":false`)
		assert.Contains(t, o.Stdout, `"request_id":"term-req-exited"`)
		assert.NotContains(t, o.Stderr, "disposition: in_progress")
		assert.NotContains(t, o.Stderr, "safe retry:")

		inputs := w.recordedInputs()
		require.Len(t, inputs, 1)
		assert.Equal(t, "term-req-exited", inputs[0].GetRequestId())
	})
}

func TestAgentRequestStatus_NotFoundFailedAndCompletedEmptyReplies(t *testing.T) {
	hubHandler := func(agID string) func(string, []byte) (proto.Message, error) {
		return func(method string, _ []byte) (proto.Message, error) {
			tab := &leapmuxv1.WorkspaceTab{
				Tab:      &leapmuxv1.TabIdent{TabId: agID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
				WorkerId: "worker-A",
			}
			switch method {
			case leapmuxv1connect.WorkspaceServiceLocateTabProcedure:
				return &leapmuxv1.LocateTabResponse{Tab: tab}, nil
			case leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure:
				return &leapmuxv1.GetWorkerResponse{Worker: &leapmuxv1.Worker{Id: "worker-A", Online: true}}, nil
			default:
				return &leapmuxv1.ListTabsResponse{Tabs: []*leapmuxv1.WorkspaceTab{tab}}, nil
			}
		}
	}

	t.Run("not_found when absent from queue and transcript", func(t *testing.T) {
		agID := fullTestID("ag-rs-nf")
		startPhase3BIPC(t, hubHandler(agID), func(method string, _ []byte) (proto.Message, error) {
			switch method {
			case leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure:
				return &leapmuxv1.ListAgentInputQueueResponse{Snapshot: &leapmuxv1.AgentInputQueueSnapshot{}}, nil
			case leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure:
				return &leapmuxv1.ListAgentMessagesResponse{}, nil
			default:
				return nil, assert.AnError
			}
		})

		data := runControl(t, "agent", "request-status", agID, "missing-req").Data(t)
		assert.Equal(t, "not_found", data["status"])
		assert.Equal(t, "missing-req", data["request_id"])
		_, hasReplies := data["replies"]
		assert.False(t, hasReplies)

		q := runControlText(t, "agent", "request-status", agID, "missing-req", "-q")
		require.Equal(t, cli.ExitOK, q.Code, "stderr: %s", q.Stderr)
		assert.Equal(t, "not_found\n", q.Stdout)
	})

	t.Run("failed from queued item skips ListAgentMessages", func(t *testing.T) {
		agID := fullTestID("ag-rs-qf")
		reqID := "failed-queue-req"
		inputID := requestScopedID(reqID, requestScopeAgentSend, agID)
		listMessagesCalls := 0
		startPhase3BIPC(t, hubHandler(agID), func(method string, _ []byte) (proto.Message, error) {
			switch method {
			case leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure:
				return &leapmuxv1.ListAgentInputQueueResponse{
					Snapshot: &leapmuxv1.AgentInputQueueSnapshot{
						Items: []*leapmuxv1.QueuedAgentInput{{
							Id:    inputID,
							State: leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED,
							Error: "provider crashed",
						}},
					},
				}, nil
			case leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure:
				listMessagesCalls++
				return nil, assert.AnError
			default:
				return nil, assert.AnError
			}
		})

		data := runControl(t, "agent", "request-status", agID, reqID).Data(t)
		assert.Equal(t, "failed", data["status"])
		assert.Equal(t, "failed", data["queue_state"])
		assert.Equal(t, "provider crashed", data["queue_error"])
		assert.Equal(t, 0, listMessagesCalls)
	})

	t.Run("failed from exact turn outcome", func(t *testing.T) {
		agID := fullTestID("ag-rs-tf")
		startPhase3BIPC(t, hubHandler(agID), func(method string, _ []byte) (proto.Message, error) {
			switch method {
			case leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure:
				return &leapmuxv1.ListAgentInputQueueResponse{
					Snapshot: &leapmuxv1.AgentInputQueueSnapshot{},
					InputReply: &leapmuxv1.AgentInputTurnReply{
						AcceptedSeq: 12,
						Outcome:     leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED,
					},
				}, nil
			case leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure:
				return &leapmuxv1.ListAgentMessagesResponse{}, nil
			default:
				return nil, assert.AnError
			}
		})

		data := runControl(t, "agent", "request-status", agID, "failed-turn-req").Data(t)
		assert.Equal(t, "failed", data["status"])
		assert.Equal(t, "failed", data["turn_outcome"])
		assert.EqualValues(t, 12, data["accepted_seq"])
		_, hasReplies := data["replies"]
		assert.False(t, hasReplies)
	})

	t.Run("completed with empty replies omits replies field", func(t *testing.T) {
		agID := fullTestID("ag-rs-ce")
		startPhase3BIPC(t, hubHandler(agID), func(method string, _ []byte) (proto.Message, error) {
			switch method {
			case leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure:
				return &leapmuxv1.ListAgentInputQueueResponse{
					Snapshot: &leapmuxv1.AgentInputQueueSnapshot{},
					InputReply: &leapmuxv1.AgentInputTurnReply{
						AcceptedSeq: 15,
						Outcome:     leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
					},
				}, nil
			case leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure:
				return &leapmuxv1.ListAgentMessagesResponse{ReplyCanonical: true}, nil
			default:
				return nil, assert.AnError
			}
		})

		data := runControl(t, "agent", "request-status", agID, "empty-reply-req").Data(t)
		assert.Equal(t, "completed", data["status"])
		assert.Equal(t, "completed", data["turn_outcome"])
		assert.EqualValues(t, 15, data["accepted_seq"])
		_, hasReplies := data["replies"]
		assert.False(t, hasReplies)

		text := runControlText(t, "agent", "request-status", agID, "empty-reply-req")
		require.Equal(t, cli.ExitOK, text.Code, "stderr: %s", text.Stderr)
		assert.Contains(t, text.Stdout, "status:      completed\n")
		assert.Contains(t, text.Stdout, "accepted_seq: 15\n")
		assert.NotContains(t, text.Stdout, "\n\n", "empty replies must not emit a trailing blank separator")
	})

	t.Run("whitespace-only request-id fails with usage error", func(t *testing.T) {
		clearRemoteEnv(t)
		f := runControl(t, "agent", "request-status", fullTestID("ag-rs-ws"), "   ", "--hub", "https://stub.invalid").requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "request-id is empty")
	})
}
