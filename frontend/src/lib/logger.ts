import { Logger } from 'tslog'

// Root logger: only warn+ goes to console by default.
// tslog levels: 0=silly, 1=trace, 2=debug, 3=info, 4=warn, 5=error, 6=fatal
const rootLogger = new Logger({
  name: 'leapmux',
  minLevel: 4,
  type: 'pretty',
  stylePrettyLogs: false,
})

const loggers = new Map<string, Logger<unknown>>()

export function createLogger(name: string) {
  let logger = loggers.get(name)
  if (!logger) {
    logger = rootLogger.getSubLogger({ name })
    loggers.set(name, logger)
  }
  return logger
}
