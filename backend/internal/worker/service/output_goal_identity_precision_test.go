package service

import (
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func goalCreationTimePrecisionCases() []struct {
	name string
	time time.Time
} {
	providerZone := time.FixedZone("provider", 9*60*60)
	return []struct {
		name string
		time time.Time
	}{
		{name: "after epoch", time: time.Unix(1700000000, 123456789).In(providerZone)},
		{name: "before epoch", time: time.Unix(-123, 123456789).In(providerZone)},
		{name: "one nanosecond before epoch", time: time.Unix(-1, 999999999).In(providerZone)},
	}
}

func TestGoalCreationTimePrecisionPublishesFirstProgress(t *testing.T) {
	for _, test := range goalCreationTimePrecisionCases() {
		t.Run(test.name, func(t *testing.T) {
			svc, _, writer, services, _ := goalPublicationFixture(t)
			update := goalPublicationUpdate("Precise goal", 23)
			update.NativeID, update.CreatedAt = "", test.time
			before := len(decodeAgentEvents(writer.testResponseWriter))
			services.UpsertGoal(update)
			row := mustGoalRow(t, svc)
			require.True(t, row.GoalCreatedAt.Valid)
			assert.Equal(t, sqltime.FloorMillis(test.time), row.GoalCreatedAt.Time)
			assert.Equal(t, []string{"goal:Precise goal", "notification:Precise goal", "progress:23"},
				goalPublicationStages(t, decodeAgentEvents(writer.testResponseWriter)[before:]),
				"the original accepted progress must use the actual stored timestamp precision")
		})
	}
}

func TestGoalCreationTimePrecisionDeduplicatesRepeatedReports(t *testing.T) {
	for _, test := range goalCreationTimePrecisionCases() {
		t.Run(test.name, func(t *testing.T) {
			svc, _, writer, services, _ := goalPublicationFixture(t)
			update := goalPublicationUpdate("Standing precise goal", 23)
			update.NativeID, update.CreatedAt = "", test.time
			services.UpsertGoal(update)
			before := storedGoalColumns(t, svc)
			beforeEvents := len(decodeAgentEvents(writer.testResponseWriter))
			tokens := int64(47)
			update.TokensUsed = &tokens
			services.UpsertGoal(update)
			assert.Equal(t, before, storedGoalColumns(t, svc), "the same reported goal must retain every stored column and stamp")
			assert.Equal(t, 1, goalNotificationCount(t, svc, "goal-publication-owner"))
			assert.Equal(t, []string{"progress:47"}, goalPublicationStages(t, decodeAgentEvents(writer.testResponseWriter)[beforeEvents:]),
				"storage precision must not convert a repeated report into a replacement")
		})
	}
}

func TestGoalCreationTimePrecisionKeepsNativeIdentityPrecedence(t *testing.T) {
	for _, test := range goalCreationTimePrecisionCases() {
		t.Run(test.name, func(t *testing.T) {
			svc, _, writer, services, _ := goalPublicationFixture(t)
			update := goalPublicationUpdate("Native identity goal", 23)
			update.NativeID, update.CreatedAt = "original-native-id", test.time
			services.UpsertGoal(update)
			before := storedGoalColumns(t, svc)
			beforeEvents := len(decodeAgentEvents(writer.testResponseWriter))
			update.CreatedAt = test.time.Add(time.Hour)
			services.UpsertGoal(update)
			assert.Equal(t, before, storedGoalColumns(t, svc), "matching nonempty native IDs remain authoritative over creation time")
			assert.Empty(t, goalPublicationStages(t, decodeAgentEvents(writer.testResponseWriter)[beforeEvents:]))
			assert.Equal(t, 1, goalNotificationCount(t, svc, "goal-publication-owner"))
			update.NativeID, update.CreatedAt = "replacement-native-id", time.Time{}
			services.UpsertGoal(update)
			assert.Equal(t, "replacement-native-id", mustGoalRow(t, svc).GoalNativeID)
			assert.Equal(t, []string{"set", "replaced"}, goalTransitionKinds(t, svc, "goal-publication-owner"),
				"different nonempty native IDs replace a goal even when the incoming creation time is absent")
		})
	}
}

func TestGoalCreationTimePrecisionKeepsAbsentCreationTime(t *testing.T) {
	for _, test := range goalCreationTimePrecisionCases() {
		t.Run(test.name, func(t *testing.T) {
			svc, _, writer, services, _ := goalPublicationFixture(t)
			update := goalPublicationUpdate("Standing precise goal", 23)
			update.NativeID, update.CreatedAt = "", test.time
			services.UpsertGoal(update)
			before := storedGoalColumns(t, svc)
			beforeEvents := len(decodeAgentEvents(writer.testResponseWriter))
			update.CreatedAt = time.Time{}
			tokens := int64(47)
			update.TokensUsed = &tokens
			services.UpsertGoal(update)
			assert.Equal(t, before, storedGoalColumns(t, svc), "an absent incoming creation time preserves the existing identity")
			assert.Equal(t, []string{"progress:47"}, goalPublicationStages(t, decodeAgentEvents(writer.testResponseWriter)[beforeEvents:]))
			assert.Equal(t, 1, goalNotificationCount(t, svc, "goal-publication-owner"))
			assert.Equal(t, agent.GoalStatusActive, before.Status)
		})
	}
}
