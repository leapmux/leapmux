import { vi } from 'vitest'
import { isDebugEnabled, setDebugEnabled } from '~/lib/logger'

/** A console level that `createLogger` writes to and that a capture records. */
export type CapturedLevel = 'debug' | 'warn'

export interface LoggerCapture {
  /**
   * The message of each line that the logger `name` wrote at `level`: the
   * argument after the `[name]` prefix. The payload object is left out, so a
   * test asserts on the text it cares about.
   */
  messages: (level: CapturedLevel, name: string) => unknown[]
  /** Restore the console methods and the debug setting that the capture changed. */
  restore: () => void
}

/**
 * Capture what `createLogger` loggers write to `console.debug` and
 * `console.warn`, with debug logging ON.
 *
 * Debug is on because the LEVEL of a line is the behavior under test. A test
 * that asserts a line moved from warn to debug must see the debug line, or it
 * cannot tell a demoted line from a deleted one.
 *
 * The capture also silences both methods, so an expected warning does not
 * clutter the test output. Call `restore` in an `afterEach`, because a failed
 * assertion skips the rest of the test.
 */
export function captureLoggerOutput(): LoggerCapture {
  const debugWasEnabled = isDebugEnabled()
  setDebugEnabled(true)
  const spies = {
    debug: vi.spyOn(console, 'debug').mockImplementation(() => {}),
    warn: vi.spyOn(console, 'warn').mockImplementation(() => {}),
  }
  return {
    messages: (level, name) => spies[level].mock.calls
      .filter(args => args[0] === `[${name}]`)
      .map(args => args[1]),
    restore: () => {
      spies.debug.mockRestore()
      spies.warn.mockRestore()
      setDebugEnabled(debugWasEnabled)
    },
  }
}
