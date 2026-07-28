/// <reference types="vitest/globals" />
import { describe, expect, it } from 'vitest'
import { hasWorkspaceDesktopChrome } from './desktopChrome'

describe('hasWorkspaceDesktopChrome', () => {
  it('returns true for flat app home and workspace detail routes', () => {
    // Post-org removal the chrome routes are `/` and `/workspace/:id`
    // (no `/o/{orgSlug}` prefix). Empty pathname is treated as home.
    expect(hasWorkspaceDesktopChrome('/')).toBe(true)
    expect(hasWorkspaceDesktopChrome('')).toBe(true)
    expect(hasWorkspaceDesktopChrome('/workspace/ws1')).toBe(true)
    expect(hasWorkspaceDesktopChrome('/workspace/ws1/')).toBe(true)
  })

  it('returns false for non-workspace routes', () => {
    expect(hasWorkspaceDesktopChrome('/login')).toBe(false)
    expect(hasWorkspaceDesktopChrome('/setup')).toBe(false)
    expect(hasWorkspaceDesktopChrome('/verify-email')).toBe(false)
    expect(hasWorkspaceDesktopChrome('/oauth/complete-signup')).toBe(false)
    expect(hasWorkspaceDesktopChrome('/unknown')).toBe(false)
  })

  // The retired routes were `/o/{orgSlug}` and `/o/{orgSlug}/workspace/{id}`,
  // and they used to be the ONLY paths that got the chrome. They no longer
  // exist, so they must not match -- a regex that re-admitted them (or a
  // partial revert of the flattening) would light this up. `/org/...` is not
  // the retired prefix and never was; asserting on it pins nothing.
  it('returns false for the retired org-scoped routes', () => {
    expect(hasWorkspaceDesktopChrome('/o/admin')).toBe(false)
    expect(hasWorkspaceDesktopChrome('/o/admin/workspace/ws1')).toBe(false)
  })
})
