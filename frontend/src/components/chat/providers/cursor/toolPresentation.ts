import type { ACPToolAdapter } from '../acp/toolPresentation'
import type { ToolBodySource, ToolPresentation } from '~/components/chat/results/toolPresentation'
import { CURSOR_TOOL } from '~/generated/contracts/cursor-protocol'
import { prettifyArgsJson } from '~/lib/jsonFormat'
import { isObject, pickBoolean, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { commandIsError } from '../../results/commandResult'
import { mcpToolCallDisplayName } from '../../results/mcpToolCall'
import { readFileSourceFromContent } from '../../results/readFileResult'
import { acpToolFinished } from '../acp/toolPresentation'
import { cursorAgentPresentation } from './agentResult'
import { cursorStoredToolPresentation } from './storedTool'

/**
 * The tool Cursor writes a plan with.
 *
 * Not in `contracts/cursor-protocol.json`, which holds the identifiers BOTH programs
 * read. The worker knows a plan by its JSON-RPC method (`cursor/create_plan`) and never
 * reads this tool name, so the contract rule keeps it on the one side that does.
 */
const CURSOR_TOOL_CREATE_PLAN = 'createPlan'

/**
 * The body a plan row draws.
 *
 * A plan occupies two rows: the call that proposes it and the update that reports where
 * it was saved. It must appear exactly once across BOTH, and which row draws a body
 * changes as the call progresses. Until a completing row exists the proposing row draws,
 * so the reader can read the plan while the approval is still open. Once one exists, it
 * draws instead.
 *
 * Every other row states no arguments: the whole plan as JSON is what a reader saw there.
 */
function planBody(model: ToolPresentation, plan: string, proposing: boolean, hasResult: boolean): ToolBodySource {
  if (plan && (!proposing || !hasResult))
    return { type: 'markdown', text: plan }
  return model.body.type === 'mcp'
    ? { type: 'mcp', source: { ...model.body.source, argsJson: '' } }
    : model.body
}

/**
 * The protocol-level error, for a row that states nothing else.
 *
 * Cursor reports a failed call in `rawOutput.error` and writes no output beside it.
 * A body built from a saved result can be empty for exactly that reason -- a failed
 * `mcp_*` call whose saved result is empty is the reachable case -- so EVERY return
 * from the adapter passes through here.
 */
function withProtocolError(model: ToolPresentation, raw: Record<string, unknown> | null): ToolPresentation {
  const error = pickString(raw, 'error')
  return error && !model.output ? { ...model, output: error } : model
}

/**
 * The identity of one Cursor tool call: the fields that say WHAT the call is.
 *
 * This is the one place below the early returns that writes `kind`, so every body
 * branch that follows only READS it. Cursor states a search's shape in two places --
 * the title the runtime rendered, and the counters the result carries -- and the two
 * disagree for a file search that also reports matches. A body branch that decided
 * the kind a second time lets the weaker statement overwrite the stronger one.
 *
 * Precedence for a search, strongest first:
 *  1. The rendered TITLE. The runtime writes `Find` for a file-name search and
 *     `grep` for a content search.
 *  2. The result COUNTERS. `totalFiles` rides a file-name search, and a content
 *     search reports its match total instead.
 *
 * `rawInput._toolName` is the runtime's own identifier, and it would rank above both.
 * A search row carries none. Cursor writes `_toolName` for five tools only -- `task`,
 * `createPlan`, `askQuestion`, `updateTodos` and `generateImage`. Its file-name search
 * sends `{pattern}` and its content search sends `{pattern, path}`. Four released
 * versions, from 2026.07 to 2026.09, agree. So the title is the only identity a search
 * row carries, and it must lead.
 *
 * The title match must therefore accept every title the runtime composes. Cursor builds
 * the content-search title from the arguments: `grep`, then one optional flag for each
 * argument (`-i`, `-n`, `-A N`, `-l`, `--include="..."`, and more), then the quoted
 * pattern last. A flag moves the pattern away from the front, so the `grep ` prefix is
 * the one part that stays constant. The file-name search writes `Find`, then an optional
 * path and an optional pattern, each one inside backticks.
 */
function cursorToolIdentity(
  tool: Record<string, unknown>,
  model: ToolPresentation,
  input: Record<string, unknown>,
  raw: Record<string, unknown> | null,
): ToolPresentation {
  if (model.kind === 'execute')
    return { ...model, title: pickString(input, 'description') }
  if (model.kind !== 'search')
    return model
  const title = pickString(tool, 'title')
  if (title === 'Find' || title.startsWith('Find `'))
    return { ...model, kind: 'glob' }
  if (title === 'grep' || title.startsWith('grep '))
    return { ...model, kind: 'grep' }
  if (tool.status !== 'completed' || !raw)
    return model
  const totalFiles = pickNumber(raw, 'totalFiles', undefined)
  const matches = pickNumber(raw, 'totalMatches', undefined) ?? pickNumber(raw, 'resultCount', undefined)
  if (totalFiles === undefined && matches === undefined)
    return model
  return { ...model, kind: totalFiles !== undefined ? 'glob' : 'grep' }
}

/** Cursor returns file and shell output in rawOutput without ACP content blocks. */
export const cursorToolAdapter: ACPToolAdapter = (tool, presentation, supplemental, row) => {
  const raw = pickObject(tool, 'rawOutput')
  const restored = cursorStoredToolPresentation(tool, presentation, supplemental)
  if (restored && restored.body !== presentation.body)
    return withProtocolError(restored, raw)
  const input = restored?.input ?? presentation.input
  let model = restored ?? presentation
  // A plan arrives in two halves: the stored call carries the tool name alone, and the
  // plan body reaches the transcript as supplemental content the worker recovered from
  // the approval request. A full plan belongs in the transcript, so read both halves.
  const planInput = { ...pickObject(supplemental, 'rawInput'), ...input }
  if (planInput._toolName === CURSOR_TOOL_CREATE_PLAN) {
    return {
      ...model,
      // No row states the arguments. The whole plan as JSON is what a reader saw where
      // the plan itself belonged.
      input: {},
      title: pickString(planInput, 'name') || model.title,
      label: 'Plan',
      body: planBody(model, pickString(planInput, 'plan'), row?.role !== 'result', row?.hasResult ?? false),
    }
  }
  if (input._toolName === CURSOR_TOOL.Task)
    return cursorAgentPresentation(tool, model)
  if (model.body.type === 'mcp' && pickString(input, 'toolName') && pickString(input, 'providerIdentifier')) {
    const source = { ...model.body.source, server: pickString(input, 'providerIdentifier'), tool: pickString(input, 'toolName'), argsJson: prettifyArgsJson(input.args) }
    model = { ...model, input: pickObject(input, 'args') ?? {}, title: mcpToolCallDisplayName(source), label: 'MCP Tool Call', body: { type: 'mcp', source } }
  }
  model = cursorToolIdentity(tool, model, input, raw)
  if (presentation.body.type === 'diff') {
    model = {
      ...model,
      body: {
        type: 'diff',
        sources: presentation.body.sources.map((source) => {
          // Cursor's fallback diff parser removes one prefix character from file headers.
          const header = `++ b/${source.filePath}\n`
          return source.oldStr === '-- /dev/null' && source.newStr.startsWith(header)
            ? { ...source, oldStr: '', newStr: source.newStr.slice(header.length) }
            : source
        }),
      },
    }
  }
  if (model.kind === 'execute' && acpToolFinished(tool) && raw) {
    const stdout = pickString(raw, 'stdout')
    const stderr = pickString(raw, 'stderr')
    const output = [stdout, stderr].filter(Boolean).join(stdout.endsWith('\n') ? '' : '\n') || model.output
    const exitCode = pickNumber(raw, 'exitCode', undefined)
    return {
      ...model,
      output,
      body: { type: 'command', source: { output, stderr, exitCode, isError: commandIsError(pickString(tool, 'status'), exitCode), interrupted: tool.status === 'cancelled' } },
    }
  }
  if (model.kind === 'read' && tool.status === 'completed' && typeof raw?.content === 'string') {
    const location = Array.isArray(tool.locations) ? tool.locations.find(isObject) : undefined
    const reportedLine = pickNumber(location, 'line', undefined)
    const startLine = reportedLine !== undefined && Number.isSafeInteger(reportedLine) && reportedLine > 0 ? reportedLine : 1
    return {
      ...model,
      input,
      output: raw.content,
      body: { type: 'read', source: readFileSourceFromContent({ filePath: pickString(input, 'path'), content: raw.content, startLine }) },
    }
  }
  if (['search', 'glob', 'grep'].includes(model.kind) && tool.status === 'completed' && raw) {
    const totalFiles = pickNumber(raw, 'totalFiles', undefined)
    const matches = pickNumber(raw, 'totalMatches', undefined) ?? pickNumber(raw, 'resultCount', undefined)
    if (totalFiles !== undefined || matches !== undefined) {
      const glob = model.kind === 'glob'
      return {
        ...model,
        body: {
          type: 'search',
          source: {
            variant: glob ? 'glob' : 'search',
            filenames: [],
            content: model.output,
            numFiles: totalFiles ?? 0,
            numLines: 0,
            matches,
            truncated: pickBoolean(raw, 'truncated') ?? false,
            fallbackContent: model.output,
          },
        },
      }
    }
  }
  return withProtocolError(model, raw)
}
