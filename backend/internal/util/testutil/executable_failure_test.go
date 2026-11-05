package testutil_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

// A failed write ends the goroutine of its test. If the fork lock stayed held,
// every later fork of the test binary would wait for ever, in a test that has
// nothing to do with the failed write.
func TestWriteExecutableKeepsForksRunningAfterAFailedWrite(t *testing.T) {
	t.Parallel()
	rec := &failRecorder{TB: t}
	path := filepath.Join(t.TempDir(), "missing", "program")
	done := make(chan struct{})
	go func() {
		defer close(done)
		testutil.WriteExecutable(rec, path, "#!/bin/sh\n")
	}()
	<-done
	assert.True(t, rec.failed, "a write into a missing directory must fail the test")

	forked := make(chan error, 1)
	go func() { forked <- exec.Command(os.Args[0], "-test.run=^$").Run() }()
	select {
	case err := <-forked:
		assert.NoError(t, err)
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the failed write still keeps every fork out")
	}
}
