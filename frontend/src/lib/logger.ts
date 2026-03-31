interface Logger {
  debug: (...args: unknown[]) => void
  info: (...args: unknown[]) => void
  warn: (...args: unknown[]) => void
  error: (...args: unknown[]) => void
}

const loggers = new Map<string, Logger>()

export function createLogger(name: string): Logger {
  let logger = loggers.get(name)
  if (!logger) {
    const prefix = `[${name}]`
    logger = {
      // eslint-disable-next-line no-console
      debug: (...args: unknown[]) => console.debug(prefix, ...args),
      // eslint-disable-next-line no-console
      info: (...args: unknown[]) => console.info(prefix, ...args),
      warn: (...args: unknown[]) => console.warn(prefix, ...args),
      error: (...args: unknown[]) => console.error(prefix, ...args),
    }
    loggers.set(name, logger)
  }
  return logger
}
