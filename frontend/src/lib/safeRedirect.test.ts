import { describe, expect, it } from 'vitest'
import { safeRedirect } from './safeRedirect'

describe('safeRedirect', () => {
  it('accepts an absolute app path', () => {
    expect(safeRedirect('/settings/profile')).toBe('/settings/profile')
  })

  it('accepts the root path', () => {
    expect(safeRedirect('/')).toBe('/')
  })

  it('refuses a protocol-relative URL', () => {
    expect(safeRedirect('//evil.example.com')).toBeUndefined()
  })

  it('refuses an external URL', () => {
    expect(safeRedirect('https://evil.example.com')).toBeUndefined()
  })

  it('refuses a scheme-relative scheme prefix without a host', () => {
    expect(safeRedirect('/\\evil.example.com')).toBeUndefined()
    expect(safeRedirect('/\\evil.example.com/callback')).toBeUndefined()
  })

  // A browser strips these during URL parsing, so "/\t/host" becomes
  // "//host" and leaves the origin. The backend guard refuses the same
  // class; the two must agree, because the hub forwards this value into
  // the OAuth start URL.
  it('refuses a control byte anywhere in the path', () => {
    expect(safeRedirect('/\t/evil.example.com')).toBeUndefined()
    expect(safeRedirect('/\t\\evil.example.com')).toBeUndefined()
    expect(safeRedirect('/\r/evil.example.com')).toBeUndefined()
    expect(safeRedirect('/\n/evil.example.com')).toBeUndefined()
    expect(safeRedirect('/\x00/evil.example.com')).toBeUndefined()
    expect(safeRedirect('/\x7F/evil.example.com')).toBeUndefined()
  })

  // Percent-encodings are not decoded before the browser picks the
  // authority, so they stay ordinary same-origin paths.
  it('accepts a percent-encoded byte that a browser does not decode', () => {
    expect(safeRedirect('/%09/settings')).toBe('/%09/settings')
    expect(safeRedirect('/%5Csettings')).toBe('/%5Csettings')
  })

  it('refuses a relative path', () => {
    expect(safeRedirect('settings/profile')).toBeUndefined()
  })

  it('refuses empty and missing values', () => {
    expect(safeRedirect('')).toBeUndefined()
    expect(safeRedirect(undefined)).toBeUndefined()
  })
})
