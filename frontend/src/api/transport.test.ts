import { describe, expect, it } from 'vitest'
import * as transportModule from './transport'

describe('transport', () => {
  it('does not export getToken', () => {
    expect('getToken' in transportModule).toBe(false)
  })

  it('does not export setToken', () => {
    expect('setToken' in transportModule).toBe(false)
  })

  it('does not export clearToken', () => {
    expect('clearToken' in transportModule).toBe(false)
  })

  it('exports transport', () => {
    expect(transportModule.transport).toBeDefined()
  })

  it('exports setOnAuthError', () => {
    expect(typeof transportModule.setOnAuthError).toBe('function')
  })

  it('does not export authInterceptor', () => {
    expect('authInterceptor' in transportModule).toBe(false)
  })

  it('does not export TOKEN_KEY', () => {
    expect('TOKEN_KEY' in transportModule).toBe(false)
  })
})
