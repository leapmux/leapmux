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
      debugSpy.mockRestore()
      infoSpy.mockRestore()
      warnSpy.mockRestore()
      errorSpy.mockRestore()
    })

    it('delegates debug to console.debug with prefix', () => {
      const log = createLogger('test-debug')
      log.debug('msg')
      expect(debugSpy).toHaveBeenCalledWith('[test-debug]', 'msg')
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
  })
})
