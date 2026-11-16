import type { ReadFileResult } from '../../../model/readFileResult'

/** Read numbered native text only when the declared range matches every row. */
export function openCodeReadResult(text: string): ReadFileResult | null {
  const rows = text.split(/\r?\n/)
  const heading = rows.shift() ?? ''
  if (/^Read file .+, 0 lines$/.test(heading) && rows.length === 0)
    return { lines: [], fallbackContent: '' }
  const range = /^Read file .+, lines (\d+)-(\d+)(?: \(offset \d+ is past the end; the file has \d+ lines?\))?$/.exec(heading)
  if (!range)
    return null
  const first = Number(range[1])
  const last = Number(range[2])
  if (!Number.isSafeInteger(first) || !Number.isSafeInteger(last) || first < 1 || last < first)
    return null
  if (/^\[Output truncated\. Continue reading with offset: \d+\]$/.test(rows.at(-1) ?? ''))
    rows.pop()
  const lines = []
  for (const row of rows) {
    const match = /^(\d+): (.*)$/.exec(row)
    if (!match || Number(match[1]) !== first + lines.length)
      return null
    lines.push({
      num: Number(match[1]),
      text: match[2] ?? '',
    })
  }
  if (lines.length !== last - first + 1)
    return null
  return {
    lines,
    fallbackContent: lines.map(line => line.text).join('\n'),
  }
}
