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

// prefixResolutionLimit caps a single-page server-side id_prefix lookup. The
// server orders prefix results by id ASC, so 21 rows suffice to see an exact
// match at row 0, a unique match, or up to 20 ambiguous candidates without
// multi-page scans.
const prefixResolutionLimit = 21

// isCompleteAdminID reports whether s already has the shape of a full generated
// identifier or a standard 36-character UUID, so prefix listing can be skipped.
func isCompleteAdminID(s string) bool {
	return resolve.IsGeneratedID(s) || (len(s) == 36 && uuid.Validate(s) == nil)
}

// resolveAdminIDPrefix resolves an admin entity target that supports title
// matching (such as apps, API tokens, and sign-in providers).
func resolveAdminIDPrefix(ctx context.Context, raw, kind string, list func(ctx context.Context, idPrefix string) ([]cli.Candidate, error)) (string, error) {
	return resolveAdminTarget(ctx, raw, kind, true, list)
}

// resolveAdminTarget resolves an admin entity target that may be a full ID,
// an explicit "id:<id>" selector (exact ID at any length or unique ID prefix
// of at least resolve.MinPrefixLength characters, never matching titles), a
// bare unique ID prefix of at least resolve.MinPrefixLength characters, or
// (when allowTitleMatch is true) a "name:<name>"/"title:<title>" selector or
// bare case-insensitive title match. Complete IDs skip listing completely so
// exact lookups cost no extra round trip.
func resolveAdminTarget(
	ctx context.Context,
	raw, kind string,
	allowTitleMatch bool,
	list func(ctx context.Context, idPrefix string) ([]cli.Candidate, error),
) (string, error) {
	return resolveAdminTargetAcrossPasses(ctx, raw, kind, allowTitleMatch, []func(context.Context, string) ([]cli.Candidate, error){list})
}

func resolveAdminTargetAcrossPasses(
	ctx context.Context,
	raw, kind string,
	allowTitleMatch bool,
	passes []func(ctx context.Context, idPrefix string) ([]cli.Candidate, error),
) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if literal, ok := strings.CutPrefix(trimmed, "id:"); ok {
		literal = strings.TrimSpace(literal)
		if literal == "" {
			return "", cli.Usagef(`"id:" needs an id after the colon`)
		}
		if isCompleteAdminID(literal) {
			return literal, nil
		}
		for _, list := range passes {
			candidates, err := list(ctx, literal)
			if err != nil {
				return "", err
			}
			id, matched, err := pickAdminIDPrefixMatch(candidates, literal, kind)
			if err != nil || matched {
				return id, err
			}
		}
		// Let the direct RPC produce the hub's canonical not_found error.
		return literal, nil
	}
	if name, ok := strings.CutPrefix(trimmed, "name:"); ok {
		if !allowTitleMatch {
			return "", cli.Usagef(`%s targets do not support "name:" selectors; specify an id or unique id prefix`, kind)
		}
		return resolveAdminByTitleAcrossPasses(ctx, name, "name:", kind, passes)
	}
	if title, ok := strings.CutPrefix(trimmed, "title:"); ok {
		if !allowTitleMatch {
			return "", cli.Usagef(`%s targets do not support "title:" selectors; specify an id or unique id prefix`, kind)
		}
		return resolveAdminByTitleAcrossPasses(ctx, title, "title:", kind, passes)
	}
	if isCompleteAdminID(trimmed) || trimmed == "" {
		return trimmed, nil
	}

	// Query server-side id_prefix first when trimmed can be an ID prefix.
	// Across live-then-all passes, check live id_prefix then historical
	// id_prefix before falling back to full-table scans for title matching.
	if len(trimmed) >= resolve.MinPrefixLength && resolve.IsBareIDPrefixCandidate(trimmed) {
		for _, list := range passes {
			candidates, err := list(ctx, trimmed)
			if err != nil {
				return "", err
			}
			id, matched, err := pickAdminIDPrefixMatch(candidates, trimmed, kind)
			if err != nil || matched {
				return id, err
			}
		}
	}

	if !allowTitleMatch {
		// Fall through to the direct RPC so the hub produces its canonical
		// not_found error (and synthetic test IDs still reach the server).
		return trimmed, nil
	}

	for _, list := range passes {
		candidates, err := list(ctx, "")
		if err != nil {
			return "", err
		}
		var titleMatches []cli.Candidate
		for _, c := range candidates {
			if c.ID == trimmed {
				return c.ID, nil
			}
			if c.Title != "" && strings.EqualFold(c.Title, trimmed) {
				titleMatches = append(titleMatches, c)
			}
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
	}
	// Fall through to the direct RPC so the hub produces its canonical
	// not_found error (and synthetic test IDs still reach the server).
	return trimmed, nil
}

