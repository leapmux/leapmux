import type { McpContentItem } from '../../../model/mcpToolCall'
import type { ReadFileResult } from '../../../model/readFileResult'
import type { ToolCallSpec } from '../../../model/toolCall'
import type { ToolRequestByKind } from '../../../model/tools'
import type { ACPToolCallAdapter, ACPToolFacts } from '../../acp/extractors/toolCall'
import { ACP_SUPPLEMENT_REQUEST } from '~/generated/contracts/acp-protocol'
import { KIRO_KIND, KIRO_META, KIRO_TOOL_TITLE } from '~/generated/contracts/kiro-protocol'
import { pickNumber, pickString } from '~/lib/jsonPick'
import { withCommandExit } from '../../../model/commandResult'
import { mcpToolCallRequest, parseMcpContentItem, splitPrefixedPair } from '../../../model/mcpToolCall'
import { failedResult, proseResult } from '../../../model/toolCall'
import { acpRemapFacts, acpResultAvailable, acpSpecFor } from '../../acp/extractors/toolCall'
import { kiroUserInputQuestions } from '../askUserQuestion'
import { kiroMeta } from '../protocol'
import { isKiroTool, KIRO_TOOL, KIRO_TOOL_KINDS } from '../toolKinds'
import { kiroAgentRequest, kiroAgentRun } from './agent'
import { kiroCommandExit, kiroCommandOutput, kiroFileSearchResult, kiroGrepResult, kiroListResult, kiroRawOutput, kiroRequestedTodoItems, kiroTodoItems } from './results'

/**
 * Kiro's tool id of a question, `_meta.kiro.toolId`. The question's own text is its
 * title, so the id is what identifies the call.
 */
const KIRO_USER_INPUT_TOOL_ID = 'user_input'

/** The title Kiro gives a call of a Model Context Protocol tool: `@server/tool`. */
const KIRO_MCP_TITLE = /^@([^/\s]+)\/(.+)$/

/**
 * The separator in the tool name that the model calls a Model Context Protocol tool
 * by, `server___tool`. Kiro states that name as the title of a call it could not
 * route to a server.
 */
const KIRO_MCP_NAME_SEPARATOR = '___'

/** The wrapper Kiro prints around the content of a file that it read. */
const KIRO_READ_CONTENT = /^\s*<file name="[^"]*"[^>]*>\n<content>\n([\s\S]*)\n<\/content>(?:\n<issues>[\s\S]*<\/issues>)?\n<\/file>\s*$/

/** The server and the tool of one Model Context Protocol call, or null for another call. */
function kiroMcpPair(title: string): { server: string, tool: string } | null {
  const match = KIRO_MCP_TITLE.exec(title)
  if (match)
    return { server: match[1] ?? '', tool: match[2] ?? '' }
  return splitPrefixedPair(title, '', KIRO_MCP_NAME_SEPARATOR)
}

/**
 * The arguments of one Model Context Protocol call.
 *
 * Kiro streams the arguments as the model writes them, and it adds its own record
 * of that parse under `_meta`. That record is no argument of the call.
 */
function kiroMcpArgs(args: Record<string, unknown>): Record<string, unknown> {
  const { _meta: _parse, ...own } = args
  return own
}

/**
 * The pictures a Model Context Protocol tool returned.
 *
 * Kiro states each one as a URL, a `data:` URL for a picture the server returned
 * inline.
 */
function kiroMcpImages(tool: Record<string, unknown>): McpContentItem[] {
  const urls = kiroRawOutput(tool)?.imageBase64Urls
  if (!Array.isArray(urls))
    return []
  return urls.filter((url): url is string => typeof url === 'string' && url !== '').map(url => ({ type: 'image' as const, source: { url } }))
}

/**
 * One call of a Model Context Protocol tool.
 *
 * A server call that FAILED gave no answer, so its reason is the body. A call the
 * reader stopped keeps the blocks that arrived, and a call that no answer reached
 * states no result.
 */
