package zcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/util/procutil"
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

type zcodeStorageCommand struct {
	command   *exec.Cmd
	delimiter string
}

type zcodeNativeStorageQuery struct {
	command func(context.Context, agent.StoredSessionQuery) (zcodeStorageCommand, error)
}

// newZCodeStorageQuery reads native config through the official preparation command.
// A reuse acknowledgement prevents the command from opening or changing the database.
func newZCodeStorageQuery(resolved *launch.Spec) zcodeNativeStorageQuery {
	return zcodeNativeStorageQuery{command: func(ctx context.Context, q agent.StoredSessionQuery) (zcodeStorageCommand, error) {
		environment := q.Environ()
		if environment == nil {
			return zcodeStorageCommand{}, errors.New("the ZCode storage query requires an ordered environment")
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
					return zcodeStorageCommand{}, errors.New("the ZCode storage query has an invalid runtime locator")
				}
				locator = *q.RuntimeLocator
			}
			var err error
			spec, err = locator.Resolve(ctx, shell, q.LoginShell, "ZCode")
			if err != nil {
				return zcodeStorageCommand{}, err
			}
		} else {
			spec = *resolved
		}
		cmd, delimiter, _ := launch.Wrap(ctx, launch.WrapSpec{
			Shell: shell, LoginShell: q.LoginShell, Launch: spec,
			BaseArgs: []string{"app-server", "--stdio", "--prepare-storage"}, WorkingDir: q.WorkingDir,
		})
		// A running process supplies its finalized environment as the sole authority.
		// Session discovery without a snapshot still needs the locator's launch variables.
		if q.EnvEntries == nil {
			environment = append(environment, spec.Env...)
			if q.HomeDir != "" {
				environment = append(environment, "HOME="+q.HomeDir, "USERPROFILE="+q.HomeDir)
			}
		}
		cmd.Env = environment
		return zcodeStorageCommand{command: cmd, delimiter: delimiter}, nil
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
	if run.command == nil {
		return "", errors.New("the ZCode storage query has no command")
	}
	cmd := run.command
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	defer func() { resultErr = errors.Join(resultErr, closeZCodeStoragePipe(stdin)) }()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	defer func() { resultErr = errors.Join(resultErr, closeZCodeStoragePipe(stdout)) }()
	var stderr providerkit.LimitedBuffer
	stderr.Limit = 64 << 10
	cmd.Stderr = &stderr
	owner := procutil.PrepareProcess(cmd)
	if err := owner.Start(); err != nil {
		return "", errors.Join(err, owner.Close())
	}
	waited := false
	defer func() {
		if !waited {
			cancel()
		}
		cleanupErr := owner.Close()
		if !waited {
			cleanupErr = errors.Join(cleanupErr, owner.Wait())
		}
		resultErr = errors.Join(resultErr, cleanupErr)
		if resultErr != nil {
			path = ""
		}
	}()
	limited := &io.LimitedReader{R: stdout, N: zcodeStorageQueryOutput + 1}
	scanner := agent.NewStdoutScanner(limited)
	preamble := run.delimiter != ""
	prepared := false
	for scanner.Scan() {
		line := scanner.Bytes()
		if preamble {
			if string(line) == run.delimiter {
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
			if err := owner.Capture(ctx); err != nil {
				return "", err
			}
			if err := json.NewEncoder(stdin).Encode(struct {
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
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read the ZCode storage query: %w", err)
	}
	if limited.N == 0 {
		return "", errors.New("the ZCode storage query exceeds the output limit")
	}
	if preamble || path == "" || !prepared {
		return "", errors.New("the ZCode storage query ended without a complete acknowledgement")
	}
	err = owner.Wait()
	waited = true
	if err != nil {
		return "", fmt.Errorf("the ZCode storage query failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return path, nil
}

func validZCodeStoragePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsRune(path, '\x00')
}

func closeZCodeStoragePipe(pipe io.Closer) error {
	err := pipe.Close()
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}
