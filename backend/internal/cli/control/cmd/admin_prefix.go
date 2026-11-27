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

// isCompleteAdminID reports whether s already has the shape of a full generated
// identifier or a standard 36-character UUID, so prefix listing can be skipped.
func isCompleteAdminID(s string) bool {
	return resolve.IsGeneratedID(s) || (len(s) == 36 && uuid.Validate(s) == nil)
}

// resolveAdminIDPrefix resolves an admin entity target that supports title
// matching (such as apps, API tokens, and sign-in providers).
func resolveAdminIDPrefix(ctx context.Context, raw, kind string, list func(context.Context) ([]cli.Candidate, error)) (string, error) {
	return resolveAdminTarget(ctx, raw, kind, true, list)
}

// resolveAdminTarget resolves an admin entity target that may be a full ID,
// an explicit "id:<literal>" value, a unique ID prefix of at least
// resolve.MinPrefixLength characters, or (when allowTitleMatch is true) a
// "name:<name>"/"title:<title>" selector or bare case-insensitive title match.
// Full IDs and literal "id:" values skip listing completely so exact lookups
// cost no extra round trip.
func resolveAdminTarget(
	ctx context.Context,
	raw, kind string,
	allowTitleMatch bool,
	list func(context.Context) ([]cli.Candidate, error),
) (string, error) {
	id, matched, err := matchAdminTarget(ctx, raw, kind, allowTitleMatch, list)
	if err != nil {
		return "", err
	}
	if !matched {
		trimmed := strings.TrimSpace(raw)
		if name, ok := strings.CutPrefix(trimmed, "name:"); ok {
			return "", cli.Newf(cli.CodeNotFound, "no %s matches %q", kind, strings.TrimSpace(name))
		}
		if title, ok := strings.CutPrefix(trimmed, "title:"); ok {
			return "", cli.Newf(cli.CodeNotFound, "no %s matches %q", kind, strings.TrimSpace(title))
		}
	}
	return id, nil
}

// matchAdminTarget resolves raw against list and reports whether a candidate
// (or a complete/explicit ID) matched. Callers that query live entries before
// falling back to historical rows use matched=false to trigger the second pass.
func matchAdminTarget(
	ctx context.Context,
	raw, kind string,
	allowTitleMatch bool,
	list func(context.Context) ([]cli.Candidate, error),
) (string, bool, error) {
	trimmed := strings.TrimSpace(raw)
	if literal, ok := strings.CutPrefix(trimmed, "id:"); ok {
		literal = strings.TrimSpace(literal)
		if literal == "" {
			return "", false, cli.Usagef(`"id:" needs an id after the colon`)
		}
		return literal, true, nil
	}
	if name, ok := strings.CutPrefix(trimmed, "name:"); ok {
		if !allowTitleMatch {
			return "", false, cli.Usagef(`%s targets do not support "name:" selectors; specify an id or unique id prefix`, kind)
		}
		return matchAdminByTitle(ctx, name, "name:", kind, list)
	}
	if title, ok := strings.CutPrefix(trimmed, "title:"); ok {
		if !allowTitleMatch {
			return "", false, cli.Usagef(`%s targets do not support "title:" selectors; specify an id or unique id prefix`, kind)
		}
		return matchAdminByTitle(ctx, title, "title:", kind, list)
	}
	if isCompleteAdminID(trimmed) || trimmed == "" {
		return trimmed, true, nil
	}
	candidates, err := list(ctx)
	if err != nil {
		return "", false, err
	}
	var idMatches []cli.Candidate
	var titleMatches []cli.Candidate
	for _, c := range candidates {
		if c.ID == trimmed {
			return c.ID, true, nil
		}
		if len(trimmed) >= resolve.MinPrefixLength && strings.HasPrefix(c.ID, trimmed) {
			idMatches = append(idMatches, c)
		}
		if allowTitleMatch && c.Title != "" && strings.EqualFold(c.Title, trimmed) {
			titleMatches = append(titleMatches, c)
		}
	}
	if len(idMatches) == 1 {
		return idMatches[0].ID, true, nil
	}
	if len(idMatches) > 1 {
		sort.Slice(idMatches, func(i, j int) bool { return idMatches[i].ID < idMatches[j].ID })
		e := cli.Newf(cli.CodeAmbiguousTarget, "%d %ss match the id prefix %s", len(idMatches), kind, trimmed).
			WithHint("type more characters of the id, or the full id")
		e.Candidates = idMatches
		return "", false, e
	}
	if allowTitleMatch {
		if len(titleMatches) == 1 {
			return titleMatches[0].ID, true, nil
		}
		if len(titleMatches) > 1 {
			sort.Slice(titleMatches, func(i, j int) bool { return titleMatches[i].ID < titleMatches[j].ID })
			e := cli.Newf(cli.CodeAmbiguousTarget, "%d %ss match %q", len(titleMatches), kind, trimmed).
				WithHint("specify an id or unique prefix instead")
			e.Candidates = titleMatches
			return "", false, e
		}
	}
	// Fall through to the direct RPC so the hub produces its canonical
	// not_found error (and synthetic test IDs still reach the server).
	return trimmed, false, nil
}

