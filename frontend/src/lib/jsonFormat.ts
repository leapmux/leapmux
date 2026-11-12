import { Formatter, FracturedJsonOptions } from 'fracturedjsonjs'

export const DEFAULT_JSON_LINE_LENGTH = 80

function createFormatter(lineLength: number): Formatter {
  const formatter = new Formatter()
  const options = new FracturedJsonOptions()
  options.MaxTotalLineLength = lineLength
  options.MaxInlineComplexity = 1
  formatter.Options = options
  return formatter
}

const formatter = createFormatter(DEFAULT_JSON_LINE_LENGTH)

/**
 * The longest JSON text, in UTF-16 code units, that prettifyJson lays out with
 * FracturedJson.
 *
 * FracturedJson costs about 0.35 ms per KiB of structured JSON in V8, 15 to 20 times
 * what the native `JSON.stringify` costs, and it runs on the main thread, mostly
 * while a chat row extracts or renders. Up to this length a layout stays near 6 ms.
 * A longer text gets the native indented layout, which uses more lines.
 */
export const FRACTURED_JSON_MAX_LENGTH = 16 * 1024

/**
 * Pretty-print JSON text or a plain JS value: with FracturedJson up to
 * FRACTURED_JSON_MAX_LENGTH, with the native indentation above it.
 */
export function prettifyJson(input: unknown, lineLength = DEFAULT_JSON_LINE_LENGTH): string {
  const raw = typeof input === 'string'
    ? input
    : JSON.stringify(input)

  if (raw === undefined)
    return String(input)

  if (raw.length > FRACTURED_JSON_MAX_LENGTH)
    return indentJson(input, raw)

  try {
    const columns = Number.isFinite(lineLength) && lineLength >= 1
      ? Math.floor(lineLength)
      : DEFAULT_JSON_LINE_LENGTH
    const selected = columns === DEFAULT_JSON_LINE_LENGTH
      ? formatter
      : createFormatter(columns)
    return selected.Reformat(raw)
  }
  catch {
    return raw
  }
}

/** Lay out `raw` with the native indentation, or return it unchanged when it is not JSON. */
function indentJson(input: unknown, raw: string): string {
  try {
    const value: unknown = typeof input === 'string'
      ? JSON.parse(raw)
      : input
    return JSON.stringify(value, null, formatter.Options.IndentSpaces)
  }
  catch {
    return raw
  }
}

/**
 * Pretty-print a tool-args object, returning `''` for null/undefined and for
 * empty objects. Used by MCP extractors so that an absent or `{}` `arguments`
 * field renders as no body rather than as a literal empty `{}`.
 */
export function prettifyArgsJson(args: unknown, lineLength?: number): string {
  if (args === undefined || args === null)
    return ''
  if (typeof args === 'object' && !Array.isArray(args)
    && Object.keys(args as Record<string, unknown>).length === 0) {
    return ''
  }
  return prettifyJson(args, lineLength)
}

/**
 * Pretty-print MCP `structuredContent`, returning `undefined` when the value
 * is absent or empty. Per MCP spec the field is a JSON value; in practice
 * it's always an object/array, but we accept any non-null/non-empty value
 * so neither Claude nor Codex silently drops a payload.
 */
export function prettifyStructuredJson(input: unknown): string | undefined {
  if (input === undefined || input === null || input === '')
    return undefined
  return prettifyJson(input)
}
