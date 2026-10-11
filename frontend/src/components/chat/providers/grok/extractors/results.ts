import type { CommandExit } from '../../../model/commandResult'
import type { FileListEntry, SearchMatch, SearchResult } from '../../../model/searchResult'
import type { ListResult } from '../../../model/tools/list'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'

/**
 * The `rawOutput` of one Grok Build call, which states its own shape under `type`.
 *
 * Grok writes every output as a tagged union (`{type: "Bash", ...}`), and a struct
 * variant nests its fields one level down (`{type: "ListDir", Content: {...}}`).
 */
export function grokRawOutput(tool: Record<string, unknown>, type: string): Record<string, unknown> | undefined {
  const raw = pickObject(tool, 'rawOutput')
  return raw && pickString(raw, 'type') === type ? raw : undefined
}

/**
 * A byte array or a string, as text.
 *
 * Grok serializes a captured stream as the array of its bytes, and it states the
 * same stream as a string in other places. Both forms decode here, and anything else
 * is no stream.
 */
export function grokStreamText(value: unknown): string {
  if (typeof value === 'string')
    return value
  if (!Array.isArray(value) || !value.every(byte => Number.isInteger(byte) && byte >= 0 && byte <= 255))
    return ''
  return new TextDecoder().decode(Uint8Array.from(value as number[]))
}

/**
 * How one shell call ended, from the `Bash` output Grok states.
 *
 * Grok's `exit_code` is always a number (an `i32`). A process that a signal ended
 * has no code of its own, so Grok states a stand-in code beside the signal. The
 * signal therefore wins whenever Grok states one, and the code answers otherwise.
 */
export function grokCommandExit(tool: Record<string, unknown>): CommandExit | undefined {
  const raw = grokRawOutput(tool, 'Bash')
  if (!raw)
    return undefined
  const signal = pickString(raw, 'signal')
  const code = pickNumber(raw, 'exit_code')
  if (signal)
    return { signal }
  return code !== null ? { exitCode: code } : undefined
}

/** The prefix of one entry in Grok's directory tree: its indent, then `- `. */
const TREE_ENTRY = /^( *)- (.+)$/

/**
 * The entries of Grok's directory tree, with each path relative to the listed root.
 *
 * Grok prints the root first (`- /abs/root/`), then each entry two spaces deeper
 * than its parent directory. A directory ends with `/`. Returns null for text that is
 * not that tree, so the caller keeps the words Grok printed.
 */
export function grokDirectoryEntries(text: string): FileListEntry[] | null {
  const lines = text.split('\n').filter(line => line.trim() !== '')
  const [root, ...rest] = lines
  if (root === undefined || !TREE_ENTRY.test(root))
    return null
  const parents: string[] = []
  const entries: FileListEntry[] = []
  for (const line of rest) {
    const match = TREE_ENTRY.exec(line)
    if (!match)
      return null
    const depth = Math.floor((match[1] ?? '').length / 2)
    const name = match[2] ?? ''
    if (depth < 1)
      return null
    parents.length = depth - 1
    const path = [...parents, name].join('')
    entries.push({ path })
    if (name.endsWith('/'))
      parents.push(name)
  }
  return entries
}

/** The directory listing one completed `list_dir` states, or null when it states none. */
export function grokListResult(tool: Record<string, unknown>): ListResult | null {
  const content = pickString(pickObject(grokRawOutput(tool, 'ListDir'), 'Content'), 'content')
  const entries = content ? grokDirectoryEntries(content) : null
  return entries ? { entries } : null
}

/**
 * The matches one completed `grep` states.
 *
 * Grok counts its matches and groups them by file under `file_matches`, so the paths
 * never have to be split out of the printed lines. The printed text stays the
 * fallback, because that is what the model read.
 */
export function grokGrepResult(tool: Record<string, unknown>): SearchResult | null {
  const raw = grokRawOutput(tool, 'GrepSearch')
  if (!raw || !Array.isArray(raw.file_matches))
    return null
  const lines: SearchMatch[] = raw.file_matches.filter(isObject).flatMap((file) => {
    const filePath = pickString(file, 'path')
    const matches = Array.isArray(file.matches) ? file.matches.filter(isObject) : []
    return matches.map((match) => {
      const lineNumber = pickNumber(match, 'line_number')
      return { filePath, ...(lineNumber !== null ? { lineNumber } : {}), text: pickString(match, 'content') }
    })
  })
  const filenames = [...new Set(lines.map(line => line.filePath).filter(Boolean))]
  const matchCount = pickNumber(raw, 'match_count') ?? lines.length
  return {
    filenames,
    content: '',
    lines,
    numFiles: filenames.length,
    numLines: lines.length,
    matchCount,
    truncated: false,
    fallbackContent: grokStreamText(raw.stdout),
    empty: matchCount === 0,
  }
}
