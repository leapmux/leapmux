import { beforeEach, describe, expect, it, vi } from 'vitest'

import { isServerRoute, postAuthNavigate } from './postAuthNavigate'

describe('isServerRoute', () => {
  it('claims the hub-served /auth/ family', () => {
    expect(isServerRoute('/auth/cli/start?state=s')).toBe(true)
    expect(isServerRoute('/auth/cli/activate')).toBe(true)
    expect(isServerRoute('/auth/oauth/github/reauth')).toBe(true)
  })

  // The hand-written list this replaced held `/auth/` alone, so each of these
  // took the client-side branch and rendered the SPA's own 404 page -- the
  // exact failure this module exists to prevent. Nothing linked that list to
  // the Go mux, so it could only drift; this module asks the question the
  // other way round now, against the SPA's own route files.
  it('claims every other address the hub serves', () => {
    expect(isServerRoute('/ws/channel')).toBe(true)
    expect(isServerRoute('/ws/userevents?workspace_ids=w-1')).toBe(true)
    expect(isServerRoute('/metrics')).toBe(true)
    expect(isServerRoute('/version')).toBe(true)
    expect(isServerRoute('/worker/delegation-tokens/mint')).toBe(true)
    expect(isServerRoute('/leapmux.v1.AuthService/Login')).toBe(true)
  })

  it('leaves every SPA route to the router', () => {
    expect(isServerRoute('/')).toBe(false)
    expect(isServerRoute('/elevate?redirect=%2F')).toBe(false)
    expect(isServerRoute('/verify-email')).toBe(false)
    expect(isServerRoute('/login')).toBe(false)
    expect(isServerRoute('/signup')).toBe(false)
    expect(isServerRoute('/setup')).toBe(false)
    expect(isServerRoute('/forgot-password')).toBe(false)
    expect(isServerRoute('/reset-password?token=abc')).toBe(false)
    expect(isServerRoute('/oauth/complete-signup')).toBe(false)
  })

  // The route matcher trims a trailing slash before it matches, so these are
  // one address to it and must be one address here.
  it('reads a trailing slash and a fragment as the same address', () => {
    expect(isServerRoute('/login/')).toBe(false)
    expect(isServerRoute('/')).toBe(false)
    expect(isServerRoute('/login#top')).toBe(false)
  })

  // The catch-all route matches EVERY address, so counting it would make every
  // target look client-side. That route IS the 404 page.
  it('does not let the catch-all route claim an unknown address', () => {
    expect(isServerRoute('/no-such-page')).toBe(true)
  })

  // Only a same-origin absolute path can belong to the hub's mux. Claiming an
  // absolute URL would turn the full-document branch into an off-origin
  // navigation for a caller that did not filter its input first.
  it('claims nothing that leaves the origin', () => {
    expect(isServerRoute('https://evil.example/auth/cli/start')).toBe(false)
    expect(isServerRoute('//evil.example/metrics')).toBe(false)
    expect(isServerRoute('javascript:alert(1)')).toBe(false)
    expect(isServerRoute('')).toBe(false)
  })

  // The glob resolves at build time. An empty result would make every target a
  // server route, so every post-authentication navigation would become a
  // full-document load -- which works, and hides that the derivation broke.
  it('found the route files at all', () => {
    expect(isServerRoute('/login')).toBe(false)
    expect(isServerRoute('/elevate')).toBe(false)
  })
})

describe('postAuthNavigate', () => {
  const navigate = vi.fn()
  const assign = vi.fn()

  beforeEach(() => {
    vi.clearAllMocks()
    vi.stubGlobal('location', { assign })
  })

  it('hands a hub route back to the server with a full-document load', () => {
    postAuthNavigate(navigate, '/auth/cli/start?state=s', '/')
    expect(assign).toHaveBeenCalledWith('/auth/cli/start?state=s')
    expect(navigate).not.toHaveBeenCalled()
  })

  it('routes an in-app path client-side', () => {
    postAuthNavigate(navigate, '/verify-email', '/')
    expect(navigate).toHaveBeenCalledWith('/verify-email', { replace: true })
    expect(assign).not.toHaveBeenCalled()
  })

  it('falls back when the target is absent', () => {
    postAuthNavigate(navigate, undefined, '/')
    expect(navigate).toHaveBeenCalledWith('/', { replace: true })
    expect(assign).not.toHaveBeenCalled()
  })

  it('refuses a target that could leave the origin, on BOTH branches', () => {
    for (const hostile of ['https://evil.example/', '//evil.example/', '/\\evil.example/', '/\t/evil.example']) {
      vi.clearAllMocks()
      postAuthNavigate(navigate, hostile, '/')
      expect(navigate).toHaveBeenCalledWith('/', { replace: true })
      expect(assign).not.toHaveBeenCalled()
    }
  })
})
