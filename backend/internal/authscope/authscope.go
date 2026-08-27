// Package authscope holds the scope vocabulary shared by the Hub and the
// Worker: the wire token of each leapmuxv1.Scope, the set type a grant is
// carried in, and the rules that close, narrow and parse a set.
//
// It is its own package because BOTH processes need it and neither may import
// the other's. The Worker enforces a grant inside the Noise tunnel (see
// channel.Caller), and it cannot import internal/hub/auth; the Hub mints and
// stores the grant. Two set implementations would be exactly the drift this
// design exists to remove.
//
// The zero ScopeSet allows NOTHING. Every widening is a value a producer
// constructs on purpose -- UnscopedGrant for "no limit", New or Parse for a
// stated set -- so a dropped error or a forgotten constructor denies rather
// than grants.
package authscope

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// grantableTokens is the total bijection between a grantable Scope and its
// RFC 6749 section 3.3 wire token.
//
// The three non-grantable values are ABSENT by construction, which is what
// makes them unsayable: Parse can only produce what this map lists, so
// SCOPE_UNSPECIFIED, SCOPE_NEVER and SCOPE_ALL have no spelling a request, a
// stored row or a consent screen could carry.
//
// A scope added to scope.proto with no entry here fails
// TestGrantableTokensCoverEveryScope, so the vocabulary cannot grow a value
// that no surface can name.
var grantableTokens = map[leapmuxv1.Scope]string{
	leapmuxv1.Scope_SCOPE_ACCOUNT_READ:    "account:read",
	leapmuxv1.Scope_SCOPE_ACCOUNT_WRITE:   "account:write",
	leapmuxv1.Scope_SCOPE_WORKSPACE_READ:  "workspace:read",
	leapmuxv1.Scope_SCOPE_WORKSPACE_WRITE: "workspace:write",
	leapmuxv1.Scope_SCOPE_WORKER_READ:     "worker:read",
	leapmuxv1.Scope_SCOPE_WORKER_ADMIN:    "worker:admin",
	leapmuxv1.Scope_SCOPE_AGENT_READ:      "agent:read",
	leapmuxv1.Scope_SCOPE_AGENT_WRITE:     "agent:write",
	leapmuxv1.Scope_SCOPE_TERMINAL_READ:   "terminal:read",
	leapmuxv1.Scope_SCOPE_TERMINAL_WRITE:  "terminal:write",
	leapmuxv1.Scope_SCOPE_FILE_READ:       "file:read",
	leapmuxv1.Scope_SCOPE_GIT_READ:        "git:read",
	leapmuxv1.Scope_SCOPE_GIT_WRITE:       "git:write",
	leapmuxv1.Scope_SCOPE_TUNNEL_OPEN:     "tunnel:open",
	leapmuxv1.Scope_SCOPE_ADMIN_READ:      "admin:read",
	leapmuxv1.Scope_SCOPE_ADMIN_USERS:     "admin:users",
	leapmuxv1.Scope_SCOPE_ADMIN_SETTINGS:  "admin:settings",
	leapmuxv1.Scope_SCOPE_ADMIN_WORKERS:   "admin:workers",
}

// scopesByToken is the reverse of grantableTokens, built once at init so Parse
// is a map lookup rather than a scan. Built FROM the forward map, so the two
// directions cannot disagree.
var scopesByToken = func() map[string]leapmuxv1.Scope {
	out := make(map[string]leapmuxv1.Scope, len(grantableTokens))
	for scope, token := range grantableTokens {
		out[token] = scope
	}
	return out
}()

