/// <reference types="vitest/globals" />
import { describe, expect, it } from 'vitest'
import { hasWorkspaceDesktopChrome } from './desktopChrome'

describe('hasWorkspaceDesktopChrome', () => {
  it('returns true for the app home only', () => {
    // `/` is the whole authenticated app: no `/o/{orgSlug}` prefix, and no
    // per-workspace path either. Empty pathname is treated as home.
    expect(hasWorkspaceDesktopChrome('/')).toBe(true)
    expect(hasWorkspaceDesktopChrome('')).toBe(true)
  })

  it('returns false for non-workspace routes', () => {
    expect(hasWorkspaceDesktopChrome('/login')).toBe(false)
    expect(hasWorkspaceDesktopChrome('/setup')).toBe(false)
    expect(hasWorkspaceDesktopChrome('/verify-email')).toBe(false)
    expect(hasWorkspaceDesktopChrome('/auth/idp/complete-signup')).toBe(false)
    expect(hasWorkspaceDesktopChrome('/unknown')).toBe(false)
  })

  // Two generations of retired routes, both of which USED to get the chrome:
  // `/o/{orgSlug}[/workspace/{id}]` before the org flattening, and
  // `/workspace/{id}` before workspaces stopped having a path at all. Either
  // one re-admitted by a partial revert lights this up, and `/workspace/...`
  // matters most: it now resolves to the 404 catch-all, which must render
  // without the app titlebar and sidebar. `/org/...` is not the retired prefix
  // and never was; asserting on it pins nothing.
  it('returns false for the retired org-scoped and workspace routes', () => {
    expect(hasWorkspaceDesktopChrome('/o/admin')).toBe(false)
    expect(hasWorkspaceDesktopChrome('/o/admin/workspace/ws1')).toBe(false)
    expect(hasWorkspaceDesktopChrome('/workspace')).toBe(false)
    expect(hasWorkspaceDesktopChrome('/workspace/ws1')).toBe(false)
    expect(hasWorkspaceDesktopChrome('/workspace/ws1/')).toBe(false)
  })
})
