import type { CommandExit, CommandResult } from '../../../model/commandResult'
import type { ToolCallSpec } from '../../../model/toolCall'
import type { ToolRequestByKind } from '../../../model/tools'
import type { QuestionAnswer } from '../../../model/tools/question'
import type { ACPToolCallAdapter, ACPToolFacts } from '../../acp/extractors/toolCall'
import { rawTodosToItems } from '~/components/chat/normalizers/todo'
import { ACP_SUPPLEMENT_REQUEST } from '~/generated/contracts/acp-protocol'
import { QWEN_META, QWEN_TOOL } from '~/generated/contracts/qwen-protocol'
import { isObject, pickBoolean, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { questionsFromWire } from '../../../controls/types'
import { mcpToolCallRequest, parseMcpContentItem, parseMcpToolName } from '../../../model/mcpToolCall'
import { failedResult, isUnparsedToolResult, proseResult } from '../../../model/toolCall'
import { acpRemapFacts, acpSpecFor } from '../../acp/extractors/toolCall'
import { isQwenTool, QWEN_TOOL_KINDS, QWEN_TOOL_NAME } from '../toolKinds'
import { qwenAgentRequest, qwenAgentRun, qwenWorkflowRequest, qwenWorkflowRun } from './agent'
import { qwenGlobResult, qwenGrepResult, qwenListResult, qwenReadResult } from './results'

/** The `rawOutput` type of a finished shell command. */
const QWEN_SHELL_RESULT = 'shell_result'

/** The `rawOutput` type of an answered question dialog. */
const QWEN_QUESTION_ANSWERS = 'ask_user_question_answers'

/** The tool one call ran, from Qwen's `_meta.toolName`. */
export function qwenToolName(tool: Record<string, unknown>): string {
  return pickString(pickObject(tool, '_meta'), QWEN_META.ToolName)
}

/**
 * The shell result one finished command states, or undefined when it states none.
 *
 * The call's content is the sentence block Qwen gives the MODEL (`Command: ...`,
 * `Exit Code: 0`), which repeats the command and its status as prose. The record
 * states the output and the exit apart, which is what the command body draws.
 */
function qwenShellCommand(tool: Record<string, unknown>): CommandResult | undefined {
  const raw = pickObject(tool, 'rawOutput')
  if (!raw || pickString(raw, 'type') !== QWEN_SHELL_RESULT)
    return undefined
  const output = [pickString(raw, 'output'), pickString(raw, 'error')].filter(Boolean).join('\n')
  const signal = typeof raw.signal === 'string' || typeof raw.signal === 'number' ? String(raw.signal) : ''
  const code = pickNumber(raw, 'exitCode')
  const exit: CommandExit = code !== null ? { exitCode: code } : signal ? { signal } : {}
  return { output, ...exit, truncated: pickBoolean(raw, 'truncated') === true }
}

/** The answers one finished question dialog states, one for each question it asked. */
function qwenQuestionAnswers(tool: Record<string, unknown>): QuestionAnswer[] | undefined {
  const raw = pickObject(tool, 'rawOutput')
  if (!raw || pickString(raw, 'type') !== QWEN_QUESTION_ANSWERS || !Array.isArray(raw.answers))
    return undefined
  return raw.answers.filter(isObject).map(entry => ({ header: pickString(entry, 'question'), answer: pickString(entry, 'answer', undefined) ?? null }))
}

/** The request of one cron call. The tool name states the action. */
function qwenTriggerRequest(name: string, input: Record<string, unknown>): ToolRequestByKind['trigger'] {
  const action = name === QWEN_TOOL_NAME.CronCreate || name === QWEN_TOOL_NAME.LoopWakeup
    ? 'create'
    : name === QWEN_TOOL_NAME.CronDelete ? 'delete' : 'list'
  const triggerId = pickString(input, 'id')
  const delay = pickNumber(input, 'delaySeconds')
  const schedule = pickString(input, 'cron') || (delay !== null ? `in ${delay}s` : '')
  const prompt = pickString(input, 'prompt')
  return { action, ...(triggerId ? { triggerId } : {}), ...(prompt ? { name: prompt } : {}), ...(schedule ? { schedule } : {}) }
}

/**
 * One call of a Model Context Protocol tool, `mcp__server__tool`.
 *
 * A server call that FAILED gave no answer, so its reason is the body. A call the
 * reader stopped keeps the blocks that arrived.
 */
function qwenMcpSpec(facts: ACPToolFacts, name: string, server: string, tool: string): ToolCallSpec {
  return {
    ...mcpToolCallRequest(server, tool, facts.args),
    name,
    ...(facts.finished ? { result: facts.status === 'failed' ? failedResult(facts.text) : { content: facts.content.map(parseMcpContentItem) } } : {}),
  }
}

/**
 * The arguments of one call, in the keys the shared readers look for.
 *
 * `notebook_edit` states its file as `notebook_path` and the new cell as
 * `new_source`, which no shared reader knows. The row draws the edit as a change to
 * that file.
 */
function qwenArgs(name: string, input: Record<string, unknown>): Record<string, unknown> {
  if (name !== QWEN_TOOL_NAME.NotebookEdit)
    return input
  const path = pickString(input, 'notebook_path')
  const source = pickString(input, 'new_source')
  return { ...input, ...(path ? { file_path: path } : {}), ...(source ? { new_string: source } : {}) }
}

/** Qwen identifies each call in `_meta.toolName`, and its titles are prose. */
export const qwenToolCallAdapter: ACPToolCallAdapter = (facts, base) => {
  const name = qwenToolName(facts.tool)
  if (!name)
    return base()
  const input = facts.args
  const toolCallId = pickString(facts.tool, 'toolCallId')

  if (name === QWEN_TOOL.Agent) {
    const request = qwenAgentRequest(input, toolCallId)
    // `facts.finished`, never the frame's own status: a retained row of a turn that
    // ended keeps its report.
    return { kind: 'agent', name, request, ...(facts.finished ? { result: { agents: [qwenAgentRun(facts, request)] } } : {}) }
  }
  if (name === QWEN_TOOL.Workflow) {
    const request = qwenWorkflowRequest(input)
    return { kind: 'agent', name, request, ...(facts.finished ? { result: { agents: [qwenWorkflowRun(facts, request)] } } : {}) }
  }
  if (name === QWEN_TOOL.TodoWrite && Array.isArray(input.todos)) {
    const items = rawTodosToItems(input.todos)
    return {
      kind: 'todo',
      name,
      // No title: `todoRenderer` composes the words from the list. An explicit
      // undefined, because the ACP wrapper spreads this over the frame's own title.
      title: undefined,
      request: { items },
      ...(facts.finished ? { result: facts.status === 'failed' ? failedResult(facts.text) : { items } } : {}),
    }
  }
  if (name === QWEN_TOOL.AskUserQuestion) {
    const questions = questionsFromWire(input.questions).map(({ question, options, header }) => ({ question, options, ...(header !== undefined ? { header } : {}) }))
    const asked = { kind: 'question' as const, name, request: { questions }, title: questions[0]?.header || questions[0]?.question || 'Question' }
    if (!facts.finished)
      return asked
    if (facts.status === 'failed')
      return { ...asked, result: failedResult(facts.text) }
    const answers = qwenQuestionAnswers(facts.tool)
    if (answers)
      return { ...asked, result: { answers } }
    return { ...asked, ...(facts.text ? { result: { answers: [{ header: asked.title, answer: facts.text }] } } : {}) }
  }
  if (name === QWEN_TOOL.RunShellCommand || name === QWEN_TOOL_NAME.Monitor) {
    const remapFacts = acpRemapFacts(facts, { tool: { ...facts.tool, [ACP_SUPPLEMENT_REQUEST.RawInput]: input }, kind: 'execute' })
    const spec = acpSpecFor(remapFacts, 'execute')
    const shell = facts.finished && facts.status !== 'failed' ? qwenShellCommand(facts.tool) : undefined
    return shell && spec.result !== undefined && 'commands' in spec.result
      ? { ...spec, name, result: { ...spec.result, commands: [shell] } }
      : { ...spec, name }
  }
  if (!isQwenTool(name)) {
    const mcp = parseMcpToolName(name)
    if (mcp)
      return qwenMcpSpec(facts, name, mcp.server, mcp.tool)
    return { ...base(), name }
  }

  const kind = QWEN_TOOL_KINDS[name]
  // RE-DERIVED, not cloned: every fact the shared build reads from the kind or from
  // the arguments is computed again under the kind the table states.
  const remapFacts = acpRemapFacts(facts, { tool: { ...facts.tool, [ACP_SUPPLEMENT_REQUEST.RawInput]: qwenArgs(name, input) }, kind })
  // The kinds whose declared result is the words the tool wrote. The shared ladder
  // already placed the unfinished and the failed call, so only its "could not read"
  // answer is replaced.
  const answered = facts.finished && facts.status !== 'failed'
  if (name === QWEN_TOOL_NAME.CronCreate || name === QWEN_TOOL_NAME.CronDelete || name === QWEN_TOOL_NAME.CronList || name === QWEN_TOOL_NAME.LoopWakeup) {
    const spec = acpSpecFor(remapFacts, QWEN_TOOL_KINDS[name])
    const request = qwenTriggerRequest(name, input)
    return spec.result !== undefined && isUnparsedToolResult(spec.result)
      ? { ...spec, name, request, result: proseResult(facts.text) }
      : { ...spec, name, request }
  }
  if (name === QWEN_TOOL_NAME.Skill) {
    const spec = acpSpecFor(remapFacts, QWEN_TOOL_KINDS[name])
    return spec.result !== undefined && isUnparsedToolResult(spec.result) ? { ...spec, name, result: proseResult(facts.text, 'markdown') } : { ...spec, name }
  }
  if (name === QWEN_TOOL_NAME.WebSearch) {
    const spec = acpSpecFor(remapFacts, QWEN_TOOL_KINDS[name])
    return spec.result !== undefined && isUnparsedToolResult(spec.result) ? { ...spec, name, result: { links: [], summary: facts.text } } : { ...spec, name }
  }
  if (name === QWEN_TOOL_NAME.ReadFile || name === QWEN_TOOL_NAME.ZoomImage) {
    const spec = acpSpecFor(remapFacts, QWEN_TOOL_KINDS[name])
    if (!answered)
      return { ...spec, name }
    // A picture is the answer of a zoomed image and of an image file, and it rides the
    // call's images; the result states the words beside it, if any. An empty text is
    // an empty file, which reads as zero lines.
    if (name === QWEN_TOOL_NAME.ZoomImage || facts.images.length > 0)
      return { ...spec, name, result: { lines: null, fallbackContent: facts.text } }
    return { ...spec, name, result: qwenReadResult(facts.text, pickNumber(input, 'offset')) }
  }
  if (name === QWEN_TOOL_NAME.GrepSearch || name === QWEN_TOOL_NAME.Glob) {
    const spec = acpSpecFor(remapFacts, QWEN_TOOL_KINDS[name])
    const found = answered ? (name === QWEN_TOOL_NAME.Glob ? qwenGlobResult(facts.text) : qwenGrepResult(facts.text)) : null
    return found ? { ...spec, name, result: found } : { ...spec, name }
  }
  if (name === QWEN_TOOL_NAME.ListDirectory) {
    const spec = acpSpecFor(remapFacts, QWEN_TOOL_KINDS[name])
    const listing = answered ? qwenListResult(facts.text) : null
    return listing ? { ...spec, name, result: listing } : { ...spec, name }
  }
  if (name === QWEN_TOOL_NAME.TaskStop) {
    const spec = acpSpecFor(remapFacts, QWEN_TOOL_KINDS[name])
    const request: ToolRequestByKind['task'] = { ...spec.request, action: 'stop' }
    if (!facts.finished)
      return { ...spec, name, request }
    if (facts.status === 'failed')
      return { ...spec, name, request, result: failedResult(facts.text) }
    return { ...spec, name, request, result: { outcome: 'stopped', output: facts.text } }
  }
  if (name === QWEN_TOOL_NAME.EnterPlanMode || name === QWEN_TOOL.ExitPlanMode)
    return { ...acpSpecFor(remapFacts, QWEN_TOOL_KINDS[name]), name, title: name === QWEN_TOOL.ExitPlanMode ? 'Exit plan mode' : 'Enter plan mode' }
  if (name === QWEN_TOOL_NAME.EnterWorktree || name === QWEN_TOOL_NAME.ExitWorktree) {
    const spec = acpSpecFor(remapFacts, QWEN_TOOL_KINDS[name])
    const target = pickString(input, 'name')
    return { ...spec, name, request: { ...spec.request, mode: name === QWEN_TOOL_NAME.EnterWorktree ? 'worktree' : 'leave worktree', ...(target ? { target } : {}) } }
  }
  return { ...acpSpecFor(remapFacts, kind), name }
}
