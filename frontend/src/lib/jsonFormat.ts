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

/** Pretty-print JSON text or a plain JS value using FracturedJson when possible. */
export function prettifyJson(input: unknown, lineLength = DEFAULT_JSON_LINE_LENGTH): string {
  const raw = typeof input === 'string'
    ? input
    : JSON.stringify(input)

  if (raw === undefined)
    return String(input)

  try {
    const columns = Number.isFinite(lineLength) && lineLength >= 1 ? Math.floor(lineLength) : DEFAULT_JSON_LINE_LENGTH
    const selected = columns === DEFAULT_JSON_LINE_LENGTH ? formatter : createFormatter(columns)
    return selected.Reformat(raw)
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
