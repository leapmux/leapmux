import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { DEEPSEEK_HARNESS_EVENT } from '~/generated/contracts/deepseek-harness-protocol'
import { isObject, pickObject } from '~/lib/jsonPick'
import { isFilesystemPath } from '~/lib/paths'
import { deepseekHarnessEventData } from '../protocol'

const STREAM_FILE = /[\\/]dsh-subprocess-[\w-]+[\\/]dsh-subprocess-[1-9]\d*-[1-9]\d*-[0-9a-f]{12}-(?:stdout|stderr)\.log$/u
// The native spill store writes `<root>/session-<12 hex>/<12 hex>-<encoded name>`.
// The encoded name keeps [A-Za-z0-9._-] and escapes each other UTF-16 code unit as ~XXXX.
// The root is configurable, so only the session directory and the file name identify the file.
const STORED_RESULT_FILE = /[\\/]session-[0-9a-f]{12}[\\/][0-9a-f]{12}-(?:[\w.-]|~[0-9A-F]{4})*\.txt$/u

interface NativeNotice {
  readonly pattern: RegExp
  /** The paths that the text of capture group 1 lists. */
  readonly listed: (captured: string) => readonly string[]
  /** The native layout that each listed path must have. */
  readonly layout: RegExp
}

// Native sources, `@deepseek-ai/dsh` 0.2.0-rc.2.
// A notice that states "(unavailable)" or "could not be saved" names no file, so no pattern matches it.
const NOTICES: readonly NativeNotice[] = [
  // `dsh-tool-bash` and `dsh-tool-pwsh` streamText. One stream file.
  {
    pattern: /(?:^|\n)\[output truncated; full output: ([^\]\r\n]+)\]/gu,
    listed: captured => [captured],
    layout: STREAM_FILE,
  },
  // `dsh-tool-bash` and `dsh-tool-pwsh` renderJobRead, and `dsh-tool-jobs` renderModelDelta.
  // Every spill file of the job, joined with ", ". A path that holds ", " has no unique reading,
  // so its fragments fail the layout check and the extraction refuses them.
  {
    pattern: /(?:^|\n)\[some output was dropped from memory; full output: ([^\]\r\n]+)\]/gu,
    listed: captured => captured.split(', '),
    layout: STREAM_FILE,
  },
  // `dsh-spill-policy` formatSpillNotice (` Full formatted result stored at`), `dsh-tool-fs-search`
  // formatGlobPage (` Full sorted result stored at`) and formatGrepOutput (`(Full grep result stored at`).
  // One stored result.
  {
    pattern: /(?: Full (?:formatted|sorted)|\(Full grep) result stored at: ([^\r\n]+\.txt)\. [^\r\n]*\)$/gu,
    listed: captured => [captured],
    layout: STORED_RESULT_FILE,
  },
]

/** Read each native pointer of one text block, in the order that the native text states them. */
function textPaths(text: string): readonly string[] {
  const found = NOTICES.flatMap(notice => [...text.matchAll(notice.pattern)].flatMap(match =>
    // Every pattern holds capture group 1, so `?? ''` is the type-level guard alone.
    notice.listed(match[1] ?? '')
      .filter(path => isFilesystemPath(path) && notice.layout.test(path))
      .map(path => ({ offset: match.index, path }))))
  // The sort is stable, so the entries of one dropped-output list keep the order of the list.
  return found.sort((a, b) => a.offset - b.offset).map(({ path }) => path)
}

/** Read the native stream, dropped-output, and stored-result pointers without opening any file. */
export function deepseekHarnessOutputFilePaths(input: RowExtractionInput, call: ToolCall): readonly string[] {
  const data = deepseekHarnessEventData(input.resolved.parentObject, DEEPSEEK_HARNESS_EVENT.ToolResult)
  const message = pickObject(data, 'message')
  if (input.span.role !== 'result' || message?.toolCallId !== call.id || !Array.isArray(message.content))
    return []
  const paths: string[] = []
  for (const block of message.content) {
    if (!isObject(block) || block.type !== 'text' || typeof block.text !== 'string')
      continue
    paths.push(...textPaths(block.text))
  }
  return [...new Set(paths)]
}
