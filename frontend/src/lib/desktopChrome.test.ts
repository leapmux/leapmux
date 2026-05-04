/// <reference types="vitest/globals" />
import { describe, expect, it } from 'vitest'
import { hasWorkspaceDesktopChrome } from './desktopChrome'

describe('hasWorkspaceDesktopChrome', () => {
  it('returns true for workspace chrome routes', () => {
    expect(hasWorkspaceDesktopChrome('/o/acme')).toBe(true)
    expect(hasWorkspaceDesktopChrome('/o/acme/')).toBe(true)
    expect(hasWorkspaceDesktopChrome('/o/acme/workspace/ws1')).toBe(true)
  })

  it('returns false for non-workspace routes', () => {
    expect(hasWorkspaceDesktopChrome('/login')).toBe(false)
    expect(hasWorkspaceDesktopChrome('/setup')).toBe(false)
    expect(hasWorkspaceDesktopChrome('/verify-email')).toBe(false)
    expect(hasWorkspaceDesktopChrome('/oauth/complete-signup')).toBe(false)
    expect(hasWorkspaceDesktopChrome('/o/acme/org')).toBe(false)
    expect(hasWorkspaceDesktopChrome('/unknown')).toBe(false)
  })
})