function kiroMcpSpec(facts: ACPToolFacts, title: string, pair: { server: string, tool: string }): ToolCallSpec {
  const content = [...facts.content.map(parseMcpContentItem), ...kiroMcpImages(facts.tool)]
  return {
    ...mcpToolCallRequest(pair.server, pair.tool, kiroMcpArgs(facts.args)),
    name: title,
    ...(acpResultAvailable(facts) ? { result: facts.status === 'failed' ? failedResult(facts.text) : { content } } : {}),
  }
}

/**
 * The file content one `Read File` returned, without the wrapper Kiro prints around
 * it for the model.
 *
 * The lines are numbered from the call's own offset, which Kiro counts from zero.
 * Returns null for text that is not that wrapper -- an image, an empty file, an
 * error -- so the shared reader keeps the words.
 */
function kiroReadResult(text: string, offset: number): ReadFileResult | null {
  const match = KIRO_READ_CONTENT.exec(text)
  if (!match)
    return null
  const body = match[1] ?? ''
  const rows = body.split('\n')
  // Kiro prints the content and then its own line break, so a file that ends with a
  // line break ends with an empty row that is no line of the file.
  if (rows.length > 0 && rows[rows.length - 1] === '')
    rows.pop()
  return {
    lines: rows.map((row, index) => ({ num: offset + index + 1, text: row })),
    fallbackContent: body,
  }
}

/** The arguments of one file change, in the keys the shared readers look for. */
function kiroFileChangeArgs(title: string, input: Record<string, unknown>): Record<string, unknown> {
  const path = pickString(input, 'path')
  switch (title) {
    case KIRO_TOOL.ReplaceInFile:
      return { path, old_string: pickString(input, 'oldStr'), new_string: pickString(input, 'newStr') }
    case KIRO_TOOL.WriteFile:
      return { path, content: pickString(input, 'text') }
    case KIRO_TOOL.AppendToFile:
      // An append adds its text at the end, which reads as an insertion.
      return { path, old_string: '', new_string: pickString(input, 'text') }
    case KIRO_TOOL.DeleteFile:
      return { path: pickString(input, 'targetFile') || path }
    default:
      return input
  }
}

/**
 * The arguments of one call, in the keys the shared readers look for.
 *
 * Kiro's own keys differ from the shared ones for the file changes and the searches,
 * so this renames them. Every other call keeps its own arguments.
 */
function kiroArgs(title: string, input: Record<string, unknown>): Record<string, unknown> {
  switch (title) {
    case KIRO_TOOL.ReplaceInFile:
    case KIRO_TOOL.WriteFile:
    case KIRO_TOOL.AppendToFile:
    case KIRO_TOOL.DeleteFile:
      return kiroFileChangeArgs(title, input)
    case KIRO_TOOL.FileSearch:
    case KIRO_TOOL.GrepSearch:
    case KIRO_TOOL.KnowledgeSearch:
    case KIRO_TOOL.ToolSearch: {
      const include = pickString(input, 'includePattern')
      return { pattern: pickString(input, 'query'), ...(include ? { paths: [include] } : {}) }
    }
    default:
      return input
  }
}

/**
 * One shell command, with the output and the exit code that Kiro states apart from
 * the summary it gives the model.
 */
function kiroCommandSpec(facts: ACPToolFacts, base: () => ToolCallSpec): ToolCallSpec {
  const spec = base()
  if (spec.kind !== 'execute' || spec.result === undefined || !('commands' in spec.result))
    return spec
  const output = kiroCommandOutput(facts.tool)
  const exit = kiroCommandExit(facts.tool)
  if (output === undefined && exit === undefined)
    return spec
  const commands = spec.result.commands.map((command) => {
    const withOutput = output !== undefined ? { ...command, output } : command
    return exit ? withCommandExit(withOutput, exit) : withOutput
  })
  return { ...spec, result: { ...spec.result, commands } }
}

/** The actions of Kiro's `Control Process` tool that LeapMux reads. */
const KIRO_PROCESS_START = 'start'
const KIRO_PROCESS_STOP = 'stop'

/**
 * One `Control Process` call. Starting a process runs a command in the background,
 * which the command card draws. Stopping one acts on a process, which the task card
 * draws. Any other action draws the generic card, which states the arguments and
 * the answer as Kiro wrote them, so a new action never reads as a stop.
 */
