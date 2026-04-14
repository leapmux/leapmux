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
