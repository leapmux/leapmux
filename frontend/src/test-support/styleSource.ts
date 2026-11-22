/**
 * Extract the inner text of a balanced `(...)` or `{...}` block starting at
 * `openIndex`, or `undefined` if `openIndex` does not point at `openChar` or
 * the block is unclosed.
 */
export function extractBalancedBlock(
  source: string,
  openIndex: number,
  openChar: '(' | '{',
  closeChar: ')' | '}',
): string | undefined {
  if (openIndex < 0 || openIndex >= source.length || source[openIndex] !== openChar)
    return undefined
  let depth = 0
  for (let i = openIndex; i < source.length; i++) {
    const ch = source[i]
    if (ch === openChar)
      depth++
    else if (ch === closeChar && --depth === 0)
      return source.slice(openIndex + 1, i)
  }
  return undefined
}

/**
 * Extract the argument list of `export const <exportName> = style(...)` from a
 * vanilla-extract `.css.ts` source file, covering both `style({ ... })` and
 * composed `style([base, { ... }])`. Throws if the export is missing or unclosed.
 */
export function extractStyleCallBlock(source: string, exportName: string): string {
  const marker = `export const ${exportName} = style(`
  const start = source.indexOf(marker)
  if (start < 0)
    throw new Error(`missing style export ${exportName}`)
  const open = source.indexOf('(', start)
  const block = extractBalancedBlock(source, open, '(', ')')
  if (block === undefined)
    throw new Error(`unclosed style call for ${exportName}`)
  return block
}
