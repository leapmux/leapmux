package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failedSetupOutput struct {
	cause error
	limit int
}

func (w failedSetupOutput) Write(p []byte) (int, error) { return min(len(p), w.limit), w.cause }

func TestSetupPromptsRejectFailedOutputBeforeReadingConsent(t *testing.T) {
	for name, output := range map[string]failedSetupOutput{
		"write failure":         {cause: assert.AnError},
		"partial write failure": {cause: assert.AnError, limit: 1},
		"short write":           {},
		"partial short write":   {limit: 1},
	} {
		t.Run(name, func(t *testing.T) {
			answer := strings.NewReader("yes\n")
			accepted, err := promptApply(output, answer)
			require.Error(t, err)
			assert.False(t, accepted)
			assert.Equal(t, 4, answer.Len(), "do not consume an answer to an undisplayed prompt")
			choice := strings.NewReader("1\n")
			_, err = promptStartupChoice(output, choice)
			require.Error(t, err)
			assert.Equal(t, 2, choice.Len())
			if output.cause != nil {
				assert.ErrorIs(t, err, output.cause)
			} else {
				assert.ErrorIs(t, err, io.ErrShortWrite)
			}
		})
	}
}

func TestNativeConfigurationRejectsUndisplayedPreview(t *testing.T) {
	for _, verb := range []string{"enable", "start", "restart"} {
		t.Run(verb, func(t *testing.T) {
			root := mustCanonicalDir(t, t.TempDir())
			manager := &nativeStartRecorder{t: t}
			factory := nativeRecorderController(manager)
			output, err := os.CreateTemp(t.TempDir(), "closed-output-")
			require.NoError(t, err)
			require.NoError(t, output.Close())
			original := os.Stdout
			os.Stdout = output
			defer func() { os.Stdout = original }()
			args := []string{"--data-dir", root, "--startup=boot", "--yes"}
			if verb == "enable" {
				err = runModeEnableWithController("hub", factory)(cmdCtx{}, args)
			} else {
				err = executeNativeStartWithController("hub", verb, args, factory)
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, os.ErrClosed)
			assert.Empty(t, manager.installs)
			assert.Empty(t, manager.starts)
			assert.NoFileExists(t, lifecycle.PathsOf(root).ServiceSpec)
		})
	}
}

type interruptedPromptAnswer struct {
	text  string
	cause error
}

func (r interruptedPromptAnswer) Read(p []byte) (int, error) { return copy(p, r.text), r.cause }

func TestIncompletePromptAnswersNeverAuthorize(t *testing.T) {
	for _, cause := range []error{io.EOF, context.Canceled, io.ErrUnexpectedEOF} {
		t.Run(cause.Error(), func(t *testing.T) {
			accepted, err := promptApply(&bytes.Buffer{}, interruptedPromptAnswer{"yes", cause})
			require.NoError(t, err)
			assert.False(t, accepted, "a partial answer with an input failure cannot authorize setup")
			_, err = promptStartupChoice(&bytes.Buffer{}, interruptedPromptAnswer{"1", cause})
			require.Error(t, err)
		})
	}
}

func TestNoninteractiveSetupRefusesMissingFlagsWithoutPrompting(t *testing.T) {
	for _, verb := range []string{"enable", "start", "restart"} {
		for _, startup := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/startup=%t", verb, startup), func(t *testing.T) {
				reader, writer, err := os.Pipe()
				require.NoError(t, err)
				original := os.Stdin
				os.Stdin = reader
				t.Cleanup(func() { os.Stdin = original; _ = reader.Close(); _ = writer.Close() })
				root := t.TempDir()
				manager := &nativeStartRecorder{t: t}
				factory := nativeRecorderController(manager)
				args := []string{"--data-dir", root}
				if startup {
					args = append(args, "--startup=boot")
				}
				output := testutil.CaptureStdout(t, func() {
					done := make(chan struct{})
					go func() {
						defer close(done)
						if verb == "enable" {
							err = runModeEnableWithController("hub", factory)(cmdCtx{}, args)
						} else {
							err = executeNativeStartWithController("hub", verb, args, factory)
						}
					}()
					ctx := testutil.DeadlineContext(t)
					select {
					case <-done:
					case <-ctx.Done():
						_ = writer.Close()
						<-done
						t.Fatal("noninteractive setup tried to read an open input pipe")
					}
				})
				require.Error(t, err)
				assert.Contains(t, err.Error(), "--yes")
				if !startup {
					assert.Contains(t, err.Error(), "--startup")
				}
				assert.NotContains(t, output, "Choose [1]")
				assert.NotContains(t, output, "Apply this configuration?")
				assert.Empty(t, manager.installs)
				assert.Empty(t, manager.starts)
			})
		}
	}
}

// TestConsentAndNoninteractiveSetup drives the interactive prompts and
// the noninteractive refusals: y/yes/mixed case accept; empty input,
// EOF, and a cancelled read decline; a noninteractive caller missing
// the startup choice fails without mutating anything.
func TestConsentAndNoninteractiveSetup(t *testing.T) {
	t.Run("y and yes accept, case-insensitively", func(t *testing.T) {
		for _, answer := range []string{"y\n", "yes\n", "Y\n", "YES\n", "Yes\n"} {
			accepted, err := promptApply(&bytes.Buffer{}, strings.NewReader(answer))
			require.NoError(t, err)
			assert.True(t, accepted, "answer %q accepts", strings.TrimSpace(answer))
		}
	})

	t.Run("empty input declines", func(t *testing.T) {
		accepted, err := promptApply(&bytes.Buffer{}, strings.NewReader("\n"))
		require.NoError(t, err)
		assert.False(t, accepted)
	})

	t.Run("end of input declines", func(t *testing.T) {
		accepted, err := promptApply(&bytes.Buffer{}, strings.NewReader(""))
		require.NoError(t, err, "EOF declines rather than failing")
		assert.False(t, accepted)
	})

	t.Run("anything else declines", func(t *testing.T) {
		accepted, err := promptApply(&bytes.Buffer{}, strings.NewReader("no\n"))
		require.NoError(t, err)
		assert.False(t, accepted)
	})

	t.Run("the startup choice defaults to login", func(t *testing.T) {
		var out bytes.Buffer
		choice, err := promptStartupChoice(&out, strings.NewReader("\n"))
		require.NoError(t, err)
		assert.Equal(t, "login", string(choice))
		assert.Contains(t, out.String(), "When you log in")
		assert.Contains(t, out.String(), "When the computer starts")
	})

	t.Run("choice 2 selects boot", func(t *testing.T) {
		choice, err := promptStartupChoice(&bytes.Buffer{}, strings.NewReader("2\n"))
		require.NoError(t, err)
		assert.Equal(t, "boot", string(choice))
	})

	t.Run("an unknown choice refuses", func(t *testing.T) {
		_, err := promptStartupChoice(&bytes.Buffer{}, strings.NewReader("7\n"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "1 or 2")
	})

	t.Run("end of input on the startup choice identifies the flags", func(t *testing.T) {
		_, err := promptStartupChoice(&bytes.Buffer{}, strings.NewReader(""))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--startup")
	})

	t.Run("a noninteractive enable with no saved choice fails without mutation", func(t *testing.T) {
		// --yes must not select a startup mode when none is configured:
		// the verb fails with the exact options needed and writes no
		// specification.
		dataDir := t.TempDir()
		err := runModeCommand("worker", []string{"enable", "--yes", "--data-dir", dataDir})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--startup")
		assert.NoFileExists(t, dataDir+"/.lifecycle/service.yaml",
			"the refusal changed nothing")
	})
}
