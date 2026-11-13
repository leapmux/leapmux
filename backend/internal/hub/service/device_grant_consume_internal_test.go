package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type deviceGrantConsumeStore struct {
	store.Store
	devices *deviceGrantConsumeRecorder
}

func (s deviceGrantConsumeStore) DeviceAuthorizations() store.DeviceAuthorizationStore {
	return s.devices
}

type deviceGrantConsumeRecorder struct {
	store.DeviceAuthorizationStore
	rows   int64
	err    error
	calls  int
	digest []byte
	now    time.Time
}

func (s *deviceGrantConsumeRecorder) Consume(_ context.Context, digest []byte, now time.Time) (int64, error) {
	s.calls++
	s.digest = append([]byte(nil), digest...)
	s.now = now
	return s.rows, s.err
}
func TestSpendDeviceGrantPreservesStoreTimeAndResult(t *testing.T) {
	t.Parallel()
	fault := errors.New("consume fault")
	for _, tc := range []struct {
		name      string
		rows      int64
		err, want error
	}{
		{"one", 1, nil, nil}, {"zero", 0, nil, errAuthorizationGrantUnavailable}, {"negative", -1, nil, errAuthorizationGrantUnavailable}, {"multiple", 2, nil, errAuthorizationGrantUnavailable}, {"store failure", 0, fault, fault},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &deviceGrantConsumeRecorder{rows: tc.rows, err: tc.err}
			selected := deviceGrantConsumeStore{devices: recorder}
			digest := []byte{0, 1, 255}
			now := time.Date(2026, time.November, 13, 4, 5, 6, 7, time.UTC)
			err := spendDeviceGrant(t.Context(), selected, digest, now)
			if tc.want == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.want)
				assert.Same(t, tc.want, err)
			}
			assert.Equal(t, 1, recorder.calls)
			assert.Equal(t, digest, recorder.digest)
			assert.Equal(t, now, recorder.now)
		})
	}
}
