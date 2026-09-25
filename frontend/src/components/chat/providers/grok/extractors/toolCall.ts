import type { QuestionPrompt } from '../../../model/question'
import type { ToolCallSpec } from '../../../model/toolCall'
import type { ToolRequestByKind } from '../../../model/tools'
import type { ACPToolCallAdapter, ACPToolFacts } from '../../acp/extractors/toolCall'
import { rawTodosToItems } from '~/components/chat/normalizers/todo'
import { ACP_SUPPLEMENT_REQUEST } from '~/generated/contracts/acp-protocol'
import { GROK_META, GROK_TOOL } from '~/generated/contracts/grok-protocol'
import { isObject, pickFirstString, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { questionsFromWire } from '../../../controls/types'
import { withCommandExit } from '../../../model/commandResult'
import { mcpToolCallRequest, parseMcpContentItem, splitPrefixedPair } from '../../../model/mcpToolCall'
import { failedResult, isUnparsedToolResult, proseResult } from '../../../model/toolCall'
import { acpRemapFacts, acpSpecFor } from '../../acp/extractors/toolCall'
import { TOOL_FILE_PATH_KEYS } from '../../toolInputKeys'
import { GROK_TOOL_KINDS, GROK_TOOL_NAME, isGrokTool } from '../toolKinds'
import { grokAgentRequest, grokAgentRun, grokWorkflowRequest, grokWorkflowRun } from './agent'
import { grokCommandExit, grokGrepResult, grokListResult, grokRawOutput } from './results'

/**
 * The tag Grok adds to the arguments it PRESENTS in its first `tool_call_update`.
 *
 * It gives the Rust variant of the normalized input (`Bash`, `ReadFile`), and it is
 * no argument of the call, so the row never draws it.
 */
const GROK_INPUT_VARIANT = 'variant'

/** The namespace Grok states for a tool that a Model Context Protocol server supplies. */
const GROK_MCP_NAMESPACE = 'mcp'

/** The separator between the server and the tool of one MCP tool name: `server__tool`. */
const GROK_MCP_SEPARATOR = '__'

/** The names outside the kind table that the adapter still reads by name. */
const GROK_BRANCH_TOOLS: ReadonlySet<string> = new Set([GROK_TOOL.SpawnSubagent, GROK_TOOL_NAME.TodoWrite, GROK_TOOL_NAME.UseTool, GROK_TOOL_NAME.Workflow])

/** Grok's own identity of one call, `_meta["x.ai/tool"]`, when the frame carries it. */
function grokToolMeta(tool: Record<string, unknown>): Record<string, unknown> | undefined {
  return pickObject(pickObject(tool, '_meta'), GROK_META.Tool) ?? undefined
}

/**
 * The tool one call ran.
 *
 * `_meta["x.ai/tool"].name` is Grok's stable identity. The first `tool_call` also
 * states the name as its title, before the presentation update replaces the title
 * with prose, so a title that IS a known name stands in when the identity is absent.
 */
export function grokToolName(tool: Record<string, unknown>): string {
  const name = pickString(grokToolMeta(tool), 'name')
  if (name)
    return name
  const title = pickString(tool, 'title')
  return isGrokTool(title) || GROK_BRANCH_TOOLS.has(title) ? title : ''
}

/**
 * The arguments of one call, in the keys the shared readers look for.
 *
 * Grok states them three ways: the model's own arguments on the first frame, the
 * normalized input with its variant tag on the presentation update, and a CANONICAL
 * input (`path`, `command`, `pattern`) inside its identity. The call's own keys win,
 * the canonical keys fill what they omit, and the tag goes. `read_file` and `list_dir`
 * state their target under a key no shared reader knows, so it becomes the `path`.
 */
function grokArgs(facts: ACPToolFacts): Record<string, unknown> {
  const { [GROK_INPUT_VARIANT]: _variant, ...own } = facts.args
  const canonical = pickObject(grokToolMeta(facts.tool), 'input') ?? {}
  const args: Record<string, unknown> = { ...canonical, ...own }
  const target = pickString(own, 'target_file') || pickString(own, 'target_directory')
  if (target && !pickFirstString(args, TOOL_FILE_PATH_KEYS))
    args.path = target
  return args
}

/** The questions of one `ask_user_question`, with Grok's snake-case flag folded. */
function grokQuestionPrompts(input: Record<string, unknown>): QuestionPrompt[] {
  const raw = Array.isArray(input.questions)
    ? input.questions.map(entry => isObject(entry) && entry.multiSelect === undefined && entry.multi_select !== undefined ? { ...entry, multiSelect: entry.multi_select } : entry)
    : input.questions
  return questionsFromWire(raw).map(({ question, options, header }) => ({ question, options, ...(header !== undefined ? { header } : {}) }))
}

/**
 * The ids of the background work one task call is about.
 *
 * `kill_command_or_subagent` states one id, and `get_command_or_subagent_output`
 * states a list. The row states them together.
 */
function grokTaskIds(input: Record<string, unknown>): string {
  const ids = Array.isArray(input.task_ids) ? input.task_ids.filter((id): id is string => typeof id === 'string' && id !== '') : []
  return ids.length > 0 ? ids.join(', ') : pickString(input, 'task_id')
}

/** The request of one scheduler call. The tool name states the action. */
function grokTriggerRequest(name: string, input: Record<string, unknown>): ToolRequestByKind['trigger'] {
  const action = name === GROK_TOOL_NAME.SchedulerCreate ? 'create' : name === GROK_TOOL_NAME.SchedulerDelete ? 'delete' : 'list'
  const triggerId = pickString(input, 'task_id') || pickString(input, 'id')
  const schedule = pickString(input, 'interval')
  const prompt = pickString(input, 'prompt')
  return { action, ...(triggerId ? { triggerId } : {}), ...(prompt ? { name: prompt } : {}), ...(schedule ? { schedule } : {}) }
}

/**
 * One call of a Model Context Protocol tool: a direct call, or one that `use_tool`
 * wraps.
 *
 * A server call that FAILED gave no answer, so its reason is the body. A call the
 * reader stopped keeps the blocks that arrived.
 */
function grokMcpSpec(facts: ACPToolFacts, name: string, server: string, tool: string, input: Record<string, unknown>): ToolCallSpec {
  return {
    ...mcpToolCallRequest(server, tool, input),
    name,
    ...(facts.finished ? { result: facts.status === 'failed' ? failedResult(facts.text) : { content: facts.content.map(parseMcpContentItem) } } : {}),
  }
}

/** Grok identifies each call in its `_meta`, and its titles are prose. */
export const grokToolCallAdapter: ACPToolCallAdapter = (facts, base) => {
  const name = grokToolName(facts.tool)
  if (!name)
    return base()
  const input = grokArgs(facts)
  const toolCallId = pickString(facts.tool, 'toolCallId')
  const title = pickString(facts.tool, 'title')

  if (name === GROK_TOOL.SpawnSubagent) {
    const request = grokAgentRequest(input, title && title !== name ? title : 'Subagent', toolCallId)
    // `facts.finished`, never the frame's own status: a retained row of a turn that
    // ended keeps its report.
    return { kind: 'agent', name, request, ...(facts.finished ? { result: { agents: [grokAgentRun(facts, request)] } } : {}) }
  }
  if (name === GROK_TOOL_NAME.Workflow) {
    const request = grokWorkflowRequest(input, facts)
    return { kind: 'agent', name, request, ...(facts.finished ? { result: { agents: [grokWorkflowRun(facts, request, input)] } } : {}) }
  }
  // Grok MERGES the list into the one it holds when `merge` is set, so the arguments
  // can state a part of the list. The request is what the call asked for, and the
  // result is the whole list Grok holds after it.
  if (name === GROK_TOOL_NAME.TodoWrite && Array.isArray(input.todos)) {
    const updated = pickObject(grokRawOutput(facts.tool, 'Todo'), 'TodosUpdated')
    const asked = rawTodosToItems(input.todos)
    const held = Array.isArray(updated?.todos) ? rawTodosToItems(updated.todos) : asked
    return {
      kind: 'todo',
      name,
      // No title: `todoRenderer` composes the words from the list. An explicit
      // undefined, because the ACP wrapper spreads this over the frame's own title.
      title: undefined,
      request: { items: asked },
      ...(facts.finished ? { result: facts.status === 'failed' ? failedResult(facts.text) : { items: held } } : {}),
    }
  }
  if (name === GROK_TOOL_NAME.UseTool) {
    const wrapped = pickString(input, 'tool_name')
    const pair = splitPrefixedPair(wrapped, '', GROK_MCP_SEPARATOR)
    const args = pickObject(input, 'tool_input') ?? {}
    if (pair)
      return grokMcpSpec(facts, name, pair.server, pair.tool, args)
    if (wrapped)
      return grokMcpSpec(facts, name, '', wrapped, args)
  }
  if (!isGrokTool(name)) {
    const pair = splitPrefixedPair(name, '', GROK_MCP_SEPARATOR)
    if (pair || pickString(grokToolMeta(facts.tool), 'namespace') === GROK_MCP_NAMESPACE)
      return grokMcpSpec(facts, name, pair?.server ?? '', pair?.tool ?? name, input)
    return { ...base(), name }
  }

  const kind = GROK_TOOL_KINDS[name]
  // RE-DERIVED, not cloned: every fact the shared build reads from the kind or from
  // the arguments is computed again under the kind the table states.
  const remapFacts = acpRemapFacts(facts, { tool: { ...facts.tool, [ACP_SUPPLEMENT_REQUEST.RawInput]: input }, kind })

  if (name === GROK_TOOL_NAME.RunTerminalCommand || name === GROK_TOOL_NAME.Monitor) {
    const spec = acpSpecFor(remapFacts, GROK_TOOL_KINDS[name])
    // Grok states the exit code in its own record, where the shared reader does not
    // look. The command body then states how the process ended.
    const exit = grokCommandExit(facts.tool)
    if (!exit || spec.result === undefined || !('commands' in spec.result))
      return { ...spec, name }
    return { ...spec, name, result: { ...spec.result, commands: spec.result.commands.map(command => withCommandExit(command, exit)) } }
  }
  if (name === GROK_TOOL_NAME.ListDir) {
    const spec = acpSpecFor(remapFacts, GROK_TOOL_KINDS[name])
    const listing = facts.finished && facts.status !== 'failed' ? grokListResult(facts.tool) : null
    return listing ? { ...spec, name, result: listing } : { ...spec, name }
  }
  if (name === GROK_TOOL_NAME.Grep) {
    const spec = acpSpecFor(remapFacts, GROK_TOOL_KINDS[name])
    const matches = facts.finished && facts.status !== 'failed' ? grokGrepResult(facts.tool) : null
    return matches ? { ...spec, name, result: matches } : { ...spec, name }
  }
  if (name === GROK_TOOL_NAME.GetOutput || name === GROK_TOOL_NAME.Kill) {
    const taskId = grokTaskIds(input)
    const timeoutMs = pickNumber(input, 'timeout_ms')
    const request: ToolRequestByKind['task'] = {
      action: name === GROK_TOOL_NAME.Kill ? 'stop' : 'output',
      ...(taskId ? { taskId } : {}),
      ...(timeoutMs !== null ? { timeoutMs } : {}),
    }
    if (!facts.finished)
      return { kind: 'task', name, request, title }
    if (facts.status === 'failed')
      return { kind: 'task', name, request, title, result: failedResult(facts.text) }
    return { kind: 'task', name, request, title, result: { outcome: name === GROK_TOOL_NAME.Kill ? 'stopped' : 'completed', output: facts.text } }
  }
  // The scheduler and the web search answer in words, which is the declared result of
  // both kinds. The shared ladder already placed the unfinished and the failed call,
  // so only its "could not read" answer is replaced.
  if (name === GROK_TOOL_NAME.SchedulerCreate || name === GROK_TOOL_NAME.SchedulerDelete || name === GROK_TOOL_NAME.SchedulerList) {
    const spec = acpSpecFor(remapFacts, GROK_TOOL_KINDS[name])
    const request = grokTriggerRequest(name, input)
    return spec.result !== undefined && isUnparsedToolResult(spec.result)
      ? { ...spec, name, request, result: proseResult(facts.text) }
      : { ...spec, name, request }
  }
  if (name === GROK_TOOL_NAME.WebSearch) {
    const spec = acpSpecFor(remapFacts, GROK_TOOL_KINDS[name])
    return spec.result !== undefined && isUnparsedToolResult(spec.result)
      ? { ...spec, name, result: { links: [], summary: facts.text } }
      : { ...spec, name }
  }
  if (name === GROK_TOOL_NAME.AskUserQuestion) {
    const questions = grokQuestionPrompts(input)
    const header = questions[0]?.header || questions[0]?.question || 'Question'
    const asked = { kind: 'question' as const, name, request: { questions }, ...(title ? { title } : {}) }
    if (!facts.finished)
      return asked
    if (facts.status === 'failed')
      return { ...asked, result: failedResult(facts.text) }
    return { ...asked, ...(facts.text ? { result: { answers: [{ header, answer: facts.text }] } } : {}) }
  }
  return { ...acpSpecFor(remapFacts, kind), name }
}
