package errwrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsCancellationPreservesIndependentCauses(t *testing.T) {
	storage := errors.New("database close failed")
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{"absent", nil, false},
		{"cancel", context.Canceled, true},
		{"wrapped", fmt.Errorf("startup: %w", context.Canceled), true},
		{"automatic rollback", errors.Join(context.Canceled, sql.ErrTxDone), true},
		{"transaction alone", sql.ErrTxDone, false},
		{"deadline", context.DeadlineExceeded, false},
		{"independent cleanup", errors.Join(context.Canceled, storage), false},
		{"wrapped independent cleanup", fmt.Errorf("shutdown: %w", errors.Join(context.Canceled, sql.ErrTxDone, storage)), false},
	} {
		t.Run(test.name, func(t *testing.T) { assert.Equal(t, test.want, IsCancellation(test.err)) })
	}
}
