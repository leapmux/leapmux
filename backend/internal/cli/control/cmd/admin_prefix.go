package cmd

import (
	"context"
	"sort"
	"strings"

	"connectrpc.com/connect/v2"
	"github.com/google/uuid"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	hubservice "github.com/leapmux/leapmux/internal/hub/service"
)

// resolveAdminIDPrefix resolves an admin entity target that may be a full ID,
// an explicit "id:<literal>" value, a "name:<name>"/"title:<title>" selector,
// a unique ID prefix of at least resolve.MinPrefixLength characters, or a bare
// case-insensitive name/title match. Full IDs and literal "id:" values skip
// listing completely so exact lookups cost no extra round trip.
func resolveAdminIDPrefix(ctx context.Context, raw, kind string, list func(context.Context) ([]cli.Candidate, error)) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if literal, ok := strings.CutPrefix(trimmed, "id:"); ok {
		literal = strings.TrimSpace(literal)
		if literal == "" {
			return "", cli.Usagef(`"id:" needs an id after the colon`)
		}
		return literal, nil
	}
	if name, ok := strings.CutPrefix(trimmed, "name:"); ok {
		return resolveAdminByTitle(ctx, name, "name:", kind, list)
	}
	if title, ok := strings.CutPrefix(trimmed, "title:"); ok {
		return resolveAdminByTitle(ctx, title, "title:", kind, list)
	}
	if resolve.IsGeneratedID(trimmed) || (len(trimmed) == 36 && uuid.Validate(trimmed) == nil) {
		return trimmed, nil
	}
	if trimmed == "" {
		return trimmed, nil
	}
	candidates, err := list(ctx)
	if err != nil {
		return "", err
	}
	var idMatches []cli.Candidate
	var titleMatches []cli.Candidate
	for _, c := range candidates {
		if c.ID == trimmed {
			return c.ID, nil
		}
		if len(trimmed) >= resolve.MinPrefixLength && strings.HasPrefix(c.ID, trimmed) {
			idMatches = append(idMatches, c)
		}
		if c.Title != "" && strings.EqualFold(c.Title, trimmed) {
			titleMatches = append(titleMatches, c)
		}
	}
	if len(idMatches) == 1 {
		return idMatches[0].ID, nil
	}
	if len(idMatches) > 1 {
		sort.Slice(idMatches, func(i, j int) bool { return idMatches[i].ID < idMatches[j].ID })
		e := cli.Newf(cli.CodeAmbiguousTarget, "%d %ss match the id prefix %s", len(idMatches), kind, trimmed).
			WithHint("type more characters of the id, or the full id")
		e.Candidates = idMatches
		return "", e
	}
	if len(titleMatches) == 1 {
		return titleMatches[0].ID, nil
	}
	if len(titleMatches) > 1 {
		sort.Slice(titleMatches, func(i, j int) bool { return titleMatches[i].ID < titleMatches[j].ID })
		e := cli.Newf(cli.CodeAmbiguousTarget, "%d %ss match %q", len(titleMatches), kind, trimmed).
			WithHint("specify an id or unique prefix instead")
		e.Candidates = titleMatches
		return "", e
	}
	// Fall through to the direct RPC so the hub produces its canonical
	// not_found error (and synthetic test IDs still reach the server).
	return trimmed, nil
}

func resolveAdminByTitle(ctx context.Context, raw, prefix, kind string, list func(context.Context) ([]cli.Candidate, error)) (string, error) {
	want := strings.TrimSpace(raw)
	if want == "" {
		return "", cli.Usagef(`%q needs a value after the colon`, prefix)
	}
	candidates, err := list(ctx)
	if err != nil {
		return "", err
	}
	var matches []cli.Candidate
	for _, c := range candidates {
		if c.Title != "" && strings.EqualFold(c.Title, want) {
			matches = append(matches, c)
		}
	}
	switch len(matches) {
	case 0:
		return "", cli.Newf(cli.CodeNotFound, "no %s matches %q", kind, want)
	case 1:
		return matches[0].ID, nil
	default:
		sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
		e := cli.Newf(cli.CodeAmbiguousTarget, "%d %ss match %q", len(matches), kind, want).
			WithHint("specify an id or unique prefix instead")
		e.Candidates = matches
		return "", e
	}
}

