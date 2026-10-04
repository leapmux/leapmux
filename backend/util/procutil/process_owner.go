package procutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"
)

const processCaptureTimeout = 2 * time.Second

// ProcessOwner keeps one command and its verified process tree together.
// It retains children only after their live ancestry and creation identities agree.
// A dead parent cannot grant ownership of an unobserved detached child.
type ProcessOwner struct {
	mu           sync.Mutex
	cmd          processCommand
	root         ProcessIdentity
	children     map[ProcessIdentity]struct{}
	platform     processOwnerPlatform
	started      bool
	closed       bool
	result       error
	waitStarted  bool
	waitFinished bool
	waitDone     chan struct{}
	waitResult   error
}

// PrepareProcess owns cancellation before Start can create a child.
func PrepareProcess(cmd *exec.Cmd) *ProcessOwner {
	owner := &ProcessOwner{children: make(map[ProcessIdentity]struct{})}
	if cmd != nil {
		owner.cmd = execProcessCommand{command: cmd}
		// A plain exec.Command rejects a nonnil Cancel function.
		// Context commands supply a Cancel function before the owner replaces it.
		if cmd.Cancel != nil {
			cmd.Cancel = owner.Cancel
		}
		cmd.WaitDelay = 5 * time.Second
	}
	return owner
}

// OwnStartedProcess accepts only the current verified identity of an already-started child.
func OwnStartedProcess(identity ProcessIdentity) (*ProcessOwner, error) {
	if identity.PID == os.Getpid() {
		return nil, errors.New("the process owner cannot adopt the worker process")
	}
	if identity.IsZero() || !identity.Runs() {
		return nil, errors.New("the process owner requires a live creation identity")
	}
	owner := &ProcessOwner{root: identity, children: make(map[ProcessIdentity]struct{}), started: true}
	if err := owner.platform.attach(owner.root); err != nil {
		return nil, err
	}
	return owner, nil
}

func (o *ProcessOwner) Start() error {
	if o == nil {
		return errors.New("the process owner is absent")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || o.started || o.cmd == nil {
		return errors.New("the process owner cannot start this command")
	}
	if err := o.cmd.start(); err != nil {
		o.result = errors.Join(o.result, err)
		return err
	}
	o.started = true
	process := o.cmd.process()
	identity, identified := IdentifyProcess(process.Pid)
	if !identified {
		err := errors.New("the started process supplied no creation identity")
		if killErr := process.Kill(); killErr != nil {
			err = errors.Join(err, killErr)
		}
		err = errors.Join(err, o.waitLocked())
		o.result = errors.Join(o.result, err)
		return err
	}
	o.root = identity
	if err := o.platform.attach(o.root); err != nil {
		_, killErr := o.root.Kill()
		err = errors.Join(err, killErr, o.waitLocked())
		o.result = errors.Join(o.result, err)
		return err
	}
	return nil
}

// Capture records descendants before stdin closes or any root signal arrives.
func (o *ProcessOwner) Capture(ctx context.Context) error {
	if o == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("the process capture context is absent")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	err := o.captureLocked(ctx)
	o.result = errors.Join(o.result, err)
	return err
}

// PID returns the captured root PID without reading a command that Wait can release.
func (o *ProcessOwner) PID() int {
	if o == nil {
		return 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.root.PID
}

// Wait performs one native wait and gives every caller the same result.
func (o *ProcessOwner) Wait() error {
	if o == nil {
		return errors.New("the process owner is absent")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cmd == nil || !o.started {
		return errors.New("the process owner has no started command")
	}
	return o.waitLocked()
}

// waitLocked releases the lock during the native wait so cancellation can complete.
// The caller holds the lock before this method and after it returns.
func (o *ProcessOwner) waitLocked() error {
	if o.waitStarted {
		done := o.waitDone
		o.mu.Unlock()
		<-done
		o.mu.Lock()
		return o.waitResult
	}
	o.waitStarted, o.waitDone = true, make(chan struct{})
	command := o.cmd
	o.mu.Unlock()
	result := command.wait()
	o.mu.Lock()
	o.waitResult, o.waitFinished = result, true
	close(o.waitDone)
	return result
}

// ProcessState returns the native state after the caller completes Wait.
func (o *ProcessOwner) ProcessState() *os.ProcessState {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cmd == nil || !o.waitFinished {
		return nil
	}
	return o.cmd.state()
}

// BindDescendants records observed children while the original process remains alive.
func (o *ProcessOwner) BindDescendants(ctx context.Context) error {
	if o == nil {
		return errors.New("the process owner is absent")
	}
	if ctx == nil {
		return errors.New("the process binding context is absent")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !o.started || o.closed || o.root.IsZero() || !o.root.Runs() {
		return errors.New("the original process ended before its descendants could be bound")
	}
	err := o.captureLocked(ctx)
	if !o.root.Runs() {
		err = errors.Join(err, errors.New("the original process ended during descendant binding"))
	}
	o.result = errors.Join(o.result, err)
	return err
}

func (o *ProcessOwner) captureLocked(ctx context.Context) error {
	if !o.started || o.root.IsZero() || o.closed {
		return nil
	}
	roots := make([]ProcessIdentity, 0, len(o.children)+1)
	if o.root.Runs() {
		roots = append(roots, o.root)
	}
	for child := range o.children {
		if child.Runs() {
			roots = append(roots, child)
		}
	}
	if len(roots) == 0 {
		return nil
	}
	table, snapshotErr := SnapshotProcessTable(ctx)
	if snapshotErr != nil {
		snapshotErr = fmt.Errorf("capture descendants of process %d: %w", o.root.PID, snapshotErr)
	}
	if table == nil {
		return snapshotErr
	}
	children, err := table.VerifiedForest(ctx, roots)
	for _, child := range children {
		o.children[child] = struct{}{}
	}
	return errors.Join(snapshotErr, err)
}

// Cancel captures ownership before it signals descendants and then the root.
func (o *ProcessOwner) Cancel() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || !o.started {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), processCaptureTimeout)
	err := o.captureLocked(ctx)
	cancel()
	err = errors.Join(err, o.killChildrenLocked(), o.platform.cancel(o.root))
	o.result = errors.Join(o.result, err)
	return err
}

// Terminate stops verified descendants before the original group loses its root.
func (o *ProcessOwner) Terminate() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return o.result
	}
	ctx, cancel := context.WithTimeout(context.Background(), processCaptureTimeout)
	err := o.captureLocked(ctx)
	cancel()
	err = errors.Join(err, o.killChildrenLocked(), o.platform.terminate(o.root))
	o.closed = true
	o.result = errors.Join(o.result, err)
	return o.result
}

// Close retains verified identities after reparenting and releases the owner's resources.
func (o *ProcessOwner) Close() error { return o.Terminate() }

func (o *ProcessOwner) Err() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.result
}

func (o *ProcessOwner) killChildrenLocked() error {
	var failures error
	for child := range o.children {
		if _, err := child.Kill(); err != nil {
			failures = errors.Join(failures, fmt.Errorf("stop owned process %d created at %d: %w", child.PID, child.StartTime, err))
		}
	}
	return failures
}
