import type { GrantableScope } from '~/generated/contracts/scopes'
import { SCOPE_TOKENS } from '~/generated/contracts/scopes'

/**
 * One scope's RFC 6749 section 3.3 token.
 *
 * The lookup is total by construction: the generator emits GrantableScope
 * and SCOPE_TOKENS from the same contracts/scopes.json entries (the same
 * table the Go side's authscope reads), so every GrantableScope carries a
 * token and a scope without one cannot typecheck into this function.
 *
 * Hub wire data types as the full Scope enum; narrow it with
 * isGrantableScope before calling.
 */
export function scopeToken(scope: GrantableScope): string {
  return SCOPE_TOKENS[scope]
}
