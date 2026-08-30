import { describe, expect, it } from 'vitest'
import { isGrantableScope, SCOPE_CATEGORIES } from '~/generated/contracts/scopes'
import { Scope } from '~/generated/proto/leapmux/v1/scope_pb'
import { scopeToken } from './scopeToken'

describe('scopeToken derives RFC 6749 tokens from the contract', () => {
  it('renders the contract token for grantable scopes', () => {
    expect(scopeToken(Scope.WORKSPACE_READ)).toBe('workspace:read')
    expect(scopeToken(Scope.ACCOUNT_WRITE)).toBe('account:write')
    expect(scopeToken(Scope.ADMIN_USERS)).toBe('admin:users')
  })

  it('carries a family:action token for every scope the catalogue renders', () => {
    // Totality: the generator emits GrantableScope and SCOPE_TOKENS from the
    // same contracts/scopes.json entries, so this loop cannot meet a scope
    // without a token -- and each token must read like a wire token, not a
    // fallback or an empty string.
    for (const category of SCOPE_CATEGORIES) {
      for (const scope of category.scopes)
        expect(scopeToken(scope)).toMatch(/^[a-z-]+:[a-z-]+$/)
    }
  })

  it('narrows hub wire data down to the grantable half', () => {
    // app.scopes arrives typed as the full enum; the three non-grantable
    // values are constructions (UNSPECIFIED, NEVER, ALL), not permissions.
    expect(isGrantableScope(Scope.WORKSPACE_READ)).toBe(true)
    expect(isGrantableScope(Scope.UNSPECIFIED)).toBe(false)
    expect(isGrantableScope(Scope.NEVER)).toBe(false)
    expect(isGrantableScope(Scope.ALL)).toBe(false)
  })
})
