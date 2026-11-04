package cmd

import (
	"errors"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func identity(s string) string { return s }

// nextPage builds the page response that carries next as its cursor.
func nextPage(next string) *leapmuxv1.PageResponse {
	return &leapmuxv1.PageResponse{NextCursor: next}
}

func TestReadAllPagesFollowsTheCursorUntilTheListingEnds(t *testing.T) {
	var requests []*leapmuxv1.PageRequest
	rows, err := readAllPages(func(page *leapmuxv1.PageRequest) ([]string, *leapmuxv1.PageResponse, error) {
		requests = append(requests, page)
		if page.GetCursor() == "" {
			return []string{"a", "b"}, nextPage("c1"), nil
		}
		return []string{"c"}, nextPage(""), nil
	}, identity)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, rows)
	require.Len(t, requests, 2)
	assert.Empty(t, requests[0].GetCursor(), "the first page starts at no cursor")
	assert.Equal(t, "c1", requests[1].GetCursor(), "the second page starts at the first page's next cursor")
}

// Every page request asks for the hub's maximum page. A request that omits the
// limit resolves to the hub's small default, which costs ten times the
// requests for the same listing. readAllPages builds the request, so no caller
// can omit it.
func TestReadAllPagesAsksForTheMaximumPage(t *testing.T) {
	var limits []int32
	_, err := readAllPages(func(page *leapmuxv1.PageRequest) ([]string, *leapmuxv1.PageResponse, error) {
		limits = append(limits, page.GetLimit())
		if page.GetCursor() == "" {
			return []string{"a"}, nextPage("c1"), nil
		}
		return []string{"b"}, nil, nil
	}, identity)
	require.NoError(t, err)
	assert.Equal(t, []int32{listPageSize, listPageSize}, limits)
}

// A page with no page response ends the listing, as an empty next cursor does.
func TestReadAllPagesEndsAtAPageWithNoPageResponse(t *testing.T) {
	calls := 0
	rows, err := readAllPages(func(*leapmuxv1.PageRequest) ([]string, *leapmuxv1.PageResponse, error) {
		calls++
		return []string{"a"}, nil, nil
	}, identity)
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
	assert.Equal(t, []string{"a"}, rows)
}

func TestReadAllPagesReturnsNoRowsForAnEmptyListing(t *testing.T) {
	rows, err := readAllPages(func(*leapmuxv1.PageRequest) ([]string, *leapmuxv1.PageResponse, error) {
		return nil, nextPage(""), nil
	}, identity)
	require.NoError(t, err)
	assert.Empty(t, rows)
	assert.NotNil(t, rows, "an empty listing prints [], not null")
}

// versionedRow is a listing row whose key can repeat with a different value,
// so a test can tell which copy of a repeated row the listing kept.
type versionedRow struct {
	key     string
	version int
}

func versionedRowKey(r versionedRow) string { return r.key }

// A keyset boundary can repeat a row across two pages. The row prints once, at
// the position of its first copy and with the value of its last, which is the
// rule the browser's fetchAllPages keeps too: one listing must not print a
// different value for a row in the CLI and in the browser.
func TestReadAllPagesKeepsTheLastCopyOfARepeatedRowAtItsFirstPosition(t *testing.T) {
	rows, err := readAllPages(func(page *leapmuxv1.PageRequest) ([]versionedRow, *leapmuxv1.PageResponse, error) {
		if page.GetCursor() == "" {
			return []versionedRow{{"a", 1}, {"b", 1}}, nextPage("c1"), nil
		}
		return []versionedRow{{"b", 2}, {"c", 1}}, nextPage(""), nil
	}, versionedRowKey)
	require.NoError(t, err)
	assert.Equal(t, []versionedRow{{"a", 1}, {"b", 2}, {"c", 1}}, rows)
}

// A row that repeats inside one page follows the same rule.
func TestReadAllPagesKeepsTheLastCopyOfARowRepeatedInOnePage(t *testing.T) {
	rows, err := readAllPages(func(*leapmuxv1.PageRequest) ([]versionedRow, *leapmuxv1.PageResponse, error) {
		return []versionedRow{{"a", 1}, {"b", 1}, {"a", 2}}, nextPage(""), nil
	}, versionedRowKey)
	require.NoError(t, err)
	assert.Equal(t, []versionedRow{{"a", 2}, {"b", 1}}, rows)
}

// A guard that stops the loop before the hub ended the listing fails the read.
// Printing the rows read so far would present part of the listing as all of it,
// as a failed page read would.
func TestReadAllPagesFailsWhenTheCursorDoesNotAdvance(t *testing.T) {
	calls := 0
	rows, err := readAllPages(func(*leapmuxv1.PageRequest) ([]string, *leapmuxv1.PageResponse, error) {
		calls++
		return []string{"a"}, nextPage("stuck"), nil
	}, identity)
	require.ErrorIs(t, err, errListingIncomplete)
	assert.Nil(t, rows, "an incomplete listing returns no partial rows")
	assert.Equal(t, 2, calls)
}

func TestReadAllPagesFailsAtTheRunawayGuard(t *testing.T) {
	calls := 0
	rows, err := readAllPages(func(*leapmuxv1.PageRequest) ([]string, *leapmuxv1.PageResponse, error) {
		calls++
		return []string{strconv.Itoa(calls)}, nextPage("c" + strconv.Itoa(calls)), nil
	}, identity)
	require.ErrorIs(t, err, errListingIncomplete)
	assert.Nil(t, rows, "an incomplete listing returns no partial rows")
	assert.Equal(t, maxListPages, calls)
}

// A cursor that alternates between two values always advances from the
// previous one, so only the page guard ends the loop.
func TestReadAllPagesFailsOnACursorThatAlternates(t *testing.T) {
	calls := 0
	rows, err := readAllPages(func(page *leapmuxv1.PageRequest) ([]string, *leapmuxv1.PageResponse, error) {
		calls++
		if page.GetCursor() == "a" {
			return []string{"x"}, nextPage("b"), nil
		}
		return []string{"y"}, nextPage("a"), nil
	}, identity)
	require.ErrorIs(t, err, errListingIncomplete)
	assert.Nil(t, rows)
	assert.Equal(t, maxListPages, calls)
}

// The last page the guard allows still ends the listing when its cursor is
// empty: the guard fails only a listing that the hub did not end.
func TestReadAllPagesAcceptsAListingThatEndsOnTheLastAllowedPage(t *testing.T) {
	calls := 0
	rows, err := readAllPages(func(*leapmuxv1.PageRequest) ([]string, *leapmuxv1.PageResponse, error) {
		calls++
		if calls == maxListPages {
			return []string{strconv.Itoa(calls)}, nextPage(""), nil
		}
		return []string{strconv.Itoa(calls)}, nextPage("c" + strconv.Itoa(calls)), nil
	}, identity)
	require.NoError(t, err)
	assert.Len(t, rows, maxListPages)
	assert.Equal(t, maxListPages, calls)
}

func TestReadAllPagesReturnsAFailedPageRead(t *testing.T) {
	boom := errors.New("offline")
	rows, err := readAllPages(func(page *leapmuxv1.PageRequest) ([]string, *leapmuxv1.PageResponse, error) {
		if page.GetCursor() == "" {
			return []string{"a"}, nextPage("c1"), nil
		}
		return nil, nil, boom
	}, identity)
	require.ErrorIs(t, err, boom)
	assert.Nil(t, rows, "a failed read returns no partial listing")
}