function kiroControlProcessSpec(facts: ACPToolFacts, title: string, input: Record<string, unknown>): ToolCallSpec {
  const action = pickString(input, 'action')
  if (action === KIRO_PROCESS_START) {
    const remap = acpRemapFacts(facts, { tool: { ...facts.tool, [ACP_SUPPLEMENT_REQUEST.RawInput]: { command: pickString(input, 'command') } }, kind: 'execute' })
    return { ...acpSpecFor(remap, 'execute'), name: title, metadata: [{ label: 'Background', value: 'Yes' }] }
  }
  if (action !== KIRO_PROCESS_STOP)
    return { ...kiroGenericSpec(facts), name: title }
  const terminalId = pickString(input, 'terminalId')
  const request: ToolRequestByKind['task'] = { action: 'stop', ...(terminalId ? { taskId: terminalId } : {}) }
  if (!acpResultAvailable(facts))
    return { kind: 'task', name: title, request, title }
  if (facts.status === 'failed')
    return { kind: 'task', name: title, request, title, result: failedResult(facts.text) }
  return { kind: 'task', name: title, request, title, result: { outcome: 'stopped', output: facts.text } }
}

/**
 * One `Task List` call.
 *
 * The request is what the call ASKED: the tasks it creates or adds. A call that
 * completes or removes tasks states them by id and asks for no task. The result is
 * the whole list Kiro holds after the call, which each finished call states.
 */
function kiroTodoSpec(facts: ACPToolFacts, title: string, input: Record<string, unknown>): ToolCallSpec {
  const asked = kiroRequestedTodoItems(input)
  const note = pickString(input, 'task_list_description').trim()
  const request: ToolRequestByKind['todo'] = { items: asked, ...(note ? { note } : {}) }
  // No title: `todoRenderer` composes the words from the list. An explicit undefined,
  // because the ACP wrapper spreads this over the frame's own title.
  const base = { kind: 'todo' as const, name: title, title: undefined, request }
  // A call that no answer reached states no list: the asked tasks are not the list
  // that Kiro holds.
  if (!acpResultAvailable(facts))
    return base
  if (facts.status === 'failed')
    return { ...base, result: failedResult(facts.text) }
  const tasks = kiroRawOutput(facts.tool)?.tasks
  return { ...base, result: { items: Array.isArray(tasks) ? kiroTodoItems(tasks) : asked } }
}

/**
 * The call that ends plan mode: Kiro writes the plan into the call's arguments, and
 * switches the session to the mode that runs it. Kiro raises no approval of its own
 * for the plan, so the row states the plan and the switch, and nothing more.
 */
function kiroSwitchToExecutionSpec(facts: ACPToolFacts, title: string, input: Record<string, unknown>): ToolCallSpec {
  const plan = pickString(input, 'plan').trim()
  const spec = acpSpecFor(acpRemapFacts(facts, { tool: facts.tool, kind: 'switch_mode' }), 'switch_mode')
  if (!plan || facts.status === 'failed' || !acpResultAvailable(facts))
    return { ...spec, name: title, title: 'Switch to execution' }
  return { ...spec, name: title, title: 'Switch to execution', result: proseResult(plan, 'markdown') }
}

/**
 * The generic card of one call: the arguments and the answer as Kiro wrote them.
 *
 * `base()` cannot supply it for an `execute` call, because the shared build answers
 * the kind that the wire states, and that is a command card with no command.
 */
function kiroGenericSpec(facts: ACPToolFacts): ToolCallSpec {
  return acpSpecFor(acpRemapFacts(facts, { tool: facts.tool, kind: 'unspecified' }), 'unspecified')
}

/**
 * The shared build of one kind, over the arguments in the keys its readers look for.
 *
 * Generic over the title, so the build answers the literal kind that the table states
 * for it, and a branch can put that kind's own result on it.
 */
function kiroRemappedSpec<T extends keyof typeof KIRO_TOOL_KINDS>(facts: ACPToolFacts, title: T, input: Record<string, unknown>) {
  const kind: (typeof KIRO_TOOL_KINDS)[T] = KIRO_TOOL_KINDS[title]
  const remapFacts = acpRemapFacts(facts, { tool: { ...facts.tool, [ACP_SUPPLEMENT_REQUEST.RawInput]: kiroArgs(title, input) }, kind })
  return acpSpecFor(remapFacts, kind)
}