// grantableOrder lists every grantable scope in enum order, which is the
// canonical order this package sorts by. Enum order groups the families
// (account, workspace, worker, agent, terminal, file, git, tunnel, admin), so
// one order serves the stored string, the token response and the consent
// screen -- and a second ordering cannot drift from it.
var grantableOrder = func() []leapmuxv1.Scope {
	out := make([]leapmuxv1.Scope, 0, len(grantableTokens))
	for scope := range grantableTokens {
		out = append(out, scope)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}()

// impliedBy states what a grant EXPANDS to at the mint.
//
// Three families, and each is a case where granting one scope without the
// other would promise something the hub cannot deliver:
//
//   - Every WORKER-SURFACE scope implies worker:read, because the channel it
//     needs cannot be opened without it.
//   - Every WRITE implies its own read. An app that may type into a terminal
//     but may not read the output cannot see what it typed, and the consent
//     screen would state a boundary that means nothing.
//   - Every admin:* scope implies admin:read, because administering a thing
//     starts with listing it.
//
// The expansion runs ONCE, in Close, at the moment the grant is minted --
// never at enforcement. So the stored set, the consent screen and the token
// response all show the same thing, and every gate stays a plain membership
// test.
//
// Closing at enforcement instead would put the rule in every gate, including
// the Worker's, and a gate that forgot it would refuse a grant the consent
// screen promised.
var impliedBy = map[leapmuxv1.Scope][]leapmuxv1.Scope{
	leapmuxv1.Scope_SCOPE_ACCOUNT_WRITE:   {leapmuxv1.Scope_SCOPE_ACCOUNT_READ},
	leapmuxv1.Scope_SCOPE_WORKSPACE_WRITE: {leapmuxv1.Scope_SCOPE_WORKSPACE_READ},
	leapmuxv1.Scope_SCOPE_WORKER_ADMIN:    {leapmuxv1.Scope_SCOPE_WORKER_READ},
	leapmuxv1.Scope_SCOPE_AGENT_READ:      {leapmuxv1.Scope_SCOPE_WORKER_READ},
	leapmuxv1.Scope_SCOPE_AGENT_WRITE:     {leapmuxv1.Scope_SCOPE_WORKER_READ, leapmuxv1.Scope_SCOPE_AGENT_READ},
	leapmuxv1.Scope_SCOPE_TERMINAL_READ:   {leapmuxv1.Scope_SCOPE_WORKER_READ},
	leapmuxv1.Scope_SCOPE_TERMINAL_WRITE:  {leapmuxv1.Scope_SCOPE_WORKER_READ, leapmuxv1.Scope_SCOPE_TERMINAL_READ},
	leapmuxv1.Scope_SCOPE_FILE_READ:       {leapmuxv1.Scope_SCOPE_WORKER_READ},
	leapmuxv1.Scope_SCOPE_GIT_READ:        {leapmuxv1.Scope_SCOPE_WORKER_READ},
	leapmuxv1.Scope_SCOPE_GIT_WRITE:       {leapmuxv1.Scope_SCOPE_WORKER_READ, leapmuxv1.Scope_SCOPE_GIT_READ},
	leapmuxv1.Scope_SCOPE_TUNNEL_OPEN:     {leapmuxv1.Scope_SCOPE_WORKER_READ},
	leapmuxv1.Scope_SCOPE_ADMIN_USERS:     {leapmuxv1.Scope_SCOPE_ADMIN_READ},
	leapmuxv1.Scope_SCOPE_ADMIN_SETTINGS:  {leapmuxv1.Scope_SCOPE_ADMIN_READ},
	leapmuxv1.Scope_SCOPE_ADMIN_WORKERS:   {leapmuxv1.Scope_SCOPE_ADMIN_READ},
}

// IsGrantable reports whether an account may grant this scope to an app.
//
// The three non-grantable values answer false: SCOPE_UNSPECIFIED (nobody
// classified it), SCOPE_NEVER (the recorded refusal) and SCOPE_ALL (the
// absence of a limit, which is a property of a SET rather than a member of
// one).
func IsGrantable(scope leapmuxv1.Scope) bool {
	_, ok := grantableTokens[scope]
	return ok
}

// Token returns the wire spelling of a grantable scope. A non-grantable value
// has none, and the second result says so.
func Token(scope leapmuxv1.Scope) (string, bool) {
	token, ok := grantableTokens[scope]
	return token, ok
}

// ScopeFor resolves one wire token. An unknown token has no scope, and the
// caller must refuse rather than skip it; see Parse.
func ScopeFor(token string) (leapmuxv1.Scope, bool) {
	scope, ok := scopesByToken[token]
	return scope, ok
}

// Grantable returns every grantable scope in canonical (enum) order. The slice
// is freshly built, so a caller may sort or filter it.
func Grantable() []leapmuxv1.Scope {
	return append([]leapmuxv1.Scope(nil), grantableOrder...)
}

// ScopeSet is one grant: either an explicit absence of limit, or a set of
// grantable scopes.
//
// A struct with one unexported field rather than a bare integer, so the only
// values that exist are the ones this package's constructors made. It stays
// COMPARABLE and copyable, which is what lets a UserInfo carry it by value on
// the hot path and a channel index key on it.
//
// The zero value is the empty set, which allows nothing.
type ScopeSet struct {
	// bits holds one bit per leapmuxv1.Scope enum number. The SCOPE_ALL bit
	// marks the unscoped grant, so "unscoped" is a value in the same field
	// rather than a second flag that could contradict it.
	bits uint32
}

func bit(scope leapmuxv1.Scope) uint32 {
	return 1 << uint(scope)
}

// UnscopedGrant is the grant that limits nothing.
//
// It is spelled out at every site that has one -- a session cookie, solo
// mode, the control CLI's own credential -- because the alternative is the
// zero value, and a producer that FAILS to state the grant also produces the
// zero value. Making the two differ is what turns a dropped grant into a
// refusal instead of a silent promotion.
func UnscopedGrant() ScopeSet {
	return ScopeSet{bits: bit(leapmuxv1.Scope_SCOPE_ALL)}
}

// New builds a set from grantable scopes, and refuses any other value.
//
// It does NOT close the set; call Close at the mint. Keeping the two apart
// means a test can state a set exactly, and the closure runs once at the one
// place that owns what gets stored.
func New(scopes ...leapmuxv1.Scope) (ScopeSet, error) {
	var set ScopeSet
	for _, scope := range scopes {
		if !IsGrantable(scope) {
			return ScopeSet{}, fmt.Errorf("scope %s cannot be granted", scope)
		}
		set.bits |= bit(scope)
	}
	return set, nil
}

// MustNew is New for a caller that already knows its arguments are grantable
// -- a package-level table of literals. It panics on anything else, which
// turns a bad table into a boot failure rather than a runtime denial.
func MustNew(scopes ...leapmuxv1.Scope) ScopeSet {
	set, err := New(scopes...)
	if err != nil {
		panic(err)
	}
	return set
}

// IsUnscoped reports whether this grant limits nothing.
func (s ScopeSet) IsUnscoped() bool {
	return s.bits&bit(leapmuxv1.Scope_SCOPE_ALL) != 0
}

// IsEmpty reports whether this grant reaches nothing at all. The zero value is
// empty, and so is a set every narrowing emptied.
func (s ScopeSet) IsEmpty() bool {
	return s.bits == 0
}

// Allows reports whether this grant reaches one scope.
//
// A non-grantable value is refused by EVERY set, the unscoped one included: no
// grant reaches SCOPE_NEVER, and SCOPE_UNSPECIFIED marks a procedure nobody
// classified. The scope rung applies its own unscoped short-circuit before it
// asks, because an unscoped credential is not limited by the map at all; see
// auth.enforceScope.
func (s ScopeSet) Allows(scope leapmuxv1.Scope) bool {
	if !IsGrantable(scope) {
		return false
	}
	if s.IsUnscoped() {
		return true
	}
	return s.bits&bit(scope) != 0
}

// Scopes returns the members of this grant in canonical order.
//
// An unscoped grant returns SCOPE_ALL alone, which is what it stores and what
// it sends on the wire. A caller that wants to DISPLAY what an unscoped grant
// reaches expands it with Grantable itself, so the two questions stay apart.
func (s ScopeSet) Scopes() []leapmuxv1.Scope {
	if s.IsUnscoped() {
		return []leapmuxv1.Scope{leapmuxv1.Scope_SCOPE_ALL}
	}
	out := make([]leapmuxv1.Scope, 0, len(grantableOrder))
	for _, scope := range grantableOrder {
		if s.bits&bit(scope) != 0 {
			out = append(out, scope)
		}
	}
	return out
}

// String is the canonical RFC 6749 section 3.3 `scope` value: the tokens in
// canonical order, separated by one space, with no duplicates.
//
// It is what the store holds, what the token response returns and what the
// consent screen lists -- ONE spelling, so no second encoding can drift from
// it. A bitmask is the in-memory form alone.
//
// An unscoped grant has no token (SCOPE_ALL is not grantable), so it renders
// as the EMPTY string, which is also how the empty set renders. The two are
// distinguished by IsUnscoped, never by this value -- see Storable, which is
// what every mint calls so an unscoped grant is never written to a column.
func (s ScopeSet) String() string {
	return strings.Join(s.Tokens(), " ")
}

// Tokens renders the set as its RFC 6749 section 3.3 tokens, in the CANONICAL
// order -- the order scope.proto declares, which groups a family together.
//
// It is what String joins, so a caller that wants the list and a caller that
// wants the string read the same walk. A non-grantable value has no token and
// cannot be in a set, so nothing is dropped here silently.
func (s ScopeSet) Tokens() []string {
	scopes := s.Scopes()
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if token, ok := Token(scope); ok {
			out = append(out, token)
		}
	}
	return out
}

