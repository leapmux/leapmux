package procutil

import (
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordedJobProcess struct {
	identity    ProcessIdentity
	runs        bool
	identityErr error
	assignErr   error
	closeErr    error
	steps       *[]string
}

func (p *recordedJobProcess) Identity() (ProcessIdentity, bool, error) {
	*p.steps = append(*p.steps, "identity")
	return p.identity, p.runs, p.identityErr
}
func (p *recordedJobProcess) Assign(uintptr) error {
	*p.steps = append(*p.steps, "assign")
	return p.assignErr
}
func (p *recordedJobProcess) Close() error {
	*p.steps = append(*p.steps, "close-process")
	return p.closeErr
}

type recordedJobDriver struct {
	process      *recordedJobProcess
	createErr    error
	configureErr error
	openErr      error
	terminateErr error
	closeErr     error
	openedPID    int
	steps        []string
}

func (d *recordedJobDriver) CreateJob() (uintptr, error) {
	d.steps = append(d.steps, "create-job")
	if d.createErr != nil {
		return 0, d.createErr
	}
	return 500, nil
}
func (d *recordedJobDriver) ConfigureJob(uintptr) error {
	d.steps = append(d.steps, "configure-job")
	return d.configureErr
}
func (d *recordedJobDriver) OpenProcess(pid int) (heldJobProcess, error) {
	d.steps = append(d.steps, "open-process")
	d.openedPID = pid
	if d.openErr != nil {
		return nil, d.openErr
	}
	d.process.steps = &d.steps
	return d.process, nil
}
func (d *recordedJobDriver) CloseJob(uintptr) error {
	d.steps = append(d.steps, "close-job")
	return d.closeErr
}
func (d *recordedJobDriver) TerminateJob(uintptr) error {
	d.steps = append(d.steps, "terminate-job")
	return d.terminateErr
}

func newRecordedJobDriver(identity ProcessIdentity) *recordedJobDriver {
	return &recordedJobDriver{process: &recordedJobProcess{identity: identity, runs: true}}
}

func TestAssignOwnedJobRequiresTheHeldHandleIdentityBeforeAssignment(t *testing.T) {
	root := ProcessIdentity{PID: 200, StartTime: 1_790_000_000_123}
	driver := newRecordedJobDriver(root)
	job, err := assignOwnedJob(root, driver)
	require.NoError(t, err)
	assert.Equal(t, uintptr(500), job)
	assert.Equal(t, root.PID, driver.openedPID)
	assert.Equal(t, []string{"create-job", "configure-job", "open-process", "identity", "assign", "close-process"}, driver.steps)
}

func TestAssignOwnedJobRefusesAReplacedEndedOrUnreadableHandle(t *testing.T) {
	root := ProcessIdentity{PID: 200, StartTime: 1_790_000_000_123}
	denied := errors.New("the handle identity is unreadable")
	for _, testCase := range []struct {
		label       string
		identity    ProcessIdentity
		runs        bool
		identityErr error
	}{
		{"replaced creation time", ProcessIdentity{PID: root.PID, StartTime: root.StartTime + 1}, true, nil},
		{"different pid", ProcessIdentity{PID: root.PID + 1, StartTime: root.StartTime}, true, nil},
		{"ended process", root, false, nil},
		{"unreadable identity", root, true, denied},
		{"absent identity", ProcessIdentity{}, true, nil},
	} {
		t.Run(testCase.label, func(t *testing.T) {
			driver := newRecordedJobDriver(testCase.identity)
			driver.process.runs, driver.process.identityErr = testCase.runs, testCase.identityErr
			job, err := assignOwnedJob(root, driver)
			require.Error(t, err)
			assert.Zero(t, job)
			assert.NotContains(t, driver.steps, "assign", "an unverified handle must receive no kill-on-close authority")
			assert.Contains(t, driver.steps, "close-job")
			assert.Contains(t, driver.steps, "close-process")
			if testCase.identityErr != nil {
				assert.ErrorIs(t, err, testCase.identityErr)
			}
		})
	}
}

func TestAssignOwnedJobPreservesOperationAndCleanupErrors(t *testing.T) {
	root := ProcessIdentity{PID: 200, StartTime: 1_790_000_000_123}
	for _, stage := range []string{"create", "configure", "open", "assign"} {
		t.Run(stage, func(t *testing.T) {
			driver := newRecordedJobDriver(root)
			operationErr := errors.New("the native operation failed")
			switch stage {
			case "create":
				driver.createErr = operationErr
			case "configure":
				driver.configureErr = operationErr
			case "open":
				driver.openErr = operationErr
			case "assign":
				driver.process.assignErr = operationErr
			}
			job, err := assignOwnedJob(root, driver)
			assert.Zero(t, job)
			require.ErrorIs(t, err, operationErr)
			if stage == "create" {
				assert.Equal(t, []string{"create-job"}, driver.steps)
			}
			if stage == "configure" {
				assert.Equal(t, []string{"create-job", "configure-job", "close-job"}, driver.steps)
			}
			if stage == "open" {
				assert.Equal(t, []string{"create-job", "configure-job", "open-process", "close-job"}, driver.steps)
			}
			if stage == "assign" {
				assert.Equal(t, []string{"create-job", "configure-job", "open-process", "identity", "assign", "close-job", "close-process"}, driver.steps)
			}
		})
	}
	for _, stage := range []string{"configure", "open", "identity"} {
		t.Run(stage+" and job close", func(t *testing.T) {
			driver := newRecordedJobDriver(root)
			operationErr := errors.New("the native operation failed")
			cleanupErr := errors.New("the job handle close failed")
			driver.closeErr = cleanupErr
			switch stage {
			case "configure":
				driver.configureErr = operationErr
			case "open":
				driver.openErr = operationErr
			case "identity":
				driver.process.identityErr = operationErr
			}
			job, err := assignOwnedJob(root, driver)
			assert.Zero(t, job)
			assert.ErrorIs(t, err, operationErr)
			assert.ErrorIs(t, err, cleanupErr)
		})
	}
	for _, stage := range []string{"process handle", "job handle"} {
		t.Run(stage, func(t *testing.T) {
			driver := newRecordedJobDriver(root)
			operationErr, cleanupErr := errors.New("the assignment failed"), errors.New("the handle close failed")
			driver.process.assignErr = operationErr
			if stage == "process handle" {
				driver.process.closeErr = cleanupErr
			} else {
				driver.closeErr = cleanupErr
			}
			_, err := assignOwnedJob(root, driver)
			assert.ErrorIs(t, err, operationErr)
			assert.ErrorIs(t, err, cleanupErr)
		})
	}
}

func TestAssignOwnedJobRefusesInvalidIdentityBeforeAnyNativeOperation(t *testing.T) {
	tooLarge := int64(math.MaxInt32) + 1
	for _, identity := range []ProcessIdentity{
		{},
		{PID: -1, StartTime: 1},
		{PID: int(tooLarge), StartTime: 1},
		{PID: 200, StartTime: 0},
		{PID: 200, StartTime: -1},
	} {
		driver := newRecordedJobDriver(identity)
		job, err := assignOwnedJob(identity, driver)
		require.Error(t, err)
		assert.Zero(t, job)
		assert.Empty(t, driver.steps, "invalid records must grant no native handle authority")
	}
}

func TestAssignOwnedJobClosesTheJobAfterASuccessfulAssignmentButFailedHandleClose(t *testing.T) {
	root := ProcessIdentity{PID: 200, StartTime: 1_790_000_000_123}
	driver := newRecordedJobDriver(root)
	closeErr := errors.New("the process handle close failed")
	driver.process.closeErr = closeErr
	job, err := assignOwnedJob(root, driver)
	require.ErrorIs(t, err, closeErr)
	assert.Zero(t, job)
	assert.Equal(t, []string{"create-job", "configure-job", "open-process", "identity", "assign", "close-process", "close-job"}, driver.steps)
}

func TestTerminateOwnedJobPreservesEveryOperationAndCloseError(t *testing.T) {
	terminateErr := errors.New("the job termination failed")
	closeErr := errors.New("the job handle close failed")
	for _, testCase := range []struct {
		label        string
		terminateErr error
		closeErr     error
	}{
		{"completed", nil, nil},
		{"terminate failure", terminateErr, nil},
		{"close failure", nil, closeErr},
		{"both failures", terminateErr, closeErr},
	} {
		t.Run(testCase.label, func(t *testing.T) {
			driver := newRecordedJobDriver(ProcessIdentity{})
			driver.terminateErr, driver.closeErr = testCase.terminateErr, testCase.closeErr
			err := terminateOwnedJob(500, driver)
			assert.Equal(t, []string{"terminate-job", "close-job"}, driver.steps)
			if testCase.terminateErr == nil && testCase.closeErr == nil {
				require.NoError(t, err)
				return
			}
			if testCase.terminateErr != nil {
				assert.ErrorIs(t, err, testCase.terminateErr)
			}
			if testCase.closeErr != nil {
				assert.ErrorIs(t, err, testCase.closeErr)
			}
		})
	}
}

func TestAssignOwnedJobPreservesTheOperationAndBothHandleCloseErrors(t *testing.T) {
	root := ProcessIdentity{PID: 200, StartTime: 1_790_000_000_123}
	for _, stage := range []string{"identity", "assign", "successful assignment"} {
		t.Run(stage, func(t *testing.T) {
			driver := newRecordedJobDriver(root)
			operationErr := errors.New("the native operation failed")
			processCloseErr := errors.New("the process handle close failed")
			jobCloseErr := errors.New("the job handle close failed")
			driver.process.closeErr, driver.closeErr = processCloseErr, jobCloseErr
			switch stage {
			case "identity":
				driver.process.identityErr = operationErr
			case "assign":
				driver.process.assignErr = operationErr
			}
			job, err := assignOwnedJob(root, driver)
			require.Zero(t, job)
			require.ErrorIs(t, err, processCloseErr)
			require.ErrorIs(t, err, jobCloseErr)
			if stage != "successful assignment" {
				require.ErrorIs(t, err, operationErr)
			}
			var processCloses, jobCloses int
			for _, step := range driver.steps {
				switch step {
				case "close-process":
					processCloses++
				case "close-job":
					jobCloses++
				}
			}
			require.Equal(t, 1, processCloses)
			require.Equal(t, 1, jobCloses)
		})
	}
}
