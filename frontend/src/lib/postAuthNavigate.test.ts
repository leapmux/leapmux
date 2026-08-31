import { beforeEach, describe, expect, it, vi } from 'vitest'

import { isServerRoute, postAuthNavigate } from './postAuthNavigate'

describe('isServerRoute', () => {
  it('claims the hub-served /auth/ family', () => {
    expect(isServerRoute('/oauth/authorize?state=s')).toBe(true)
    expect(isServerRoute('/oauth/device')).toBe(true)
    expect(isServerRoute('/auth/idp/github/reauth')).toBe(true)
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
    expect(isServerRoute('/recover-account')).toBe(false)
    expect(isServerRoute('/recover-account/complete?token=abc')).toBe(false)
    expect(isServerRoute('/auth/idp/complete-signup')).toBe(false)
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
    expect(isServerRoute('https://evil.example/oauth/authorize')).toBe(false)
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
    postAuthNavigate(navigate, '/oauth/authorize?state=s', '/')
    expect(assign).toHaveBeenCalledWith('/oauth/authorize?state=s')
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

/**
 * The authorization server's own addresses belong to the Go mux.
 *
 * `/oauth/authorize` and `/oauth/device` are consent pages the hub renders,
 * and both BOUNCE an un-elevated caller to `/elevate?redirect=...` — so the
 * address comes back here as a redirect target after the ceremony. Taking the
 * client-side branch would render the SPA's 404 page while the app that
 * started the flow waits for a consent screen nobody ever sees.
 *
 * This is a consequence of the inverted rule rather than a list: no route file
 * declares `/oauth/...`, so it is a server route by construction. The test
 * exists because the failure is silent and the fix would be to add a route
 * file, which is exactly the thing that would break it.
 */
describe('isServerRoute for the authorization server', () => {
  it.each([
    '/oauth/authorize',
    '/oauth/authorize?client_id=x&state=y',
    '/oauth/consent',
    '/oauth/device',
    '/oauth/device?user_code=ABC-DEF',
    '/oauth/token',
    '/oauth/revoke',
    '/oauth/register',
    '/oauth/step-up',
    '/.well-known/oauth-authorization-server',
    '/.well-known/oauth-protected-resource',
  ])('sends %s to the server', (target) => {
    expect(isServerRoute(target)).toBe(true)
  })

  // The INBOUND direction is the SPA's. `/auth/idp/complete-signup` is a real
  // route file, so it must take the client-side branch -- the two directions
  // are one letter apart in the URL and opposite here.
  it('keeps the sign-in provider completion page client-side', () => {
    expect(isServerRoute('/auth/idp/complete-signup')).toBe(false)
  })
})
