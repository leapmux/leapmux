import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createLogger } from './logger'

describe('createLogger', () => {
  it('returns the same instance for the same name (singleton)', () => {
    const a = createLogger('singleton-test')
    const b = createLogger('singleton-test')
    expect(a).toBe(b)
  })

  it('returns different instances for different names', () => {
    const a = createLogger('name-a')
    const b = createLogger('name-b')
    expect(a).not.toBe(b)
  })

  describe('level gating', () => {
    let consoleSpy: ReturnType<typeof vi.spyOn>

    beforeEach(() => {
      // tslog 'pretty' type writes to console.log for all levels
      consoleSpy = vi.spyOn(console, 'log').mockImplementation(() => {})
    })

    afterEach(() => {
      consoleSpy.mockRestore()
    })

    it('suppresses debug output at default warn level', () => {
      const log = createLogger('level-debug')
      log.debug('should not appear')
      expect(consoleSpy).not.toHaveBeenCalled()
    })

    it('suppresses info output at default warn level', () => {
      const log = createLogger('level-info')
      log.info('should not appear')
      expect(consoleSpy).not.toHaveBeenCalled()
    })

    it('allows warn output at default warn level', () => {
      const log = createLogger('level-warn')
      log.warn('should appear')
      expect(consoleSpy).toHaveBeenCalled()
    })

    it('allows error output at default warn level', () => {
      const log = createLogger('level-error')
      log.error('should appear')
      expect(consoleSpy).toHaveBeenCalled()
    })
  })

  describe('transport attachment', () => {
    it('receives log entries via attached transport', () => {
      const log = createLogger('transport-test')
      const entries: unknown[] = []

      log.attachTransport((entry) => {
        entries.push(entry)
      })

      log.warn('test message')
      expect(entries.length).toBe(1)
    })
  })
})
