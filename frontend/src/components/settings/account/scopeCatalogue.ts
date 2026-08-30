import type { GrantableScope } from '~/generated/contracts/scopes'
import type { Scope } from '~/generated/proto/leapmux/v1/scope_pb'
import { SCOPE_CATEGORIES as GENERATED_CATEGORIES, IMPLIED_BY, SCOPE_DESCRIPTIONS } from '~/generated/contracts/scopes'

/**
 * The grantable vocabulary as a person reads it: grouped the way scope.proto's
 * section comments group it, each scope with the sentence its contract states.
 *
 * The vocabulary itself -- families, sentences, and the implied-by graph --
 * is generated from contracts/scopes.json, the same file the hub's authscope
 * and OAuth consent pages are generated from: one source, both languages, and
 * a scope added to the proto fails `task generate-contracts` until somebody
 * writes its entry there. This module keeps only the FORM layer: the entry
 * shape the Preferences dialog renders and the closure math the register form
 * submits with.
 */
export interface ScopeEntry {
  scope: GrantableScope
  description: string
}

/** One proto section: "Account", "Workspace", ... */
export interface ScopeCategory {
  label: string
  entries: ScopeEntry[]
}

export const SCOPE_CATEGORIES: readonly ScopeCategory[] = GENERATED_CATEGORIES.map(category => ({
  label: category.label,
  entries: category.scopes.map(scope => ({ scope, description: SCOPE_DESCRIPTIONS[scope] })),
}))

/**
 * What a grant EXPANDS to: each key carries the scopes the hub adds to any
 * grant that includes it.
 *
 * Generated from contracts/scopes.json -- the same table the hub's
 * `impliedBy` in internal/authscope expands a grant at the mint with -- so
 * the form shows the closure the hub will perform: an implied scope renders
 * checked and disabled, and the submitted set carries it, so what the owner
 * ticked is exactly what the next ListApps reads back.
 *
 * Three families, each a case where granting one scope without the other
 * would promise something the hub cannot deliver: every worker-surface scope
 * needs the channel worker:read opens; every write implies its own read; and
 * every admin:* scope implies admin:read, because administering a thing
 * starts with listing it.
 */
// Object.entries(IMPLIED_BY) returns numeric enum keys as STRINGS, and a Set
// of numbers would never match one -- so each key is parsed once, here,
// rather than on every walk of the table.
const IMPLIED_BY_ENTRIES = Object.entries(IMPLIED_BY)
  .map(([key, implied]) => [Number(key) as Scope, implied] as const)

/**
 * The closure of `selected`: the ticked scopes plus everything they imply,
 * transitively -- the same fixed point the hub's ScopeSet.Close computes,
 * iterated rather than assumed one pass deep so a future two-step
 * implication is correct with no edit here.
 *
 * The one primitive both derived views read: what the form LOCKS is this
 * closure minus the ticked set, and what it SUBMITS is the closure itself.
 */
function closure(selected: readonly Scope[]): Set<Scope> {
  const closed = new Set(selected)
  for (let added = true; added;) {
    added = false
    for (const [scope, implied] of IMPLIED_BY_ENTRIES) {
      if (!closed.has(scope))
        continue
      for (const target of implied) {
        if (!closed.has(target)) {
          closed.add(target)
          added = true
        }
      }
    }
  }
  return closed
}

/**
 * The scopes `selected` implies: the closure minus the ticked scopes
 * themselves.
 */
export function impliedScopes(selected: readonly Scope[]): Set<Scope> {
  const closed = closure(selected)
  for (const scope of selected)
    closed.delete(scope)
  return closed
}

/**
 * The set to SUBMIT: what was ticked, plus what the hub would add anyway.
 *
 * Sorted in enum order, the canonical order the hub sorts by, so the request
 * reads the same as the stored ceiling it becomes.
 */
export function closeScopes(selected: readonly Scope[]): Scope[] {
  return [...closure(selected)].sort((a, b) => a - b)
}
