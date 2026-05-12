import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { randomUUID } from './idGenerator'

const UUID_V4_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/

describe('randomUUID', () => {
  it('returns a RFC 4122 v4 UUID using crypto.randomUUID when present', () => {
    expect(typeof crypto.randomUUID).toBe('function')
    expect(randomUUID()).toMatch(UUID_V4_RE)
  })

  describe('non-secure-context fallback (crypto.randomUUID absent)', () => {
    let original: typeof crypto.randomUUID | undefined

    beforeEach(() => {
      original = crypto.randomUUID?.bind(crypto)
      // Simulate a non-secure context (plain-HTTP non-localhost page) where
      // `crypto.randomUUID` is gone but `crypto.getRandomValues` remains.
      Object.defineProperty(crypto, 'randomUUID', {
        configurable: true,
        value: undefined,
      })
    })

    afterEach(() => {
      Object.defineProperty(crypto, 'randomUUID', {
        configurable: true,
        value: original,
      })
    })

    it('returns a RFC 4122 v4 UUID via the getRandomValues fallback', () => {
      const id = randomUUID()
      expect(id).toMatch(UUID_V4_RE)
    })

    it('produces unique values across many invocations', () => {
      const ids = new Set<string>()
      for (let i = 0; i < 1000; i++) {
        ids.add(randomUUID())
      }
      expect(ids.size).toBe(1000)
    })
  })
})
