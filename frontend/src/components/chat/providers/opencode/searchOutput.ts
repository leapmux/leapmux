import type { SearchResultLine } from '../../results/searchResult'

/** Read OpenCode's grouped grep output only when every row matches its native format. */
export function openCodeSearchLines(text: string, count: number, truncated = false): SearchResultLine[] | null {
  if (!Number.isSafeInteger(count) || count < 0)
    return null
  if (count === 0 && text.trim() === 'No files found')
    return []
  const rows = text.split(/\r?\n/)
  const heading = /^Found (\d+) matches(?: \(more matches available\))?$/.exec(rows.shift() ?? '')
  if (!heading || Number(heading[1]) !== count)
    return null
  if (heading[0].includes('(more matches available)') && !truncated)
    return null
  const matches: SearchResultLine[] = []
  let path = ''
  let pathHasMatch = false
  let ended = false
  for (const row of rows) {
    if (row === '')
      continue
    if (ended)
      return null
    if (truncated && row === '(Results truncated. Consider using a more specific path or pattern.)') {
      ended = true
      continue
    }
    const line = /^ {2}Line (\d+): (.*)$/.exec(row)
    if (line && path) {
      const lineNumber = Number(line[1])
      if (!Number.isSafeInteger(lineNumber) || lineNumber < 1)
        return null
      matches.push({ filePath: path, lineNumber, text: line[2] })
      pathHasMatch = true
      continue
    }
    if (/^(?:\/|[a-z]:[\\/]|\\\\).+:$/i.test(row)) {
      if (path && !pathHasMatch)
        return null
      path = row.slice(0, -1)
      pathHasMatch = false
      continue
    }
    return null
  }
  return matches.length === count && (!path || pathHasMatch) ? matches : null
}