func pickAdminIDPrefixMatch(candidates []cli.Candidate, prefix, kind string) (string, bool, error) {
	match, ambiguous, ok := lookupByIDPrefixMin(candidates, prefix, resolve.MinPrefixLength, func(c cli.Candidate) string {
		return c.ID
	})
	if ok {
		return match.ID, true, nil
	}
	if len(ambiguous) > 1 {
		sort.Slice(ambiguous, func(i, j int) bool { return ambiguous[i].ID < ambiguous[j].ID })
		e := cli.Newf(cli.CodeAmbiguousTarget, "%d %ss match the id prefix %s", len(ambiguous), kind, prefix).
			WithHint("type more characters of the id, or the full id")
		e.Candidates = ambiguous
		return "", false, e
	}
	return "", false, nil
}

func resolveAdminByTitleAcrossPasses(
	ctx context.Context,
	raw, prefix, kind string,
	passes []func(ctx context.Context, idPrefix string) ([]cli.Candidate, error),
) (string, error) {
	want := strings.TrimSpace(raw)
	if want == "" {
		return "", cli.Usagef(`%q needs a value after the colon`, prefix)
	}
	for _, list := range passes {
		candidates, err := list(ctx, "")
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
			continue
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
	return "", cli.Newf(cli.CodeNotFound, "no %s matches %q", kind, want)
}

// resolvePagedAdminID resolves raw against a keyset-paginated admin listing,
// issuing a single capped page request when a server-side id_prefix query
// applies and scanning all pages only for title fallbacks.
func resolvePagedAdminID[T any](
	ctx context.Context,
	noun, raw string,
	allowTitleMatch bool,
	fetchPage func(ctx context.Context, idPrefix string, page *leapmuxv1.PageRequest) ([]T, *leapmuxv1.PageResponse, error),
	keyOf func(T) string,
	toCandidate func(T) cli.Candidate,
) (string, error) {
	return resolveAdminTarget(ctx, raw, noun, allowTitleMatch, func(ctx context.Context, idPrefix string) ([]cli.Candidate, error) {
		return fetchPagedAdminCandidates(ctx, idPrefix, fetchPage, keyOf, toCandidate)
	})
}

// resolveLiveThenAllPagedAdminID queries live entries first and falls back to
// historical rows only when no live entry matched, avoiding scans of revoked
// or expired rows and preventing dead rows from colliding with a live prefix.
func resolveLiveThenAllPagedAdminID[T any](
	ctx context.Context,
	noun, raw string,
	allowTitleMatch bool,
	fetchPage func(ctx context.Context, includeHistorical bool, idPrefix string, page *leapmuxv1.PageRequest) ([]T, *leapmuxv1.PageResponse, error),
	keyOf func(T) string,
	toCandidate func(T) cli.Candidate,
) (string, error) {
	passes := []func(context.Context, string) ([]cli.Candidate, error){
		func(ctx context.Context, idPrefix string) ([]cli.Candidate, error) {
			return fetchPagedAdminCandidates(ctx, idPrefix, func(ctx context.Context, prefix string, page *leapmuxv1.PageRequest) ([]T, *leapmuxv1.PageResponse, error) {
				return fetchPage(ctx, false, prefix, page)
			}, keyOf, toCandidate)
		},
		func(ctx context.Context, idPrefix string) ([]cli.Candidate, error) {
			return fetchPagedAdminCandidates(ctx, idPrefix, func(ctx context.Context, prefix string, page *leapmuxv1.PageRequest) ([]T, *leapmuxv1.PageResponse, error) {
				return fetchPage(ctx, true, prefix, page)
			}, keyOf, toCandidate)
		},
	}
	return resolveAdminTargetAcrossPasses(ctx, raw, noun, allowTitleMatch, passes)
}

func fetchPagedAdminCandidates[T any](
	ctx context.Context,
	idPrefix string,
	fetchPage func(ctx context.Context, idPrefix string, page *leapmuxv1.PageRequest) ([]T, *leapmuxv1.PageResponse, error),
	keyOf func(T) string,
	toCandidate func(T) cli.Candidate,
) ([]cli.Candidate, error) {
	var (
		rows []T
		err  error
	)
	if idPrefix != "" {
		rows, _, err = fetchPage(ctx, idPrefix, &leapmuxv1.PageRequest{Limit: prefixResolutionLimit})
	} else {
		rows, err = readAllPages(func(page *leapmuxv1.PageRequest) ([]T, *leapmuxv1.PageResponse, error) {
			return fetchPage(ctx, "", page)
		}, keyOf)
	}
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
	if len(trimmed) >= resolve.MinPrefixLength && resolve.IsBareIDPrefixCandidate(trimmed) {
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
// target carries an ID prefix, it queries AdminUserService.ListUsers with
// id_prefix and retries with the resolved full ID.
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
	resp, err := c.AdminUserService().ListUsers(ctx, &leapmuxv1.ListUsersRequest{
		IdPrefix: prefix,
		Page:     &leapmuxv1.PageRequest{Limit: prefixResolutionLimit},
	})
	if err != nil {
		return "", err
	}
	matched, ambiguous, ok := lookupByIDPrefixMin(resp.GetUsers(), prefix, resolve.MinPrefixLength, (*leapmuxv1.AdminUser).GetId)
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