// parseAdminUserSelectorDirect parses a user selector without issuing any
// Hub RPC. It also reports whether a NotFound response from the Hub should
// trigger a lazy user ID prefix lookup and what prefix string to search for.
func parseAdminUserSelectorDirect(raw string) (sel *leapmuxv1.UserSelector, prefix string, allowPrefixFallback bool, err error) {
	sel, err = control.ParseUserArgument(raw)
	if err != nil {
		return nil, "", false, err
	}
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "username:") {
		return sel, "", false, nil
	}
	if literal, ok := strings.CutPrefix(trimmed, "id:"); ok {
		literal = strings.TrimSpace(literal)
		if resolve.IsGeneratedID(literal) || (len(literal) == 36 && uuid.Validate(literal) == nil) || len(literal) < resolve.MinPrefixLength {
			return sel, "", false, nil
		}
		return sel, literal, true, nil
	}
	if resolve.IsGeneratedID(trimmed) || (len(trimmed) == 36 && uuid.Validate(trimmed) == nil) {
		return hubservice.SelectUserByID(trimmed), "", false, nil
	}
	if len(trimmed) >= resolve.MinPrefixLength {
		return sel, trimmed, true, nil
	}
	return sel, "", false, nil
}

func isAdminUserNotFound(err error) bool {
	if err == nil {
		return false
	}
	if connect.CodeOf(err) == connect.CodeNotFound {
		return true
	}
	return cli.FromError(err).Code == cli.CodeNotFound
}

// callWithAdminUserSelector invokes call with the directly parsed user selector
// first (costing zero extra RPCs for exact usernames and exact IDs). Only when
// the Hub reports NotFound and the input is eligible as a user ID prefix does
// it scan AdminUserService.ListUsers and retry with the resolved full ID.
func callWithAdminUserSelector[T any](ctx context.Context, c *control.HubClient, raw string, call func(*leapmuxv1.UserSelector) (T, error)) (T, error) {
	var zero T
	sel, prefix, allowPrefixFallback, err := parseAdminUserSelectorDirect(raw)
	if err != nil {
		return zero, err
	}
	resp, err := call(sel)
	if err == nil || !allowPrefixFallback || !isAdminUserNotFound(err) {
		return resp, err
	}
	resolved, resolveErr := matchAdminUserIDPrefix(ctx, c, prefix)
	if resolveErr != nil {
		return zero, resolveErr
	}
	if resolved == "" || resolved == prefix {
		return zero, err
	}
	return call(hubservice.SelectUserByID(resolved))
}

// callWithOptionalAdminUserSelector behaves like callWithAdminUserSelector when
// f has a --user value set, and invokes call(nil) directly when --user is unset.
func callWithOptionalAdminUserSelector[T any](ctx context.Context, c *control.HubClient, f *userFilterFlags, call func(*leapmuxv1.UserSelector) (T, error)) (T, error) {
	if f.Selector() == nil {
		return call(nil)
	}
	return callWithAdminUserSelector(ctx, c, f.User, call)
}

func matchAdminUserIDPrefix(ctx context.Context, c *control.HubClient, prefix string) (string, error) {
	if c == nil {
		return "", nil
	}
	var idMatches []cli.Candidate
	cursor := ""
	for range 20 {
		resp, err := c.AdminUserService().ListUsers(ctx, &leapmuxv1.ListUsersRequest{
			Page: &leapmuxv1.PageRequest{Limit: 100, Cursor: cursor},
		})
		if err != nil {
			// Best-effort: let the caller return the original Hub error.
			return "", nil
		}
		for _, u := range resp.GetUsers() {
			if u.GetId() == prefix {
				return u.GetId(), nil
			}
			if strings.HasPrefix(u.GetId(), prefix) {
				idMatches = append(idMatches, cli.Candidate{ID: u.GetId(), Kind: "user", Title: u.GetUsername()})
			}
		}
		nextCursor := resp.GetPage().GetNextCursor()
		if nextCursor == "" || nextCursor == cursor {
			break
		}
		cursor = nextCursor
	}
	if len(idMatches) == 1 {
		return idMatches[0].ID, nil
	}
	if len(idMatches) > 1 {
		sort.Slice(idMatches, func(i, j int) bool { return idMatches[i].ID < idMatches[j].ID })
		e := cli.Newf(cli.CodeAmbiguousTarget, "%d users match the id prefix %s", len(idMatches), prefix).
			WithHint("type more characters of the id, or the full id")
		e.Candidates = idMatches
		return "", e
	}
	return "", nil
}
