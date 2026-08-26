import { beforeEach, describe, expect, it, vi } from 'vitest'

import { isServerRoute, postAuthNavigate } from './postAuthNavigate'

describe('isServerRoute', () => {
  it('claims the hub-served /auth/ family', () => {
    expect(isServerRoute('/auth/cli/start?state=s')).toBe(true)
    expect(isServerRoute('/auth/cli/activate')).toBe(true)
    expect(isServerRoute('/auth/oauth/github/reauth')).toBe(true)
  })

  it('leaves every SPA route to the router', () => {
    expect(isServerRoute('/')).toBe(false)
    expect(isServerRoute('/elevate?redirect=%2F')).toBe(false)
    expect(isServerRoute('/verify-email')).toBe(false)
    // A path that merely CONTAINS the prefix is not one: only a leading
    // match specifies a route the hub's mux serves.
    expect(isServerRoute('/workspace/auth/cli/start')).toBe(false)
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
    postAuthNavigate(navigate, undefined, '/home')
    expect(navigate).toHaveBeenCalledWith('/home', { replace: true })
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