// SortedTokens renders the set for a MACHINE-facing surface -- an app listing,
// a CLI envelope, the metadata document -- lexicographically rather than in the
// declared order, because those read as a list of names.
//
// A consent screen uses neither: it renders sentences, in the declared order,
// so a reader meets the account family before the hub-administration one.
func (s ScopeSet) SortedTokens() []string {
	out := s.Tokens()
	sort.Strings(out)
	return out
}

// ErrUnscopedNotStorable reports an attempt to persist the unscoped grant as a
// `scope` string.
//
// SCOPE_ALL has no wire token by design (see grantableTokens), so String
// renders an unscoped grant as the empty string -- which parses back as the
// EMPTY set. The round trip therefore loses authority rather than inventing
// it, which fails closed but silently. Every mint refuses the value instead,
// so the loss cannot happen at all.
//
// No stored credential legitimately carries an unscoped grant: CeilingFor
// narrows every kind to a finite ceiling before the mint, so a set that
// reaches this error came from a caller that skipped the narrowing.
var ErrUnscopedNotStorable = errors.New("an unscoped grant has no stored spelling; narrow it to a ceiling first")

// Storable returns the canonical string to persist, and refuses the unscoped
// grant. Every credential mint calls it, so the refusal is a property of the
// boundary rather than a line each mint must remember.
func (s ScopeSet) Storable() (string, error) {
	if s.IsUnscoped() {
		return "", ErrUnscopedNotStorable
	}
	return s.String(), nil
}