func matchAdminByTitle(
	ctx context.Context,
	raw, prefix, kind string,
	list func(context.Context) ([]cli.Candidate, error),
) (string, bool, error) {
	want := strings.TrimSpace(raw)
	if want == "" {
		return "", false, cli.Usagef(`%q needs a value after the colon`, prefix)
	}
	candidates, err := list(ctx)
	if err != nil {
		return "", false, err
	}
	var matches []cli.Candidate
	for _, c := range candidates {
		if c.Title != "" && strings.EqualFold(c.Title, want) {
			matches = append(matches, c)
		}
	}
	switch len(matches) {
	case 0:
		return "", false, nil
	case 1:
		return matches[0].ID, true, nil
	default:
		sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
		e := cli.Newf(cli.CodeAmbiguousTarget, "%d %ss match %q", len(matches), kind, want).
			WithHint("specify an id or unique prefix instead")
		e.Candidates = matches
		return "", false, e
	}
}

// resolvePagedAdminID resolves raw against a keyset-paginated admin listing.
func resolvePagedAdminID[T any](
	ctx context.Context,
	noun, raw string,
	allowTitleMatch bool,
	fetchPage func(ctx context.Context, page *leapmuxv1.PageRequest) ([]T, *leapmuxv1.PageResponse, error),
	keyOf func(T) string,
	toCandidate func(T) cli.Candidate,
) (string, error) {
	return resolveAdminTarget(ctx, raw, noun, allowTitleMatch, func(ctx context.Context) ([]cli.Candidate, error) {
		return fetchPagedAdminCandidates(ctx, fetchPage, keyOf, toCandidate)
	})
}

// resolveLiveThenAllPagedAdminID queries live entries first and scans
// historical rows only when no live entry matched, avoiding full scans of
// revoked or expired rows and preventing dead rows from colliding with a live
// prefix.
func resolveLiveThenAllPagedAdminID[T any](
	ctx context.Context,
	noun, raw string,
	allowTitleMatch bool,
	fetchPage func(ctx context.Context, includeHistorical bool, page *leapmuxv1.PageRequest) ([]T, *leapmuxv1.PageResponse, error),
	keyOf func(T) string,
	toCandidate func(T) cli.Candidate,
) (string, error) {
	id, matched, err := matchAdminTarget(ctx, raw, noun, allowTitleMatch, func(ctx context.Context) ([]cli.Candidate, error) {
		return fetchPagedAdminCandidates(ctx, func(ctx context.Context, page *leapmuxv1.PageRequest) ([]T, *leapmuxv1.PageResponse, error) {
			return fetchPage(ctx, false, page)
		}, keyOf, toCandidate)
	})
	if err != nil || matched {
		return id, err
	}
	return resolvePagedAdminID(ctx, noun, raw, allowTitleMatch, func(ctx context.Context, page *leapmuxv1.PageRequest) ([]T, *leapmuxv1.PageResponse, error) {
		return fetchPage(ctx, true, page)
	}, keyOf, toCandidate)
}

func fetchPagedAdminCandidates[T any](
	ctx context.Context,
	fetchPage func(ctx context.Context, page *leapmuxv1.PageRequest) ([]T, *leapmuxv1.PageResponse, error),
	keyOf func(T) string,
	toCandidate func(T) cli.Candidate,
) ([]cli.Candidate, error) {
	rows, err := readAllPages(func(page *leapmuxv1.PageRequest) ([]T, *leapmuxv1.PageResponse, error) {
		return fetchPage(ctx, page)
	}, keyOf)
	if err != nil {
		return nil, err
	}
	out := make([]cli.Candidate, 0, len(rows))
	for _, row := range rows {
		out = append(out, toCandidate(row))
	}
	return out, nil
}

