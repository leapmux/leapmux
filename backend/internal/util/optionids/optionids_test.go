package optionids

import (
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func TestGroupByID(t *testing.T) {
	groups := []*leapmuxv1.AvailableOptionGroup{
		{Id: Model, CurrentValue: "opus"},
		{Id: Effort, CurrentValue: "high"},
	}

	assert.Equal(t, "opus", GroupByID(groups, Model).GetCurrentValue())
	assert.Nil(t, GroupByID(groups, PermissionMode), "an absent id returns nil")
	assert.Nil(t, GroupByID(nil, Model), "a nil catalog returns nil")
}

func TestCurrentValue(t *testing.T) {
	groups := []*leapmuxv1.AvailableOptionGroup{
		{Id: Model, CurrentValue: "opus"},
		{Id: Effort},
	}

	assert.Equal(t, "opus", CurrentValue(groups, Model))
	assert.Equal(t, "", CurrentValue(groups, Effort), "a present group with no current yields empty")
	assert.Equal(t, "", CurrentValue(groups, PrimaryAgent), "an absent id yields empty (nil-safe)")
}
