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
  })

  it('refuses a relative path', () => {
    expect(safeRedirect('settings/profile')).toBeUndefined()
  })

  it('refuses empty and missing values', () => {
    expect(safeRedirect('')).toBeUndefined()
    expect(safeRedirect(undefined)).toBeUndefined()
  })
})