// Parse reads an RFC 6749 section 3.3 `scope` value.
//
// An unknown token REFUSES THE WHOLE VALUE rather than being dropped. Dropping
// is the failure that looks safe: a row holding "workspace:read agent:write"
// whose second token became unknown would keep working as a workspace-read app,
// and nobody would ever notice that the vocabulary drifted. Refusing turns the
// same drift into one loud failure at the next request.
//
// An empty or all-whitespace value is the EMPTY set, not an error. A request
// may legitimately ask for nothing, and the caller decides whether that is
// usable; the store's own NOT NULL is what stops an unstated grant.
func Parse(raw string) (ScopeSet, error) {
	var set ScopeSet
	for _, token := range strings.Fields(raw) {
		scope, ok := ScopeFor(token)
		if !ok {
			return ScopeSet{}, fmt.Errorf("unknown scope %q", token)
		}
		set.bits |= bit(scope)
	}
	return set, nil
}

// Close expands a grant by its implications, and it runs at the MINT.
//
// See impliedBy for the three implication families. Closing here rather than at
// each gate keeps the stored set, the consent screen, the token response and
// every enforcement point reading the same value.
//
// An unscoped grant closes to itself: it already reaches everything.
func (s ScopeSet) Close() ScopeSet {
	if s.IsUnscoped() {
		return s
	}
	out := s
	// One pass per member is enough only if the implication graph has no chain
	// this pass would miss, so iterate to a fixed point instead of assuming a
	// depth. The graph is tiny, and this makes a future two-step implication
	// correct with no edit here.
	for {
		before := out.bits
		for scope, implied := range impliedBy {
			if out.bits&bit(scope) == 0 {
				continue
			}
			for _, target := range implied {
				out.bits |= bit(target)
			}
		}
		if out.bits == before {
			return out
		}
	}
}

// NarrowTo limits this grant by a ceiling.
//
// The unscoped cases are the point:
//
//   - An UNSCOPED grant narrowed by a ceiling becomes THE CEILING, and never
//     stays unscoped. This is the rule that stops a worker-minted delegation
//     bearer -- which inherits an unscoped user -- from reaching the whole hub.
//   - A ceiling that is itself unscoped limits nothing, so the grant passes
//     through unchanged.
//
// Anything else is the intersection.
func (s ScopeSet) NarrowTo(ceiling ScopeSet) ScopeSet {
	if ceiling.IsUnscoped() {
		return s
	}
	if s.IsUnscoped() {
		return ceiling
	}
	return ScopeSet{bits: s.bits & ceiling.bits}
}

// Union is the widest grant that either set reaches. It exists for building a
// ceiling or a default from named parts, never for widening a stored grant.
func (s ScopeSet) Union(other ScopeSet) ScopeSet {
	if s.IsUnscoped() || other.IsUnscoped() {
		return UnscopedGrant()
	}
	return ScopeSet{bits: s.bits | other.bits}
}

// Without removes scopes from a set. It is how a ceiling states an exclusion
// ("everything except admin:*") from the grantable list, so the exclusion is
// read at the one place that states it.
//
// An unscoped set is returned unchanged: it has no members to remove, and
// silently turning it into a finite set here would hide the narrowing that
// NarrowTo exists to make explicit.
func (s ScopeSet) Without(scopes ...leapmuxv1.Scope) ScopeSet {
	if s.IsUnscoped() {
		return s
	}
	out := s
	for _, scope := range scopes {
		out.bits &^= bit(scope)
	}
	return out
}

