import { describe, expect, it } from 'vitest'
import { Scope } from '~/generated/leapmux/v1/scope_pb'
import { closeScopes, impliedScopes, SCOPE_CATEGORIES } from './scopeCatalogue'
import { scopeToken } from './scopeToken'

/**
 * The three NON-grantable values, by name rather than number: each is a
 * construction rather than a permission (UNSPECIFIED is "nobody classified
 * this", NEVER a recorded refusal, ALL the absence of a limit), and a
 * catalogue entry for one would be a checkbox that registers nothing.
 */
const NON_GRANTABLE: readonly Scope[] = [Scope.UNSPECIFIED, Scope.NEVER, Scope.ALL]

const GRANTABLE: Scope[] = Object.values(Scope)
  .filter((v): v is Scope => typeof v === 'number')
  .filter(scope => !NON_GRANTABLE.includes(scope))

/** Every catalogue entry, flattened, for the totality walks below. */
const CATALOGUED = SCOPE_CATEGORIES.flatMap(c => c.entries.map(e => e.scope))

describe('scopeCatalogue', () => {
  // The ENUM is the vocabulary and the catalogue is its presentation. A scope
  // added to the proto but not here renders no checkbox at all -- the
  // registrant cannot ceiling it -- and one removed from the proto but not
  // here renders a checkbox the hub refuses. Totality, each way, is what keeps
  // the form honest without a second source of truth.
  it('lists every grantable scope exactly once', () => {
    expect([...CATALOGUED].sort((a, b) => a - b)).toEqual([...GRANTABLE].sort((a, b) => a - b))
  })

  it('offers no non-grantable value', () => {
    for (const scope of NON_GRANTABLE)
      expect(CATALOGUED).not.toContain(scope)
  })

  // A category with one entry is a heading that answers nothing, and an empty
  // description is a scope the reader must take on faith. Both are cheaper to
  // catch here than in review of a nineteen-line form.
  it('lists and describes every entry', () => {
    for (const category of SCOPE_CATEGORIES) {
      expect(category.label.trim(), 'a category label').not.toBe('')
      expect(category.entries.length, `${category.label} holds at least one scope`).toBeGreaterThan(0)
      for (const entry of category.entries)
        expect(entry.description.trim(), `${scopeToken(entry.scope)} carries its proto comment's sentence`).not.toBe('')
    }
  })

  // The user's own example, stated as a test: tick workspace:write and
  // workspace:read is implied. The disabled checkbox the form renders and
  // the submitted closure both derive from this one rule.
  it('implies the read half of a write', () => {
    expect(impliedScopes([Scope.WORKSPACE_WRITE])).toEqual(new Set([Scope.WORKSPACE_READ]))
  })

  // The hub's ScopeSet.Close iterates to a fixed point, so terminal:write
  // reaches worker:read through terminal:read -- a one-pass expansion would
  // stop short and submit a set the hub widens anyway.
  it('closes transitively', () => {
    expect(closeScopes([Scope.TERMINAL_WRITE])).toEqual(
      [Scope.WORKER_READ, Scope.TERMINAL_READ, Scope.TERMINAL_WRITE].sort((a, b) => a - b),
    )
    expect(closeScopes([Scope.AGENT_WRITE])).toEqual(
      [Scope.WORKER_READ, Scope.AGENT_READ, Scope.AGENT_WRITE].sort((a, b) => a - b),
    )
  })

  it('closes to the ticked set when nothing is implied', () => {
    expect(closeScopes([Scope.WORKSPACE_READ])).toEqual([Scope.WORKSPACE_READ])
  })

  // Enum order is the canonical order the hub sorts a stored ceiling by, and
  // the request reading the same way is what makes a round trip stable: what
  // ListApps answers after a register is byte-for-byte what was submitted.
  it('returns the closure in enum order', () => {
    const closed = closeScopes([Scope.GIT_WRITE, Scope.WORKSPACE_READ])
    expect(closed).toEqual([Scope.WORKSPACE_READ, Scope.WORKER_READ, Scope.GIT_READ, Scope.GIT_WRITE])
  })
})
