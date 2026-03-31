interface Logger {
  debug: (...args: unknown[]) => void
  info: (...args: unknown[]) => void
  warn: (...args: unknown[]) => void
  error: (...args: unknown[]) => void
}

const loggers = new Map<string, Logger>()

let debugEnabled = false

export function setDebugEnabled(enabled: boolean) {
  debugEnabled = enabled
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
    }
    loggers.set(name, logger)
  }
  return logger
}