// Contains reports whether this grant reaches every member of other.
//
// Used where one set must be checked against another rather than against a
// single scope: an app's requested scopes against its registered ceiling.
func (s ScopeSet) Contains(other ScopeSet) bool {
	if s.IsUnscoped() {
		return true
	}
	if other.IsUnscoped() {
		return false
	}
	return other.bits&^s.bits == 0
}

// adminScopes is the hub-administration family.
//
// It lives HERE rather than beside the one refusal that reads it, because four
// surfaces ask the same question -- the consent leg refusing a
// non-administrator, the admin mint's default, the control CLI's default
// grant, and the credential-notice subject line -- and four literals is four
// chances for a fifth admin scope to be missed by one of them.
var adminScopes = []leapmuxv1.Scope{
	leapmuxv1.Scope_SCOPE_ADMIN_READ,
	leapmuxv1.Scope_SCOPE_ADMIN_USERS,
	leapmuxv1.Scope_SCOPE_ADMIN_SETTINGS,
	leapmuxv1.Scope_SCOPE_ADMIN_WORKERS,
}

// AdminScopes returns the hub-administration family, in canonical order.
//
// TestAdminScopesCoverEveryAdminToken pins the list against the token prefix,
// so a fifth `admin:` scope added to scope.proto fails the suite until it is
// listed here.
func AdminScopes() []leapmuxv1.Scope {
	return append([]leapmuxv1.Scope(nil), adminScopes...)
}

// NonAdminGrant is every grantable scope EXCEPT hub administration.
//
// It is the default an administrator's `api-token issue` mints with no --scope
// stated. The consent leg reaches the same set by subtracting the admin family
// from the APP's registered ceiling rather than from every grantable scope --
// a third-party app's default must stay inside what it registered -- so the
// two surfaces agree on the rule (an admin permission is never granted by
// default; it must be named) while starting from different ceilings.
//
// That property is the one the deleted api_tokens.admin_scope column defended,
// and this is where it now lives.
func NonAdminGrant() ScopeSet {
	return EveryGrantableScope().Without(adminScopes...)
}

// EveryGrantableScope is the set of every grantable scope -- the widest FINITE
// grant, which is not the same value as the unscoped one.
//
// The difference matters at exactly one place and it is the reason both exist:
// NarrowTo turns an unscoped grant into its ceiling, so a ceiling built from
// this set narrows, while an unscoped ceiling does not.
func EveryGrantableScope() ScopeSet {
	var set ScopeSet
	for _, scope := range grantableOrder {
		set.bits |= bit(scope)
	}
	return set
}

// ScopesFromWire reads the grant the Hub announced in a ChannelOpenRequest.
//
// An EMPTY list is a refusal, and the second result says so. It means the Hub
// failed to say, exactly as an empty user_id does -- a first-party credential
// sends the explicit SCOPE_ALL, so a Hub that DROPPED the field opens no
// channel at all rather than silently promoting a narrow app to full authority.
// The failure is loud and immediate; the alternative is silent and total.
//
// A list containing SCOPE_ALL is the unscoped grant. Any other non-grantable
// value is refused: SCOPE_UNSPECIFIED is the proto zero, which is what a
// serializer writes for a field it does not understand, and SCOPE_NEVER is a
// value no grant may carry.
func ScopesFromWire(wire []leapmuxv1.Scope) (ScopeSet, bool) {
	if len(wire) == 0 {
		return ScopeSet{}, false
	}
	var listed []leapmuxv1.Scope
	for _, scope := range wire {
		if scope == leapmuxv1.Scope_SCOPE_ALL {
			// The explicit absence of a limit. It is exclusive: a list that
			// carries it alongside listed scopes is a producer that did not
			// mean either, so the unscoped reading wins and the rest is
			// redundant by definition.
			return UnscopedGrant(), true
		}
		if !IsGrantable(scope) {
			return ScopeSet{}, false
		}
		listed = append(listed, scope)
	}
	set, err := New(listed...)
	if err != nil {
		return ScopeSet{}, false
	}
	return set, true
}

// ScopesToWire renders a grant for a ChannelOpenRequest.
//
// An UNSCOPED grant becomes the single explicit SCOPE_ALL, which is what makes
// a dropped field distinguishable from a first-party credential. An EMPTY grant
// becomes an empty list -- and the Worker refuses that open, which is correct:
// a credential that reaches nothing has no business holding a channel.
func ScopesToWire(set ScopeSet) []leapmuxv1.Scope {
	return set.Scopes()
}
