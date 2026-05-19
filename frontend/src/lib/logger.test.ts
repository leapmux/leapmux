import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createLogger, isDebugEnabled, setDebugEnabled } from './logger'

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

  describe('output delegation', () => {
    let debugSpy: ReturnType<typeof vi.spyOn>
    let infoSpy: ReturnType<typeof vi.spyOn>
    let warnSpy: ReturnType<typeof vi.spyOn>
    let errorSpy: ReturnType<typeof vi.spyOn>

    beforeEach(() => {
      debugSpy = vi.spyOn(console, 'debug').mockImplementation(() => {})
      infoSpy = vi.spyOn(console, 'info').mockImplementation(() => {})
      warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {})
      errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    })

    afterEach(() => {
      setDebugEnabled(false)
      debugSpy.mockRestore()
      infoSpy.mockRestore()
      warnSpy.mockRestore()
      errorSpy.mockRestore()
    })

    it('suppresses debug by default', () => {
      const log = createLogger('test-debug-off')
      log.debug('msg')
      expect(debugSpy).not.toHaveBeenCalled()
    })

    it('delegates debug to console.debug when enabled', () => {
      setDebugEnabled(true)
      const log = createLogger('test-debug-on')
      log.debug('msg')
      expect(debugSpy).toHaveBeenCalledWith('[test-debug-on]', 'msg')
    })

    it('delegates info to console.info with prefix', () => {
      const log = createLogger('test-info')
      log.info('msg')
      expect(infoSpy).toHaveBeenCalledWith('[test-info]', 'msg')
    })

    it('delegates warn to console.warn with prefix', () => {
      const log = createLogger('test-warn')
      log.warn('msg')
      expect(warnSpy).toHaveBeenCalledWith('[test-warn]', 'msg')
    })

    it('delegates error to console.error with prefix', () => {
      const log = createLogger('test-error')
      log.error('msg')
      expect(errorSpy).toHaveBeenCalledWith('[test-error]', 'msg')
    })

    it('passes multiple arguments', () => {
      const log = createLogger('test-multi')
      const err = new Error('boom')
      log.warn('failed', err)
      expect(warnSpy).toHaveBeenCalledWith('[test-multi]', 'failed', err)
    })

    it('does not affect info/warn/error when debug is disabled', () => {
      const log = createLogger('test-other-levels')
      log.info('i')
      log.warn('w')
      log.error('e')
      expect(infoSpy).toHaveBeenCalledWith('[test-other-levels]', 'i')
      expect(warnSpy).toHaveBeenCalledWith('[test-other-levels]', 'w')
      expect(errorSpy).toHaveBeenCalledWith('[test-other-levels]', 'e')
    })
  })

  describe('isDebug predicate', () => {
    afterEach(() => setDebugEnabled(false))

    it('mirrors setDebugEnabled state for both the module helper and per-logger isDebug', () => {
      const log = createLogger('test-isdebug')
      // Default state: debug off, both predicates agree.
      expect(isDebugEnabled()).toBe(false)
      expect(log.isDebug()).toBe(false)
      setDebugEnabled(true)
      expect(isDebugEnabled()).toBe(true)
      expect(log.isDebug()).toBe(true)
      setDebugEnabled(false)
      expect(isDebugEnabled()).toBe(false)
      expect(log.isDebug()).toBe(false)
    })

    it('lets callers skip expensive payload construction when debug is disabled', () => {
      // Stand-in for the moveTabToTile pattern: a payload-building thunk
      // that should NOT run when debug is disabled. The whole point of
      // log.isDebug() is to gate this without a separate import.
      const log = createLogger('test-payload-gate')
      const buildPayload = vi.fn(() => ({ heavy: 'allocation' }))
      if (log.isDebug())
        log.debug('moveTabToTile:start', buildPayload())
      expect(buildPayload).not.toHaveBeenCalled()
      // Enable debug — the same call site now runs the builder.
      setDebugEnabled(true)
      if (log.isDebug())
        log.debug('moveTabToTile:start', buildPayload())
      expect(buildPayload).toHaveBeenCalledTimes(1)
    })
  })
})
