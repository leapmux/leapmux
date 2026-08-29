import { afterEach, describe, expect, it, vi } from 'vitest'
import { Scope } from '~/generated/leapmux/v1/scope_pb'
import { scopeToken } from './scopeToken'

describe('scopeToken derives RFC 6749 tokens from the enum name', () => {
  it('strips the SCOPE_ prefix and lowercases family and action', () => {
    expect(scopeToken(Scope.WORKSPACE_READ)).toBe('workspace:read')
    expect(scopeToken(Scope.ACCOUNT_WRITE)).toBe('account:write')
    expect(scopeToken(Scope.ADMIN_USERS)).toBe('admin:users')
  })

  it('renders a name with no FAMILY_ACTION shape raw rather than throwing', () => {
    // This runs in Solid render paths with no boundary below the app's
    // outermost one, so a throw here blanks the whole Preferences dialog on
    // one odd future scope name. The raw rendering stays visible (and the
    // Go-side bijection test fails CI before such a name ships); an empty
    // string would render a silent unlabeled checkbox instead.
    const error = vi.spyOn(console, 'error').mockImplementation(() => {})
    expect(scopeToken(Scope.UNSPECIFIED)).toBe('unspecified')
    expect(scopeToken(Scope.NEVER)).toBe('never')
    expect(error).toHaveBeenCalledTimes(2)
    error.mockRestore()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })
})
