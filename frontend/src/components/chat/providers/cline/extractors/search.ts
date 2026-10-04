import type { SearchMatch, SearchResult } from '../../../model/searchResult'
import type { GrepRequest } from '../../../model/tools/grep'
import { pickString, stringArray } from '~/lib/jsonPick'
import { clineOperations } from './toolCommon'

/**
 * Cline's `search_codebase` tool.
 *
 *   {queries: ["regex", ...]}  ->  [{query, result, success}]
 *
 * One call runs several searches, and its result holds one record for each. A record's
 * `result` opens with a line that counts the matches, and then states each match as
 * `<file>:<line>:<column>`. A search that found nothing says so in words.
 */

const MATCH_LINE = /^(.+):(\d+):\d+$/

/** The patterns one call states. */
export function clineSearchRequest(args: Record<string, unknown>): GrepRequest {
  const queries = stringArray(args.queries)
  return { pattern: queries.length > 0 ? queries.join(' | ') : pickString(args, 'query'), paths: [] }
}

/** What one call found, across all its searches. */
export function clineSearchResult(output: unknown): SearchResult {
  const matches: SearchMatch[] = []
  const content: string[] = []
  const operations = clineOperations(output)
  for (const operation of operations) {
    const text = operation.success ? operation.result : operation.error
    content.push(text)
    for (const line of text.split('\n')) {
      const match = MATCH_LINE.exec(line.trim())
      if (match?.[1] && match[2])
        matches.push({ filePath: match[1], lineNumber: Number(match[2]), text: '' })
    }
  }
  const filenames = [...new Set(matches.map(match => match.filePath))]
  const text = content.join('\n\n')
  return {
    filenames,
    content: text,
    lines: matches,
    numFiles: filenames.length,
    numLines: matches.length,
    matchCount: matches.length,
    truncated: false,
    fallbackContent: text,
    // A search that ran and matched nothing states so in words and lists no match.
    empty: operations.length > 0 && matches.length === 0 && operations.every(operation => operation.success),
  }
}