// adminUserTarget holds a parsed user selector and, when the input can also
// match a user ID prefix, the prefix to search for after a NotFound response.
// Its zero value represents an unset optional --user filter.
type adminUserTarget struct {
	selector *leapmuxv1.UserSelector
	prefix   string
}

func (t adminUserTarget) isSet() bool { return t.selector != nil }

// parseAdminUserTarget parses a user argument without issuing any Hub RPC.
// Prefix fallback stays empty for explicit "username:" selectors, complete
// IDs, and inputs shorter than resolve.MinPrefixLength.
func parseAdminUserTarget(raw string) (adminUserTarget, error) {
	sel, err := control.ParseUserArgument(raw)
	if err != nil {
		return adminUserTarget{}, err
	}
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "username:") {
		return adminUserTarget{selector: sel}, nil
	}
	if literal, ok := strings.CutPrefix(trimmed, "id:"); ok {
		literal = strings.TrimSpace(literal)
		if isCompleteAdminID(literal) || len(literal) < resolve.MinPrefixLength {
			return adminUserTarget{selector: sel}, nil
		}
		return adminUserTarget{selector: sel, prefix: literal}, nil
	}
	if isCompleteAdminID(trimmed) {
		return adminUserTarget{selector: hubservice.SelectUserByID(trimmed)}, nil
	}
	if len(trimmed) >= resolve.MinPrefixLength {
		return adminUserTarget{selector: sel, prefix: trimmed}, nil
	}
	return adminUserTarget{selector: sel}, nil
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

// callWithAdminUserTarget invokes call with the parsed user selector first,
// costing zero extra RPCs for exact usernames and exact IDs. When target is
// unset, it invokes call(nil) directly. When the Hub reports NotFound and
// target carries an ID prefix, it scans AdminUserService.ListUsers and retries
// with the resolved full ID.
func callWithAdminUserTarget[T any](
	ctx context.Context,
	c *control.HubClient,
	target adminUserTarget,
	call func(*leapmuxv1.UserSelector) (T, error),
) (T, error) {
	if !target.isSet() {
		return call(nil)
	}
	resp, err := call(target.selector)
	if err == nil || target.prefix == "" || !isAdminUserNotFound(err) {
		return resp, err
	}
	var zero T
	resolved, resolveErr := matchAdminUserIDPrefix(ctx, c, target.prefix)
	if resolveErr != nil {
		return zero, resolveErr
	}
	if resolved == "" || resolved == target.prefix {
		return zero, err
	}
	return call(hubservice.SelectUserByID(resolved))
}

func matchAdminUserIDPrefix(ctx context.Context, c *control.HubClient, prefix string) (string, error) {
	if c == nil {
		return "", nil
	}
	users, err := readAllPages(func(page *leapmuxv1.PageRequest) ([]*leapmuxv1.AdminUser, *leapmuxv1.PageResponse, error) {
		resp, err := c.AdminUserService().ListUsers(ctx, &leapmuxv1.ListUsersRequest{Page: page})
		return resp.GetUsers(), resp.GetPage(), err
	}, (*leapmuxv1.AdminUser).GetId)
	if err != nil {
		return "", err
	}
	matched, ambiguous, ok := lookupByIDPrefixMin(users, prefix, resolve.MinPrefixLength, (*leapmuxv1.AdminUser).GetId)
	if ok {
		return matched.GetId(), nil
	}
	if len(ambiguous) > 0 {
		idMatches := make([]cli.Candidate, 0, len(ambiguous))
		for _, u := range ambiguous {
			idMatches = append(idMatches, cli.Candidate{ID: u.GetId(), Kind: "user", Title: u.GetUsername()})
		}
		sort.Slice(idMatches, func(i, j int) bool { return idMatches[i].ID < idMatches[j].ID })
		e := cli.Newf(cli.CodeAmbiguousTarget, "%d users match the id prefix %s", len(idMatches), prefix).
			WithHint("type more characters of the id, or the full id")
		e.Candidates = idMatches
		return "", e
	}
	return "", nil
}
