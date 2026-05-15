package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNoopResolver_ReturnsNilIP(t *testing.T) {
	r := noopResolver{}
	_, ip, err := r.Resolve(context.Background(), "example.com")
	require.NoError(t, err)
	assert.Nil(t, ip, "noopResolver should return nil IP so FQDN is preserved")
}
