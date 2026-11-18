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
	if err := pipes.Start(quartz.NewReal()); err != nil {
		return "", errors.Join(err, pipes.Close())
	}
	diagnostics := run.NewDiagnostics(64 << 10)
	stderrDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(diagnostics, pipes.Stderr())
		diagnostics.Finish()
		stderrDone <- err
	}()
	waited := false
	defer func() {
		if !waited {
			cancel()
			resultErr = errors.Join(resultErr, pipes.Close(), pipes.Wait())
			if err := <-stderrDone; !errors.Is(err, os.ErrClosed) {
				resultErr = errors.Join(resultErr, err)
			}
		} else {
			resultErr = errors.Join(resultErr, pipes.Close())
		}
		if resultErr != nil {
			path = ""
		}
	}()
	limited := &io.LimitedReader{R: pipes.Stdout(), N: zcodeStorageQueryOutput + 1}
	scanner := agent.NewStdoutScanner(limited)
	preamble := run.Delimiter != ""
	prepared := false
	for scanner.Scan() {
		line := scanner.Bytes()
		if preamble {
			if string(line) == run.Delimiter {
				preamble = false
			}
			continue
		}
		var record struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(line, &record) != nil {
			return "", errors.New("the ZCode storage query returned invalid JSON")
		}
		switch record.Method {
		case zcodeStoragePathMethod:
			var fields map[string]json.RawMessage
			var nativePath string
			if path != "" || prepared || json.Unmarshal(record.Params, &fields) != nil || len(fields) != 1 ||
				json.Unmarshal(fields["path"], &nativePath) != nil || !validZCodeStoragePath(nativePath) {
				return "", errors.New("the ZCode storage query returned an invalid or repeated path")
			}
			path = nativePath
			if err := pipes.Capture(ctx); err != nil {
				return "", err
			}
			if err := json.NewEncoder(pipes.Stdin()).Encode(struct {
				Method string `json:"method"`
				Reuse  bool   `json:"reuse"`
			}{Method: zcodeStorageReadyMethod, Reuse: true}); err != nil {
				return "", err
			}
		case zcodeStoragePreparedMethod:
			var fields map[string]json.RawMessage
			if path == "" || prepared || json.Unmarshal(record.Params, &fields) != nil || fields == nil || len(fields) != 0 {
				return "", errors.New("the ZCode storage acknowledgement has no unique preceding path")
			}
			prepared = true
		default:
			return "", errors.New("the ZCode storage query returned an unexpected record")
		}
	}
	nativeErr := pipes.Wait()
	waited = true
	stderrErr := <-stderrDone
	launchErr := diagnostics.ExitError(nativeErr, !preamble)
	if errors.Is(launchErr, launch.ErrNoCandidate) {
		return "", fmt.Errorf("the ZCode storage query failed: %w: %s", launchErr, strings.TrimSpace(diagnostics.String()))
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read the ZCode storage query: %w", err)
	}
	if limited.N == 0 {
		return "", errors.New("the ZCode storage query exceeds the output limit")
	}
	if preamble || path == "" || !prepared {
		return "", errors.New("the ZCode storage query ended without a complete acknowledgement")
	}
	if nativeErr != nil {
		return "", fmt.Errorf("the ZCode storage query failed: %w: %s", nativeErr, strings.TrimSpace(diagnostics.String()))
	}
	if stderrErr != nil && !errors.Is(stderrErr, os.ErrClosed) {
		return "", fmt.Errorf("read the ZCode storage diagnostic: %w", stderrErr)
	}
	return path, nil
}

func validZCodeStoragePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsRune(path, '\x00')
}
