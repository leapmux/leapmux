import { Formatter, FracturedJsonOptions } from 'fracturedjsonjs'

const formatter = new Formatter()
const fmtOpts = new FracturedJsonOptions()
fmtOpts.MaxTotalLineLength = 80
fmtOpts.MaxInlineComplexity = 1
formatter.Options = fmtOpts

/** Pretty-print JSON text or a plain JS value using FracturedJson when possible. */
export function prettifyJson(input: unknown): string {
  const raw = typeof input === 'string'
    ? input
    : JSON.stringify(input)

  if (raw === undefined)
    return String(input)

  try {
    return formatter.Reformat(raw)
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
export function prettifyArgsJson(args: unknown): string {
  if (args === undefined || args === null)
    return ''
  if (typeof args === 'object' && !Array.isArray(args)
    && Object.keys(args as Record<string, unknown>).length === 0) {
    return ''
  }
  return prettifyJson(args)
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
