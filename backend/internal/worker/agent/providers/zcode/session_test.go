package zcode

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestZCodeIsSessionGoneRequiresTheCurrentMissingSessionError(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		err  error
		gone bool
	}{
		{name: "the session is absent", err: &zcodeError{Code: ErrSessionNotActive}, gone: true},
		{name: "a required method is absent", err: &zcodeError{Code: ErrMethodNotFound}},
		{name: "another native refusal", err: &zcodeError{Code: ErrInternal}},
		{name: "a transport failure", err: errors.New("transport closed")},
		{name: "no failure"},
	} {
		t.Run(test.name, func(t *testing.T) { t.Parallel(); assert.Equal(t, test.gone, zcodeIsSessionGone(test.err)) })
	}
}
