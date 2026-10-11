package procutil

import (
	"context"
	"errors"
	"fmt"

	"github.com/shirou/gopsutil/v4/process"
)

type processVerification interface {
	identity(int) (ProcessIdentity, bool)
	parent(context.Context, int) (int, error)
}

type nativeProcessVerification struct{ table *ProcessTable }

func (nativeProcessVerification) identity(pid int) (ProcessIdentity, bool) {
	return IdentifyProcess(pid)
}

func (v nativeProcessVerification) parent(ctx context.Context, pid int) (int, error) {
	candidate := v.table.byPID[pid]
	if candidate == nil {
		return 0, process.ErrorProcessNotRunning
	}
	parent, err := candidate.PpidWithContext(ctx)
	return int(parent), err
}

// VerifiedForest verifies one snapshot below previously owned live process identities.
// A retained child can remain a root after its original parent exits.
func (t *ProcessTable) VerifiedForest(ctx context.Context, roots []ProcessIdentity) ([]ProcessIdentity, error) {
	if t == nil {
		return nil, errors.New("the process forest has no table")
	}
	if ctx == nil {
		return nil, errors.New("the process verification context is absent")
	}
	return t.verifyForest(ctx, roots, nativeProcessVerification{table: t})
}

func sameProcess(v processVerification, expected ProcessIdentity) bool {
	current, exists := v.identity(expected.PID)
	return exists && current == expected
}

func (t *ProcessTable) verifyForest(ctx context.Context, roots []ProcessIdentity, verifier processVerification) ([]ProcessIdentity, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	known := make(map[int]ProcessIdentity, len(roots))
	seen := make(map[int]struct{}, len(roots))
	generation := make([]int, 0, len(roots))
	for _, root := range roots {
		if root.IsZero() || root.StartTime <= 0 {
			return nil, errors.New("the process forest contains an invalid root identity")
		}
		if _, exists := seen[root.PID]; exists || !sameProcess(verifier, root) {
			continue
		}
		known[root.PID] = root
		seen[root.PID] = struct{}{}
		generation = append(generation, root.PID)
	}
	var owned []ProcessIdentity
	var failures error
	for len(generation) > 0 {
		var next []int
		for _, parent := range generation {
			for _, child := range t.byParent[parent] {
				if err := ctx.Err(); err != nil {
					return owned, errors.Join(failures, err)
				}
				if child <= 0 {
					continue
				}
				if _, visited := seen[child]; visited {
					continue
				}
				seen[child] = struct{}{}
				identity, exists := verifier.identity(child)
				if !exists {
					continue
				}
				currentParent, err := verifier.parent(ctx, child)
				if err != nil {
					if !processGone(child, err) {
						failures = errors.Join(failures, fmt.Errorf("read the parent of process %d: %w", child, err))
					}
					continue
				}
				// An ended original process grants no new ownership and adds no exit error.
				if !sameProcess(verifier, known[parent]) || !sameProcess(verifier, identity) {
					continue
				}
				if currentParent != parent {
					failures = errors.Join(failures, fmt.Errorf("process %d changed its captured parent", child))
					continue
				}
				known[child] = identity
				owned = append(owned, identity)
				next = append(next, child)
			}
		}
		generation = next
	}
	return owned, failures
}
