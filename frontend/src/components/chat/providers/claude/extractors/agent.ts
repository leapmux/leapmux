import { asContentArray, joinContentParagraphs } from '~/lib/contentBlocks'
import { isObject, pickString, stringArray } from '~/lib/jsonPick'

/**
 * The three shapes an `Agent` tool_result carries, flattened into one.
 *
 * The CLI returns a union (2.1.233), and the variants differ in what they can
 * possibly hold, which is why the view branches on `status` rather than on
 * whether a field happens to be filled:
 *
 *   - `completed` — a SYNCHRONOUS run. Carries the agent's own report in
 *     `content[]`, plus usage and tool stats. Carries NO `description` and no
 *     `outputFile`.
 *   - `async_launched` — a backgrounded run. Carries `description` (the task
 *     title the model wrote), `outputFile`, and the `prompt` it was given, but
 *     no report: it produced none yet.
 *   - `remote_launched` — a cloud run. Like the async one, but identified by
 *     `taskId` + `sessionUrl` instead of `agentId`.
 */
export interface ClaudeAgentResult {
  status: string
  /** The model-supplied task title. A launch has one; a finished sync run does not. */
  description: string
  agentId: string
  taskId: string
  sessionUrl: string
  resolvedModel: string
  /** Every distinct model used before backgrounding. Longer than 1 means a mid-run swap. */
  modelsUsed: string[]
  outputFile: string
  prompt: string
  worktreePath: string
  worktreeBranch: string
  /** The agent's own report. Only a finished sync run has one. */
  content: string
}

/**
 * Whether this result announces a launch rather than reporting a finished run.
 *
 * The status allowlist is the primary signal, and `outputFile` widens it: a
 * finished synchronous run carries none (see the variants above), so a result
 * with no report but an output file is a launch whatever the CLI calls it. The
 * status list alone would let a launch status added tomorrow fall through to the
 * finished path, where the body becomes the harness instructions -- the bug this
 * card exists to avoid.
 */
export function claudeAgentResultIsLaunch(source: ClaudeAgentResult): boolean {
  return source.status === 'async_launched'
    || source.status === 'remote_launched'
    || (!source.content && source.outputFile !== '')
}

/**
 * Flatten an `Agent` tool_result into {@link ClaudeAgentResult}.
 *
 * `resultContent` backs the report ONLY for a non-launch. For a launch it is the
 * CLI's instructions to the calling model -- "never quote or paste any part of
 * it", the id it must not mention, the warning against reading the output file.
 * None of that is about the subagent that the user reads, so treating it as
 * the card's body buried the one thing that is: what the agent was asked to do.
 *
 * Returns null when the payload is not an object, so the dispatch entry can fall
 * through to the catch-all.
 */
export function claudeAgentFromToolResult(
  toolUseResult: Record<string, unknown> | undefined,
  resultContent: string,
): ClaudeAgentResult | null {
  if (!isObject(toolUseResult))
    return null
  const status = pickString(toolUseResult, 'status', 'completed')
  const source: ClaudeAgentResult = {
    status,
    description: pickString(toolUseResult, 'description'),
    agentId: pickString(toolUseResult, 'agentId'),
    taskId: pickString(toolUseResult, 'taskId'),
    sessionUrl: pickString(toolUseResult, 'sessionUrl'),
    resolvedModel: pickString(toolUseResult, 'resolvedModel'),
    modelsUsed: stringArray(toolUseResult.modelsUsed),
    outputFile: pickString(toolUseResult, 'outputFile'),
    prompt: pickString(toolUseResult, 'prompt'),
    worktreePath: pickString(toolUseResult, 'worktreePath'),
    worktreeBranch: pickString(toolUseResult, 'worktreeBranch'),
    content: agentReportText(toolUseResult),
  }
  // The block text backs the report only for a NON-launch. For a launch it is
  // the CLI's instructions to the calling model, which is the one thing on the
  // row that is not about the user's subagent.
  if (!source.content && !claudeAgentResultIsLaunch(source))
    source.content = resultContent
  return source
}

/**
 * Join the blocks of a finished run's `content[]` into the report.
 *
 * `joinContentParagraphs` rather than a text-only filter, because its default
 * `formatOther` is `markdownImageFormatter`: a subagent that returns a
 * screenshot or a diagram carries image blocks, and the card renders markdown,
 * so they survive as `![image](...)`. Dropping every non-text block lost that
 * picture with nothing to replace it.
 */
function agentReportText(toolUseResult: Record<string, unknown>): string {
  return joinContentParagraphs(asContentArray(toolUseResult.content), { text: 'text' }).trim()
}

/**
 * What the card's collapsible body shows: the agent's report when it has one,
 * and otherwise the prompt it was launched with. A launch has no report, so the
 * prompt is the only thing it can say about the work now in flight.
 */
export function claudeAgentResultBody(source: ClaudeAgentResult): string {
  return source.content || source.prompt
}
