/**
 * Claude's failure ladder, in the one place every per-kind builder reads it.
 *
 * A LEAF module, for the reason `toolNames.ts` gives: it imports the row TYPE alone,
 * so nothing here reaches `toolKinds.ts` and its icons. `lucide-solid` refuses to load
 * outside a browser, and five extractor suites run in the `node` environment. The
 * ladder cannot live in `toolCommon.ts` beside `ClaudeToolRow` for that reason: a
 * runtime import of that module from any extractor fails all five suites at once, with
 * "Client-only API called on the server side" and no mention of the module that
 * reached the icons.
 */

import type { ToolFailureResult } from '../../../model/toolCall'
import type { ClaudeToolRow } from './toolCommon'
import { failedResult } from '../../../model/toolCall'

/**
 * The result a FAILED Claude call states: its error text alone, and no payload.
 *
 * Claude reports a failure with `is_error` on the `tool_result` block, and a failed
 * call carries NO `tool_use_result` beside it -- so the reason is the only thing the
 * row holds. Each per-kind builder asks this BEFORE it reads a payload, because the
 * kind's parser treats the error SENTENCE as the kind's own data otherwise: a failed
 * search states its reason, not a match, and a parser that never asked read
 * "File does not exist." as a file the search found.
 *
 * Undefined for a row that did not fail and for one that has not answered, so one
 * guard covers a whole builder:
 *
 *     const failure = claudeToolFailureResult(result)
 *     if (failure)
 *       return { kind: 'grep', request, result: failure }
 *
 * A kind whose failure states MORE than text does not call this, and says so where it
 * branches: `mcp` keeps the pictures a failed call returned, `agent` words the run it
 * ended, and `switch_mode` reads a plan the reader sent back as `declined`. `trigger`
 * asks one rung LOWER, because an endpoint that answered outside 2xx still answered.
 *
 * The BRAND is load-bearing although `ToolFailureResult` and `UnparsedToolResult` draw the same
 * pixels. `invariantViolations` reads it: I3 requires a failed, cancelled or declined
 * status under a `ToolFailureResult`, and I4 requires a completed one under an
 * `UnparsedToolResult`. So a failed call that answered `unparsedResult` claimed that it
 * completed.
 */
export function claudeToolFailureResult(result: ClaudeToolRow | undefined): ToolFailureResult | undefined {
  return result?.isError === true ? failedResult(result.resultContent) : undefined
}
