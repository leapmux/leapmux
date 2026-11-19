package zcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

const (
	zcodeStoragePathMethod     = "startup/storagePath"
	zcodeStorageReadyMethod    = "startup/storagePathReady"
	zcodeStoragePreparedMethod = "startup/storagePrepared"
	zcodeStorageQueryTimeout   = 30 * time.Second
	zcodeStorageQueryOutput    = 256 << 10
)

type zcodeStoragePathQuery interface {
	DatabasePath(context.Context, agent.StoredSessionQuery) (string, error)
}

type zcodeNativeStorageQuery struct {
	command func(context.Context, agent.StoredSessionQuery) (*launch.PreparedLaunch, error)
	clock   quartz.Clock
}

// newZCodeStorageQuery reads native config through the official preparation command.
// A reuse acknowledgement prevents the command from opening or changing the database.
func newZCodeStorageQuery(resolved *launch.Spec) zcodeNativeStorageQuery {
	return zcodeNativeStorageQuery{command: func(ctx context.Context, q agent.StoredSessionQuery) (*launch.PreparedLaunch, error) {
		environment := q.Environ()
		if environment == nil {
			return nil, errors.New("the ZCode storage query requires an ordered environment")
		}
		shell := q.Shell
		if shell == "" {
			shell = terminal.ResolveDefaultShell()
		}
		var spec launch.Spec
		if resolved == nil {
			locator := Registration().Locator
			if q.RuntimeLocator != nil {
				if !q.RuntimeLocator.Valid() {
					return nil, errors.New("the ZCode storage query has an invalid runtime locator")
				}
				locator = *q.RuntimeLocator
			}
			var err error
			spec, err = locator.Resolve(ctx, shell, q.LoginShell, "ZCode")
			if err != nil {
				return nil, err
			}
		} else {
			spec = *resolved
		}
		// A running process supplies its finalized environment. Discovery
		// without a snapshot still needs the locator defaults and the query home.
		if q.EnvEntries == nil {
			if q.HomeDir != "" {
				environment = append(environment, "HOME="+q.HomeDir, "USERPROFILE="+q.HomeDir)
			}
		} else {
			spec.Env = nil
		}
		return launch.Wrap(ctx, launch.WrapSpec{
			Shell: shell, LoginShell: q.LoginShell, Launch: spec,
			BaseArgs: []string{"app-server", "--stdio", "--prepare-storage"}, WorkingDir: q.WorkingDir, Environment: environment,
		})
	}}
}

func (q zcodeNativeStorageQuery) DatabasePath(ctx context.Context, query agent.StoredSessionQuery) (path string, resultErr error) {
	if ctx == nil || q.command == nil {
		return "", errors.New("the ZCode storage query has no context or command")
	}
	ctx, cancel := context.WithTimeout(ctx, zcodeStorageQueryTimeout)
	defer cancel()
	run, err := q.command(ctx, query)
	if err != nil {
		return "", err
	}
	pipes, err := providerkit.SetupLaunchPipes(run, cancel)
	if err != nil {
		return "", err
	}
	clock := q.clock
	if clock == nil {
		clock = quartz.NewReal()
	}
	if err := pipes.Start(clock); err != nil {
		return "", errors.Join(err, pipes.Close())
	}
	diagnostics := run.NewDiagnostics(64 << 10)
	stderrDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(diagnostics, pipes.Stderr())
		diagnostics.Finish()
		stderrDone <- err
	}()
	closed := false
	defer func() {
		if !closed {
			resultErr = errors.Join(resultErr, pipes.Close())
		}
		if resultErr != nil {
			path = ""
		}
	}()

	response, readErr := readZCodeStorageResponse(ctx, pipes, run.Delimiter)
	var cleanupErr error
	if readErr != nil && !pipes.NativeExitObserved() {
		// A live process can be blocked writing to stdout after a local
		// parse or size failure. Stop it before waiting for its status.
		cancel()
		cleanupErr = pipes.Close()
		closed = true
	}
	nativeErr := pipes.Wait()
	stderrErr := <-stderrDone
	launchErr := diagnostics.ExitError(nativeErr, response.ready)
	if launchErr != nil {
		launchErr = fmt.Errorf("the ZCode storage query failed: %w: %s", launchErr, strings.TrimSpace(diagnostics.String()))
	}
	if errors.Is(launchErr, launch.ErrNoCandidate) {
		return "", errors.Join(launchErr, cleanupErr)
	}
	if readErr == nil && (!response.ready || !response.acknowledged) {
		readErr = errors.New("the ZCode storage query ended without a complete acknowledgement")
	}
	if errors.Is(stderrErr, os.ErrClosed) {
		stderrErr = nil
	} else if stderrErr != nil {
		stderrErr = fmt.Errorf("read the ZCode storage diagnostic: %w", stderrErr)
	}
	return response.path, errors.Join(readErr, launchErr, stderrErr, cleanupErr)
}

type zcodeStorageResponse struct {
	path         string
	ready        bool
	acknowledged bool
}

func readZCodeStorageResponse(ctx context.Context, pipes *providerkit.ProcessPipes, delimiter string) (zcodeStorageResponse, error) {
	limited := &io.LimitedReader{R: pipes.Stdout(), N: zcodeStorageQueryOutput + 1}
	scanner := agent.NewStdoutScanner(limited)
	response := zcodeStorageResponse{ready: delimiter == ""}
	for {
		hasLine := scanner.Scan()
		if err := scanner.Err(); err != nil {
			// The process owner closes stdout after the native exit if a
			// descendant still holds the pipe. Keep that exit and discard any
			// partial record that the scanner returns with the close error.
			return response, fmt.Errorf("read the ZCode storage query: %w", err)
		}
		if limited.N == 0 {
			return response, errors.New("the ZCode storage query exceeds the output limit")
		}
		if !hasLine {
			return response, nil
		}
		line := scanner.Bytes()
		if !response.ready {
			if string(line) == delimiter {
				response.ready = true
			}
			continue
		}
		var record struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(line, &record) != nil {
			return response, errors.New("the ZCode storage query returned invalid JSON")
		}
		switch record.Method {
		case zcodeStoragePathMethod:
			var fields map[string]json.RawMessage
			var nativePath string
			if response.path != "" || response.acknowledged || json.Unmarshal(record.Params, &fields) != nil || len(fields) != 1 ||
				json.Unmarshal(fields["path"], &nativePath) != nil || !validZCodeStoragePath(nativePath) {
				return response, errors.New("the ZCode storage query returned an invalid or repeated path")
			}
			response.path = nativePath
			if err := pipes.Capture(ctx); err != nil {
				return response, err
			}
			if err := json.NewEncoder(pipes.Stdin()).Encode(struct {
				Method string `json:"method"`
				Reuse  bool   `json:"reuse"`
			}{Method: zcodeStorageReadyMethod, Reuse: true}); err != nil {
				return response, err
			}
		case zcodeStoragePreparedMethod:
			var fields map[string]json.RawMessage
			if response.path == "" || response.acknowledged || json.Unmarshal(record.Params, &fields) != nil || fields == nil || len(fields) != 0 {
				return response, errors.New("the ZCode storage acknowledgement has no unique preceding path")
			}
			response.acknowledged = true
		default:
			return response, errors.New("the ZCode storage query returned an unexpected record")
		}
	}
}

func validZCodeStoragePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsRune(path, '\x00')
}