/** Kiro identifies each built-in call by its title, and the rest by `_meta.kiro`. */
export const kiroToolCallAdapter: ACPToolCallAdapter = (facts, base) => {
  const meta = kiroMeta(facts.tool)
  const title = pickString(facts.tool, 'title')
  const input = facts.args

  if (pickString(meta, KIRO_META.Kind) === KIRO_KIND.AgentSubtask) {
    const request = kiroAgentRequest(input, title, pickString(facts.tool, 'toolCallId'))
    const subtaskId = pickString(meta, KIRO_META.AgentSubtaskId)
    // `facts.finished`, never the frame's own status: a retained row of a turn that
    // ended keeps its report, and `kiroAgentRun` states a run that sent none.
    return { kind: 'agent', name: title, request, ...(facts.finished ? { result: { agents: [kiroAgentRun(facts, request, subtaskId)] } } : {}) }
  }
  if (pickString(meta, 'toolId') === KIRO_USER_INPUT_TOOL_ID) {
    // The answer is the reply to Kiro's own question request, which draws in the
    // transcript as its own row. The call states the question alone.
    const questions = kiroUserInputQuestions(title, meta?.userInputOptions)
    const asked = { kind: 'question' as const, name: KIRO_USER_INPUT_TOOL_ID, request: { questions: questions.slice(0, 1) }, title: 'Question' }
    return acpResultAvailable(facts) && facts.status === 'failed' ? { ...asked, result: failedResult(facts.text) } : asked
  }
  const mcp = kiroMcpPair(title)
  if (mcp && facts.wireKind !== 'execute')
    return kiroMcpSpec(facts, title, mcp)
  // A shell call takes the model's description as its title, which can equal the
  // title of any tool. The process tool is the one other `execute` call, and it
  // states an action.
  if (facts.wireKind === 'execute' && !(title === KIRO_TOOL.ControlProcess && pickString(input, 'action')))
    return kiroCommandSpec(facts, base)
  if (!isKiroTool(title))
    return { ...base(), ...(title ? { name: title } : {}) }

  // Whether Kiro answered the call with a result that a typed reader can parse.
  const answered = acpResultAvailable(facts) && facts.status !== 'failed'
  switch (title) {
    case KIRO_TOOL_TITLE.TaskList:
      return kiroTodoSpec(facts, title, input)
    case KIRO_TOOL_TITLE.SwitchToExecution:
      return kiroSwitchToExecutionSpec(facts, title, input)
    case KIRO_TOOL.ControlProcess:
      return kiroControlProcessSpec(facts, title, input)
    case KIRO_TOOL.ReadFile: {
      const spec = kiroRemappedSpec(facts, title, input)
      const content = answered ? kiroReadResult(facts.text, pickNumber(input, 'offset') ?? 0) : null
      return content ? { ...spec, name: title, result: content } : { ...spec, name: title }
    }
    case KIRO_TOOL.ListDirectory: {
      const spec = kiroRemappedSpec(facts, title, input)
      const listing = answered ? kiroListResult(facts.text) : null
      return listing ? { ...spec, name: title, result: listing } : { ...spec, name: title }
    }
    case KIRO_TOOL.FileSearch: {
      const spec = kiroRemappedSpec(facts, title, input)
      const files = answered ? kiroFileSearchResult(facts.text) : null
      return files ? { ...spec, name: title, result: files } : { ...spec, name: title }
    }
    case KIRO_TOOL.GrepSearch: {
      const spec = kiroRemappedSpec(facts, title, input)
      const matches = answered ? kiroGrepResult(facts.text) : null
      return matches ? { ...spec, name: title, result: matches } : { ...spec, name: title }
    }
    // `Update Session Information`, `Report Progress` and `Memory` need no case of
    // their own: the shared build of `report` and `memory` states the arguments as
    // the payload and Kiro's words as the answer.
    default:
      return { ...kiroRemappedSpec(facts, title, input), name: title }
  }
}
