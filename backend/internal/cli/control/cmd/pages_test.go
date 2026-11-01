package cmd

import (
	"errors"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func identity(s string) string { return s }

func TestReadAllPagesFollowsTheCursorUntilTheListingEnds(t *testing.T) {
	var cursors []string
	rows, err := readAllPages(func(cursor string) ([]string, string, error) {
		cursors = append(cursors, cursor)
		if cursor == "" {
			return []string{"a", "b"}, "c1", nil
		}
		return []string{"c"}, "", nil
	}, identity)
	require.NoError(t, err)
	assert.Equal(t, []string{"", "c1"}, cursors)
	assert.Equal(t, []string{"a", "b", "c"}, rows)
}

func TestReadAllPagesReturnsNoRowsForAnEmptyListing(t *testing.T) {
	rows, err := readAllPages(func(string) ([]string, string, error) { return nil, "", nil }, identity)
	require.NoError(t, err)
	assert.Empty(t, rows)
	assert.NotNil(t, rows, "an empty listing prints [], not null")
}

// A keyset boundary can repeat a row across two pages. The first copy wins,
// so the row prints once.
func TestReadAllPagesDropsARepeatedRow(t *testing.T) {
	rows, err := readAllPages(func(cursor string) ([]string, string, error) {
		if cursor == "" {
			return []string{"a", "b"}, "c1", nil
		}
		return []string{"b", "c"}, "", nil
	}, identity)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, rows)
}

func TestReadAllPagesStopsWhenTheCursorDoesNotAdvance(t *testing.T) {
	calls := 0
	rows, err := readAllPages(func(string) ([]string, string, error) {
		calls++
		return []string{"a"}, "stuck", nil
	}, identity)
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
	assert.Equal(t, []string{"a"}, rows)
}

func TestReadAllPagesStopsAtTheRunawayGuard(t *testing.T) {
	calls := 0
	_, err := readAllPages(func(string) ([]string, string, error) {
		calls++
		return []string{strconv.Itoa(calls)}, "c" + strconv.Itoa(calls), nil
	}, identity)
	require.NoError(t, err)
	assert.Equal(t, maxListPages, calls)
}

func TestReadAllPagesReturnsAFailedPageRead(t *testing.T) {
	boom := errors.New("offline")
	rows, err := readAllPages(func(cursor string) ([]string, string, error) {
		if cursor == "" {
			return []string{"a"}, "c1", nil
		}
		return nil, "", boom
	}, identity)
	require.ErrorIs(t, err, boom)
	assert.Nil(t, rows, "a failed read returns no partial listing")
}
