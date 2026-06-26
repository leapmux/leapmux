import type { HeightInput, RowUiState } from '../../chatHeightEstimator'
import type { MessageCategory } from '../../messageClassification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { countLines, toLineLengths } from '../../chatHeightShared'
import { diffFieldsFromSources } from '../../results/fileEditDiff'
import { extractPiBash } from './extractors/bash'
import { piResolveDiffSources } from './extractors/fileEdit'
import { PI_TOOL } from './protocol'

/**
 * Pi's `Provider.heightMetrics`:
 *
 *  - A Bash `tool_use` row renders its FULL command (PiBashRenderer, alwaysVisible)
 *    as a monospace summary under the header. The generic estimator can't size it:
 *    Pi stores the command under `args.command` (not the `input.command` Claude shape
 *    `extractToolSummary` reads) and classifies Bash lowercase (`'bash'`, not the
 *    `'Bash'` that path matches), so a multi-line command would estimate as a bare
 *    header. Size the full command (matching `extractPiBash`, the exact render path).
 *  - An edit/write `tool_execution_end` (classified `tool_result`) row's diff
 *    geometry, reusing the same `piResolveDiffSources` the toolbar meta uses so the
 *    estimate matches what mounts. A multi-file edit resolves to multiple sources,
 *    which the renderer draws as separate stacked diff blocks; diffFieldsFromSources
 *    sizes each block independently and records the block count so the per-block
 *    container chrome is charged once per file (rather than collapsing every file
 *    into one block, which under-counts chrome and mis-counts cross-file separators).
 *    Pi sources carry no `originalFile`, so only between-hunk separators apply.
 *
 * Read and other Pi tool rows route through the generic text sizing.
 */
export function piHeightMetrics(
  category: MessageCategory,
  parsed: ParsedMessageContent,
  toolUseParsed: ParsedMessageContent | undefined,
  _state: RowUiState,
): Partial<HeightInput> | null {
  if (category.kind === 'tool_use') {
    if (category.toolName !== PI_TOOL.Bash)
      return null
    const command = extractPiBash(category.toolUse)?.command ?? ''
    if (!command)
      return null
    // Per-hard-line lengths so estimateToolUseHeader sums each line's wrap: the
    // PiBashRenderer summary is a pre-wrap monospace block (each line wraps on its
    // own), so the flat total-wrap model under-counts a multi-line command.
    return { textLength: command.length, logicalLineCount: countLines(command), lineLengths: toLineLengths(command) }
  }
  if (category.kind !== 'tool_result')
    return null
  // Pass parsed.parentObject -- the same object identity piToolResultMeta passes
  // to resolvePiResultDiff -- so its WeakMap cache is shared, not re-parsed.
  const sources = piResolveDiffSources(parsed.parentObject, toolUseParsed)
  if (sources.length === 0)
    return null
  return diffFieldsFromSources(sources)
}
