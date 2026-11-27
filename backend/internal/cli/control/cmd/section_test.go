package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
)

func TestResolveSection_QualifiersAndPrefixes(t *testing.T) {
	sections := []*leapmuxv1.Section{
		{Id: "sec-alpha-1111", Name: "Client Work"},
		{Id: "sec-alpha-2222", Name: "In progress"},
		{Id: "sec-beta-3333", Name: "Duplicate"},
		{Id: "sec-gamma-4444", Name: "duplicate"},
		{Id: "s1", Name: "Short"},
	}

	t.Run("rejects empty and whitespace-only qualifiers", func(t *testing.T) {
		for _, input := range []string{"", "   ", "id:", "id:   ", "name:", "name:  ", "title:", "title:\t"} {
			_, err := resolveSection(sections, input)
			require.Error(t, err, "input %q should fail", input)
			assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code, "input %q should return usage error", input)
		}
	})

	t.Run("id: qualifier matches exact id or unique prefix and never matches names", func(t *testing.T) {
		sec, err := resolveSection(sections, "id:  sec-alpha-1111  ")
		require.NoError(t, err)
		assert.Equal(t, "sec-alpha-1111", sec.GetId())

		sec, err = resolveSection(sections, "id:s1")
		require.NoError(t, err)
		assert.Equal(t, "s1", sec.GetId())

		sec, err = resolveSection(sections, "id:sec-beta")
		require.NoError(t, err)
		assert.Equal(t, "sec-beta-3333", sec.GetId())

		_, err = resolveSection(sections, "id:sec-alpha")
		require.Error(t, err)
		e := cli.FromError(err)
		assert.Equal(t, cli.CodeAmbiguousTarget, e.Code)
		assert.Len(t, e.Candidates, 2)

		_, err = resolveSection(sections, "id:sec")
		require.Error(t, err)
		assert.Equal(t, cli.CodeNotFound, cli.FromError(err).Code)

		_, err = resolveSection(sections, "id:Client Work")
		require.Error(t, err)
		assert.Equal(t, cli.CodeNotFound, cli.FromError(err).Code)
	})

	t.Run("trims spaces after name: and title: qualifiers", func(t *testing.T) {
		sec, err := resolveSection(sections, "name:  client work ")
		require.NoError(t, err)
		assert.Equal(t, "sec-alpha-1111", sec.GetId())

		sec, err = resolveSection(sections, "title:  IN PROGRESS ")
		require.NoError(t, err)
		assert.Equal(t, "sec-alpha-2222", sec.GetId())
	})

	t.Run("resolves exact ID, unique prefix, ambiguous prefix, and bare name fallback", func(t *testing.T) {
		sec, err := resolveSection(sections, "sec-beta-3333")
		require.NoError(t, err)
		assert.Equal(t, "sec-beta-3333", sec.GetId())

		sec, err = resolveSection(sections, "sec-beta")
		require.NoError(t, err)
		assert.Equal(t, "sec-beta-3333", sec.GetId())

		_, err = resolveSection(sections, "sec-alpha")
		require.Error(t, err)
		e := cli.FromError(err)
		assert.Equal(t, cli.CodeAmbiguousTarget, e.Code)
		assert.Len(t, e.Candidates, 2)

		sec, err = resolveSection(sections, "client work")
		require.NoError(t, err)
		assert.Equal(t, "sec-alpha-1111", sec.GetId())

		_, err = resolveSection(sections, "duplicate")
		require.Error(t, err)
		e = cli.FromError(err)
		assert.Equal(t, cli.CodeAmbiguousTarget, e.Code)
		assert.Len(t, e.Candidates, 2)
	})
}

func TestParseSidebarFlag(t *testing.T) {
	got, err := parseSidebarFlag(" left ")
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.Sidebar_SIDEBAR_LEFT, got)

	got, err = parseSidebarFlag("RIGHT")
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.Sidebar_SIDEBAR_RIGHT, got)

	for _, bad := range []string{"", "   ", "top", "unspecified"} {
		_, err := parseSidebarFlag(bad)
		require.Error(t, err)
		assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
	}
}

func TestSectionMove_ValidatesFlagsBeforeDialing(t *testing.T) {
	hub := &recordingHub{}
	startSpawnIPC(t, hub, nil)

	t.Run("missing placement and sidebar fails before dialing", func(t *testing.T) {
		o := runControl(t, "section", "move", "sec-1")
		o.requireFailure(t, cli.CodeUsage)
		assert.Empty(t, hub.called())
	})

	t.Run("conflicting placement flags fail before dialing", func(t *testing.T) {
		o := runControl(t, "section", "move", "sec-1", "--first", "--last")
		o.requireFailure(t, cli.CodeUsage)
		assert.Empty(t, hub.called())
	})

	t.Run("empty --before fails before dialing", func(t *testing.T) {
		o := runControl(t, "section", "move", "sec-1", "--before", "   ")
		o.requireFailure(t, cli.CodeUsage)
		assert.Empty(t, hub.called())
	})

	t.Run("invalid --sidebar fails before dialing", func(t *testing.T) {
		o := runControl(t, "section", "move", "sec-1", "--sidebar", "center")
		o.requireFailure(t, cli.CodeUsage)
		assert.Empty(t, hub.called())
	})
}
