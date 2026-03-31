import { SourceMapConsumer } from 'source-map-js'

/** Cache of parsed source map consumers keyed by JS file URL. */
const consumerCache = new Map<string, SourceMapConsumer | null>()

/** Regex to parse stack frames like "name@url:line:col" or "at name (url:line:col)". */
const FRAME_RE = /(?:@| +at (?:.+? \()?)(https?:\/\/.+):(\d+):(\d+)\)?$/

/** Matches everything up to and including the last slash (for extracting filename). */
const LAST_SLASH_RE = /.*\//

/**
 * Resolve a minified Error stack trace to original source locations using
 * source maps fetched from the same origin.
 *
 * Returns the resolved stack string, or the original if resolution fails.
 */
export async function resolveStack(stack: string): Promise<string> {
  const lines = stack.split('\n')
  const resolved: string[] = []

  for (const line of lines) {
    const match = FRAME_RE.exec(line)
    if (!match) {
      resolved.push(line)
      continue
    }

    const [, url, lineStr, colStr] = match
    const lineNum = Number(lineStr)
    const colNum = Number(colStr)

    try {
      const consumer = await getConsumer(url)
      if (!consumer) {
        resolved.push(line)
        continue
      }

      const pos = consumer.originalPositionFor({ line: lineNum, column: colNum })
      if (pos.source) {
        const name = pos.name ?? ''
        const src = pos.source.replace(LAST_SLASH_RE, '')
        resolved.push(`    at ${name || '(anonymous)'} (${src}:${pos.line}:${pos.column})`)
      }
      else {
        resolved.push(line)
      }
    }
    catch {
      resolved.push(line)
    }
  }

  return resolved.join('\n')
}

async function getConsumer(jsUrl: string): Promise<SourceMapConsumer | null> {
  const cached = consumerCache.get(jsUrl)
  if (cached !== undefined)
    return cached

  try {
    const mapUrl = `${jsUrl}.map`
    const resp = await fetch(mapUrl)
    if (!resp.ok) {
      consumerCache.set(jsUrl, null)
      return null
    }

    const rawMap = await resp.json()
    const consumer = new SourceMapConsumer(rawMap)
    consumerCache.set(jsUrl, consumer)
    return consumer
  }
  catch {
    consumerCache.set(jsUrl, null)
    return null
  }
}
