import { afterEach, describe, expect, it } from 'vitest'
import { createLogger, isDebugEnabled, setDebugEnabled } from '~/lib/logger'
import { captureLoggerOutput } from './loggerCapture'

/**
 * The console method itself, read by name. `no-console` rejects the expression
 * `console.debug` even when a test only reads the method and never calls it.
 */
function consoleMethod(level: 'debug' | 'warn'): unknown {
  return Reflect.get(console, level)
}

describe('captureLoggerOutput', () => {
  afterEach(() => setDebugEnabled(false))

  it('records the debug lines that a disabled debug setting would drop', () => {
    const capture = captureLoggerOutput()
    try {
      createLogger('capture-debug').debug('first', { payload: 1 })
      expect(capture.messages('debug', 'capture-debug')).toEqual(['first'])
    }
    finally {
      capture.restore()
    }
  })

  it('keeps each level and each logger apart', () => {
    const capture = captureLoggerOutput()
    try {
      createLogger('capture-a').warn('a warning')
      createLogger('capture-b').debug('b debug')
      expect(capture.messages('warn', 'capture-a')).toEqual(['a warning'])
      expect(capture.messages('debug', 'capture-a')).toEqual([])
      expect(capture.messages('warn', 'capture-b')).toEqual([])
      expect(capture.messages('debug', 'capture-b')).toEqual(['b debug'])
    }
    finally {
      capture.restore()
    }
  })

  it('returns no messages for a logger that wrote nothing', () => {
    const capture = captureLoggerOutput()
    try {
      expect(capture.messages('warn', 'capture-silent')).toEqual([])
    }
    finally {
      capture.restore()
    }
  })

  it('restores the console methods and the debug setting that it found', () => {
    const debugBefore = consoleMethod('debug')
    const warnBefore = consoleMethod('warn')
    setDebugEnabled(false)

    const capture = captureLoggerOutput()
    expect(isDebugEnabled()).toBe(true)
    expect(consoleMethod('debug')).not.toBe(debugBefore)
    expect(consoleMethod('warn')).not.toBe(warnBefore)
    capture.restore()

    expect(isDebugEnabled()).toBe(false)
    expect(consoleMethod('debug')).toBe(debugBefore)
    expect(consoleMethod('warn')).toBe(warnBefore)
  })

  it('leaves debug logging on when it was on before the capture', () => {
    setDebugEnabled(true)
    const capture = captureLoggerOutput()
    capture.restore()
    expect(isDebugEnabled()).toBe(true)
  })
})
