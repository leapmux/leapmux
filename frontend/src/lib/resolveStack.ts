import { SourceMapConsumer } from 'source-map-js'

/**
 * Cache of parsed source map consumers (or in-flight promises) keyed by JS file URL.
 * Unbounded, but practically limited to the small set of static JS bundles produced by Vite.
 */
const consumerCache = new Map<string, Promise<SourceMapConsumer | null>>()

/** Regex to parse stack frames like "name@url:line:col" or "at name (url:line:col)". */
const FRAME_RE = /(?:@| +at (?:.+? \()?)(\w+:\/\/.+):(\d+):(\d+)\)?$/

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

  // Pre-fetch all unique source map consumers in parallel.
  const urlsToFetch = new Set<string>()
  for (const line of lines) {
    const match = FRAME_RE.exec(line)
    if (match)
      urlsToFetch.add(match[1])
  }
  await Promise.all(Array.from(urlsToFetch, url => getConsumer(url)))

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

function getConsumer(jsUrl: string): Promise<SourceMapConsumer | null> {
  const cached = consumerCache.get(jsUrl)
  if (cached)
    return cached

  const promise = fetchConsumer(jsUrl)
  consumerCache.set(jsUrl, promise)
  return promise
}

async function fetchConsumer(jsUrl: string): Promise<SourceMapConsumer | null> {
  try {
    const mapUrl = `${jsUrl}.map`
    const resp = await fetch(mapUrl)
    if (!resp.ok)
      return null

    const rawMap = await resp.json()
    return new SourceMapConsumer(rawMap)
  }
  catch {
    return null
  }
}
