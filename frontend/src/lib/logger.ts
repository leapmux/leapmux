interface Logger {
  debug: (...args: unknown[]) => void
  info: (...args: unknown[]) => void
  warn: (...args: unknown[]) => void
  error: (...args: unknown[]) => void
  /**
   * True iff debug logging is currently enabled. Lets callers skip
   * building expensive payloads (e.g. allocating a per-tab snapshot
   * just for a `log.debug` call) when the message would be dropped.
   */
  isDebug: () => boolean
}

const loggers = new Map<string, Logger>()

let debugEnabled = false

/** The listeners of `onDebugEnabledChange`, in registration order. */
const debugListeners = new Set<(enabled: boolean) => void>()

/**
 * Turn debug logging on or off, and tell each `onDebugEnabledChange` listener
 * when the value changes. Setting the current value notifies nobody, so that a
 * listener that forwards the flag to a module which sets it again cannot loop.
 */
export function setDebugEnabled(enabled: boolean) {
  if (enabled === debugEnabled)
    return
  debugEnabled = enabled
  for (const listener of debugListeners) {
    try {
      listener(enabled)
    }
    catch (err) {
      // One failing listener must not keep the flag from the others.
      console.error('[logger] a debug flag listener failed:', err)
    }
  }
}

/**
 * Call `listener` with the new value each time `setDebugEnabled` changes the
 * flag. The channel session client uses this to forward the flag to its worker,
 * which has a logger of its own. Returns the function that removes the listener.
 */
export function onDebugEnabledChange(listener: (enabled: boolean) => void): () => void {
  // Wrap the listener, so that registering one function twice gives two
  // registrations, and each unsubscribe removes its own.
  const registration = (enabled: boolean) => listener(enabled)
  debugListeners.add(registration)
  return () => {
    debugListeners.delete(registration)
  }
}

export function isDebugEnabled(): boolean {
  return debugEnabled
}

export function createLogger(name: string): Logger {
  let logger = loggers.get(name)
  if (!logger) {
    const prefix = `[${name}]`
    logger = {
      debug: (...args: unknown[]) => {
        if (debugEnabled)
          // eslint-disable-next-line no-console
          console.debug(prefix, ...args)
      },
      // eslint-disable-next-line no-console
      info: (...args: unknown[]) => console.info(prefix, ...args),
      warn: (...args: unknown[]) => console.warn(prefix, ...args),
      error: (...args: unknown[]) => console.error(prefix, ...args),
      isDebug: () => debugEnabled,
    }
    loggers.set(name, logger)
  }
  return logger
}
