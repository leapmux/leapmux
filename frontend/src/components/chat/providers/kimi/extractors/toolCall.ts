import type { FileEditDiff } from '../../../model/fileEditDiff'
import type { ProseResult, ToolCall, ToolCallEnvelope, ToolCallLifecycleFacts, ToolCallSpecReaderTable, ToolCallSpecVariant, ToolFailureResult, UnparsedToolResult } from '../../../model/toolCall'
import type { ToolKind } from '../../../model/toolKind'
import type { ToolRequestByKind } from '../../../model/tools'
import type { AgentRun } from '../../../model/tools/agent'
import type { CommandLanguage } from '../../../model/tools/execute'
import type { FileChangeResult } from '../../../model/tools/fileChange'
import type { GenericToolResult } from '../../../model/tools/generic'
import type { SearchResult } from '../../../model/tools/search'
import type { TaskRequest } from '../../../model/tools/task'
import type { TriggerRequest } from '../../../model/tools/trigger'
import type { ToolRequestOverrides } from '../../defaultToolRequests'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { KIMI_EVENT, KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { parseDataImageUrl } from '~/lib/imageBlocks'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { createToolCall } from '../../../model/createToolCall'
import { parseMcpToolName } from '../../../model/mcpToolCall'
import { parseReadContent } from '../../../model/readFileResult'
import { failedResult, proseResult, readToolCallSpec, unparsedResult } from '../../../model/toolCall'
import { DEFAULT_TOOL_REQUESTS, toolRequestFor } from '../../defaultToolRequests'
import { retainedOutcome, retainedRowIsFinal } from '../../registry'
import { kimiQuestionsFromToolInput } from '../askUserQuestion'
import { kimiDisplay, kimiEventData } from '../protocol'
import { kimiToolKind } from '../toolKinds'
import { kimiTodoItems } from './todo'

/**
 * One Kimi Code tool call, as the persisted rows state it.
 *
 * The server states a call in two events. `tool.call.started` carries the tool name,
 * the arguments and a `display` that words the call for a UI; `tool.result` carries the
 * output and nothing else -- no name, no arguments, no display. So a result row reads
 * the call's facts off its paired start.
 *
 * A turn that ended while the call ran leaves no result: the worker stores the START
 * payload again as the call's closing row, with the completion column stating how the
 * turn ended. That row is finished although its bytes are a start, and it carries the
 * output the call printed before it stopped in the fields a result states it in.
 */
export interface KimiToolRow {
  toolCallId: string
  toolName: string
  args: Record<string, unknown>
  display: Record<string, unknown> | undefined
  /**
   * The payload that states the call's output: the `tool.result`, or a retained start
   * that carries the output the call printed. Null while the call runs, and for a
   * retained start that printed nothing.
   */
  result: Record<string, unknown> | null
  /** True for a retained start: the turn ended while the call ran. */
  retained: boolean
  /** True when this row is the last one of its call. */
  finished: boolean
  lifecycle: ToolCallLifecycleFacts
}

/**
 * Read one tool row of a span into the facts of its call.
 *
 * `request` is the span's start row and `pairedResult` the span's result row, which the
 * store resolves separately. The row's OWN bytes decide which half it is.
 */
export function kimiToolRow(
  parsed: unknown,
  spanType: string | undefined,
  request: unknown,
  pairedResult: unknown,
  completion: MessageCompletion | undefined,
): KimiToolRow | null {
  const ownStart = kimiEventData(parsed, KIMI_EVENT.ToolCallStarted)
  const ownResult = kimiEventData(parsed, KIMI_EVENT.ToolResult)
  if (!ownStart && !ownResult)
    return null
  const retainedFinal = !!ownStart && retainedRowIsFinal(completion)
  const start = ownStart ?? kimiEventData(request, KIMI_EVENT.ToolCallStarted)
  const result = ownResult ?? (retainedFinal ? kimiRetainedOutput(ownStart) : kimiEventData(pairedResult, KIMI_EVENT.ToolResult))
  const toolCallId = pickString(ownStart ?? ownResult, 'toolCallId')
  // The result frame states no name, and the worker stamps the call's name on every
  // row of the span as its span type, so a result whose start is out of the loaded
  // window still states its tool.
  const toolName = pickString(start, 'name') || spanType || ''
  const finished = !!ownResult || retainedFinal
  return {
    toolCallId,
    toolName,
    args: pickObject(start, 'args') ?? {},
    display: kimiDisplay(start),
    result,
    retained: retainedFinal,
    finished,
    lifecycle: {
      frameStatus: 'unstated',
      providerOutcome: result?.isError === true ? 'failed' : null,
      retainedOutcome: retainedOutcome(retainedFinal ? completion : undefined),
      rowFinal: finished,
      resultFrameLanded: result !== null,
    },
  }
}

/** The retained start of a call, as its result, when it carries output. */
function kimiRetainedOutput(start: Record<string, unknown> | null): Record<string, unknown> | null {
  if (!start || (typeof start.output !== 'string' && !Array.isArray(start.output)))
    return null
  return start
}

/**
 * Everything one row states, collected once before any specification decision runs.
 */
export interface KimiToolFacts {
  row: KimiToolRow
  toolName: string
  kind: ToolKind
  input: Record<string, unknown>
  /** The `display.kind` word of the call, or '' when it states none. */
  displayKind: string
  /** The words the result states, with a Bash exit trailer removed. */
  text: string
  images: ImageResultSource[]
  failed: boolean
  truncated: boolean
  /** True when this row can build a result: a result frame landed for the call. */
  resultAvailable: boolean
}

/** Collect the facts of one row and settle the kind it takes. */
export function kimiToolFacts(row: KimiToolRow): KimiToolFacts {
  const { text, images } = kimiOutput(row.result?.output)
  const kind = kimiToolKind(row.toolName)
  const facts: KimiToolFacts = {
    row,
    toolName: row.toolName,
    kind,
    input: kimiResolvedInput(kind, row),
    displayKind: pickString(row.display, 'kind'),
    text,
    images,
    failed: row.result?.isError === true,
    truncated: row.result?.truncated === true,
    resultAvailable: row.result !== null,
  }
  return { ...facts, kind: kimiReclassify(facts) }
}

/**
 * The kind a row takes after its first classification.
 *
 * A to-do call whose arguments carry no list only READS the list and changes nothing,
 * and a file change that states no file is not a file change: each takes the generic
 * card, which keeps the arguments the tool sent. A row that states no tool name at all
 * takes the same card.
 */
export function kimiReclassify(facts: KimiToolFacts): ToolKind {
  if (facts.kind === 'todo' && !Array.isArray(facts.input.todos))
    return 'other'
  if (facts.kind === 'unspecified')
    return 'other'
  if ((facts.kind === 'edit' || facts.kind === 'write') && kimiFileChanges(facts.kind, facts.input).length === 0)
    return 'other'
  return facts.kind
}

/**
 * The arguments, with the absolute path the display states for a file call.
 *
 * The model states a path relative to the working directory, and the display states it
 * resolved. The row titles itself with the resolved one, so a relative `goal.txt` reads
 * as the file it wrote.
 */
function kimiResolvedInput(kind: ToolKind, row: KimiToolRow): Record<string, unknown> {
  if (kind !== 'read' && kind !== 'write' && kind !== 'edit')
    return row.args
  const displayed = pickString(row.display, 'path')
  return displayed ? { ...row.args, path: displayed } : row.args
}

/**
 * The words and the pictures a result's `output` states.
 *
 * The output is a string, or a list of content parts: text, and an image, audio or
 * video by URL. The text parts join, and an image part whose URL this build can draw
 * becomes a picture. A clip is stated by its URL alone.
 */
export function kimiOutput(output: unknown): { text: string, images: ImageResultSource[] } {
  if (typeof output === 'string')
    return { text: output, images: [] }
  if (!Array.isArray(output))
    return { text: '', images: [] }
  const texts: string[] = []
  const images: ImageResultSource[] = []
  for (const part of output) {
    if (!isObject(part))
      continue
    switch (part.type) {
      case 'text':
        texts.push(pickString(part, 'text'))
        break
      case 'image_url': {
        const url = pickString(pickObject(part, 'imageUrl'), 'url')
        const data = parseDataImageUrl(url)
        if (data)
          images.push({ mimeType: data.mimeType, data: data.base64 })
        else if (url)
          images.push({ url })
        break
      }
      case 'audio_url':
      case 'video_url': {
        const url = pickString(pickObject(part, part.type === 'audio_url' ? 'audioUrl' : 'videoUrl'), 'url')
        if (url)
          texts.push(url)
        break
      }
      default:
        break
    }
  }
  return { text: texts.join('\n'), images }
}

/**
 * The trailer Kimi Code writes after a command that failed, and the exit code in it.
 * `bashTool.ts` appends `Command failed with exit code: N.` to the output.
 */
const KIMI_EXIT_TRAILER = /(?:^|\n)Command failed with exit code: (-?\d+)\.\s*$/

/** A foreground command the server stopped rather than one that failed. */
const KIMI_STOPPED_COMMAND = /(?:^|\n)(?:Command killed by timeout \([^)]*\)|Interrupted by user)\s*$/

/** The languages a command display may state, which the command body highlights. */
const KIMI_COMMAND_LANGUAGES: ReadonlySet<string> = new Set<CommandLanguage>(['bash', 'powershell', 'javascript', 'sql'])

function isKimiCommandLanguage(value: string): value is CommandLanguage {
  return KIMI_COMMAND_LANGUAGES.has(value)
}

/** The action each task tool performs. */
const KIMI_TASK_ACTIONS: ReadonlyMap<string, TaskRequest['action']> = new Map<string, TaskRequest['action']>([
  [KIMI_TOOL.TaskList, 'list'],
  [KIMI_TOOL.TaskOutput, 'output'],
  [KIMI_TOOL.TaskStop, 'stop'],
])

/** The action each cron tool performs. The tool name states it; no argument does. */
const KIMI_TRIGGER_ACTIONS: ReadonlyMap<string, TriggerRequest['action']> = new Map<string, TriggerRequest['action']>([
  [KIMI_TOOL.CronCreate, 'create'],
  [KIMI_TOOL.CronList, 'list'],
  [KIMI_TOOL.CronDelete, 'delete'],
])

/**
 * The kinds Kimi Code reads DIFFERENTLY from the shared table, and nothing else.
 *
 * Every entry reads a fact the shared entry cannot supply: the display, the tool name,
 * the argument spellings Kimi Code alone sends. Every other kind takes
 * `DEFAULT_TOOL_REQUESTS`. EVERY entry declares its own return type, for the reason
 * `DEFAULT_TOOL_REQUESTS` gives.
 */
export const KIMI_TOOL_REQUEST_OVERRIDES: ToolRequestOverrides<KimiToolFacts> = {
  // The subagent TYPE, which Kimi spells `subagent_type`, and a swarm's description,
  // which is its whole statement: a swarm states a template and items, not a prompt.
  agent: (args, facts): ToolRequestByKind['agent'] => {
    const agentType = pickString(args, 'subagent_type')
    const prompt = pickString(args, 'prompt') || pickString(args, 'prompt_template')
    return {
      description: pickString(args, 'description') || pickString(facts.row.display, 'agent_name'),
      ...(agentType ? { agentType } : {}),
      prompt,
    }
  },
  // The working directory and the language, which the display states.
  execute: (args, facts): ToolRequestByKind['execute'] => {
    const description = pickString(args, 'description')
    const cwd = pickString(args, 'cwd') || pickString(facts.row.display, 'cwd')
    const language = pickString(facts.row.display, 'language')
    return {
      command: pickString(args, 'command') || pickString(facts.row.display, 'command'),
      ...(description ? { description } : {}),
      ...(cwd ? { cwd } : {}),
      ...(isKimiCommandLanguage(language) ? { language } : {}),
    }
  },
  // A write states its body as `content`, which the shared entry does not read.
  write: (args): ToolRequestByKind['write'] => ({ changes: kimiFileChanges('write', args) }),
  edit: (args): ToolRequestByKind['edit'] => ({ changes: kimiFileChanges('edit', args), ...(args.replace_all === true ? { replaceAll: true } : {}) }),
  // A read states its window as `line_offset` and `n_lines`.
  read: (args): ToolRequestByKind['read'] => {
    const offset = typeof args.line_offset === 'number' ? args.line_offset : undefined
    const limit = typeof args.n_lines === 'number' ? args.n_lines : undefined
    return {
      path: pickString(args, 'path'),
      ...(offset !== undefined ? { offset } : {}),
      ...(limit !== undefined ? { limit } : {}),
    }
  },
  // The Model Context Protocol server and tool, which the wire NAME states.
  mcp: (args, facts): ToolRequestByKind['mcp'] => {
    const identity = parseMcpToolName(facts.toolName)
    return { args, server: identity?.server ?? '', tool: identity?.tool ?? facts.toolName }
  },
  // The parsed QUESTIONS. The shared entry states an empty list.
  question: (args): ToolRequestByKind['question'] => ({ questions: kimiQuestionsFromToolInput(args) }),
  // The ACTION, which the tool name states.
  task: (args, facts): ToolRequestByKind['task'] => {
    const taskId = pickString(args, 'task_id')
    return { action: KIMI_TASK_ACTIONS.get(facts.toolName) ?? 'other', ...(taskId ? { taskId } : {}) }
  },
  // Kimi's own item shape: `title` and a `done` status.
  todo: (args): ToolRequestByKind['todo'] => ({ items: kimiTodoItems(args.todos) }),
  // The id and the schedule under Kimi's spellings, and the action the tool name states.
  trigger: (args, facts): ToolRequestByKind['trigger'] => {
    const base = DEFAULT_TOOL_REQUESTS.trigger(args)
    const prompt = pickString(args, 'prompt')
    return {
      ...base,
      ...(base.name === undefined && prompt ? { name: prompt } : {}),
      action: KIMI_TRIGGER_ACTIONS.get(facts.toolName) ?? 'other',
    }
  },
  // A wait states its limit in SECONDS.
  wait: (args): ToolRequestByKind['wait'] => (typeof args.timeout === 'number' ? { durationMs: args.timeout * 1000 } : {}),
  // The mode the switch lands in, which the tool name states.
  switch_mode: (_args, facts): ToolRequestByKind['switch_mode'] => ({ mode: facts.toolName === KIMI_TOOL.EnterPlanMode ? 'plan' : 'default' }),
  // A notice the agent sends the user.
  message: (args): ToolRequestByKind['message'] => ({ text: pickString(args, 'message') || pickString(args, 'body') || pickString(args, 'title') }),
}

/** One kind's declared request: Kimi Code's own reading, or the shared table's. */
function kimiRequestFor<K extends ToolKind>(kind: K, facts: KimiToolFacts): ToolRequestByKind[K] {
  return toolRequestFor(kind, facts.input, facts, KIMI_TOOL_REQUEST_OVERRIDES)
}

/**
 * One reader for each kind, each checked against its OWN kind's request and result.
 *
 * Total over `ToolKind`, so a new kind is a compile error here. EVERY entry declares its
 * own return type, for the reason the zcode table states at its own declaration.
 */
export const KIMI_TOOL_READERS: ToolCallSpecReaderTable<KimiToolFacts> = {
  execute: (facts): ToolCallSpecVariant<'execute'> => {
    const request = kimiRequestFor('execute', facts)
    if (!facts.resultAvailable)
      return { kind: 'execute', request }
    const trailer = KIMI_EXIT_TRAILER.exec(facts.text)
    const output = trailer ? facts.text.slice(0, trailer.index) : facts.text
    const stopped = facts.failed && KIMI_STOPPED_COMMAND.test(facts.text)
    // A command the turn cut off stated no exit code, and one that failed states it in
    // its trailer. Only a command that returned on its own succeeded.
    const exitCode = trailer ? Number(trailer[1]) : facts.failed || facts.row.retained ? undefined : 0
    return {
      kind: 'execute',
      request,
      result: { commands: [{ output, ...(exitCode !== undefined ? { exitCode } : {}) }], unresolvedTerminals: [] },
      ...(stopped ? { statusOverride: 'cancelled' as const } : {}),
    }
  },
  read: (facts): ToolCallSpecVariant<'read'> => {
    const request = kimiRequestFor('read', facts)
    if (!facts.resultAvailable)
      return { kind: 'read', request }
    if (facts.failed)
      return { kind: 'read', request, result: failedResult(facts.text) }
    // `N<tab>line` for each line, then a `<system>` note, which the shared reader peels
    // into an alert.
    const parts = parseReadContent(facts.text)
    return {
      kind: 'read',
      request,
      result: {
        lines: parts.lines,
        fallbackContent: facts.text,
        ...(parts.leading.length > 0 ? { leading: parts.leading } : {}),
        ...(parts.trailing.length > 0 ? { trailing: parts.trailing } : {}),
      },
      images: facts.images,
    }
  },
  glob: (facts): ToolCallSpecVariant<'glob'> => ({ kind: 'glob', request: kimiRequestFor('glob', facts), ...kimiSearchResult(facts, 'glob') }),
  grep: (facts): ToolCallSpecVariant<'grep'> => ({ kind: 'grep', request: kimiRequestFor('grep', facts), ...kimiSearchResult(facts, 'grep') }),
  edit: (facts): ToolCallSpecVariant<'edit'> => ({ kind: 'edit', request: kimiRequestFor('edit', facts), ...kimiFileChangeResult(facts, 'edit') }),
  write: (facts): ToolCallSpecVariant<'write'> => ({ kind: 'write', request: kimiRequestFor('write', facts), ...kimiFileChangeResult(facts, 'write') }),
  fetch: (facts): ToolCallSpecVariant<'fetch'> => {
    const request = kimiRequestFor('fetch', facts)
    if (!facts.resultAvailable)
      return { kind: 'fetch', request }
    if (facts.failed)
      return { kind: 'fetch', request, result: failedResult(facts.text) }
    return { kind: 'fetch', request, result: { result: facts.text } }
  },
  web_search: (facts): ToolCallSpecVariant<'web_search'> => {
    const request = kimiRequestFor('web_search', facts)
    if (!facts.resultAvailable)
      return { kind: 'web_search', request }
    if (facts.failed)
      return { kind: 'web_search', request, result: failedResult(facts.text) }
    return { kind: 'web_search', request, result: { links: [], summary: facts.text } }
  },
  todo: (facts): ToolCallSpecVariant<'todo'> => {
    // NEVER empty by accident: `kimiReclassify` answers `other` for a call with no list.
    const request = kimiRequestFor('todo', facts)
    if (!facts.resultAvailable)
      return { kind: 'todo', request }
    if (facts.failed)
      return { kind: 'todo', request, result: failedResult(facts.text) }
    return { kind: 'todo', request, result: { items: request.items } }
  },
  agent: (facts): ToolCallSpecVariant<'agent'> => {
    const request = kimiRequestFor('agent', facts)
    const title = kimiCallTitle(facts)
    if (!facts.resultAvailable)
      return { kind: 'agent', request, title }
    if (facts.failed)
      return { kind: 'agent', request, title, result: failedResult(facts.text) }
    const runs = facts.toolName === KIMI_TOOL.AgentSwarm ? kimiSwarmRuns(facts.text) : kimiAgentRuns(facts.text, request.description)
    if (runs.length > 0)
      return { kind: 'agent', request, title, result: { agents: runs } }
    return { kind: 'agent', request, title, ...(facts.text ? { result: unparsedResult(facts.text) } : {}) }
  },
  question: (facts): ToolCallSpecVariant<'question'> => {
    const request = kimiRequestFor('question', facts)
    const title = kimiCallTitle(facts)
    if (!facts.resultAvailable)
      return { kind: 'question', request, title }
    if (facts.failed)
      return { kind: 'question', request, title, result: failedResult(facts.text) }
    const answers = kimiQuestionAnswers(facts.text)
    return { kind: 'question', request, title, ...(answers ? { result: { answers } } : facts.text ? { result: unparsedResult(facts.text) } : {}) }
  },
  task: (facts): ToolCallSpecVariant<'task'> => {
    const request = kimiRequestFor('task', facts)
    const title = kimiCallTitle(facts)
    if (!facts.resultAvailable)
      return { kind: 'task', request, title }
    if (facts.failed)
      return { kind: 'task', request, title, result: failedResult(facts.text) }
    return { kind: 'task', request, title, result: { outcome: 'completed', output: facts.text } }
  },
  mcp: (facts): ToolCallSpecVariant<'mcp'> => ({ kind: 'mcp', request: kimiRequestFor('mcp', facts), ...kimiGenericResult(facts) }),
  skill: (facts): ToolCallSpecVariant<'skill'> => ({ kind: 'skill', request: kimiRequestFor('skill', facts), title: kimiCallTitle(facts), ...kimiProseResult(facts) }),
  switch_mode: (facts): ToolCallSpecVariant<'switch_mode'> => ({ kind: 'switch_mode', request: kimiRequestFor('switch_mode', facts), title: kimiCallTitle(facts), ...kimiProseResult(facts) }),
  trigger: (facts): ToolCallSpecVariant<'trigger'> => ({ kind: 'trigger', request: kimiRequestFor('trigger', facts), title: kimiCallTitle(facts), ...kimiProseResult(facts) }),
  report: (facts): ToolCallSpecVariant<'report'> => ({ kind: 'report', request: kimiRequestFor('report', facts), title: kimiCallTitle(facts), ...kimiProseResult(facts) }),
  wait: (facts): ToolCallSpecVariant<'wait'> => ({ kind: 'wait', request: kimiRequestFor('wait', facts), title: kimiCallTitle(facts), ...kimiProseResult(facts) }),
  message: (facts): ToolCallSpecVariant<'message'> => ({ kind: 'message', request: kimiRequestFor('message', facts), title: kimiCallTitle(facts), ...kimiProseResult(facts) }),
  // The generic card, for a tool no vocabulary lists.
  other: (facts): ToolCallSpecVariant<'other'> => ({ kind: 'other', request: kimiRequestFor('other', facts), ...kimiGenericResult(facts) }),
  // UNREACHABLE: `kimiReclassify` folds `unspecified` to `other`. The entry exists
  // because the table is total, and it states the same card at its own kind.
  unspecified: (facts): ToolCallSpecVariant<'unspecified'> => ({ kind: 'unspecified', request: kimiRequestFor('unspecified', facts), ...kimiGenericResult(facts) }),
  // The kinds no Kimi Code tool takes: `KIMI_TOOL_KINDS` maps no name to any of them.
  agents: kimiArgumentsOnly('agents'),
  chart: kimiArgumentsOnly('chart'),
  delete: kimiArgumentsOnly('delete'),
  image: kimiArgumentsOnly('image'),
  list: kimiArgumentsOnly('list'),
  memory: kimiArgumentsOnly('memory'),
  move: kimiArgumentsOnly('move'),
  search: kimiArgumentsOnly('search'),
  think: kimiArgumentsOnly('think'),
}

/**
 * The reader of a kind Kimi Code never produces: the shared request, and the words the
 * call printed.
 */
function kimiArgumentsOnly<P extends ToolKind>(kind: P): (facts: KimiToolFacts) => ToolCallSpecVariant<P> {
  return (facts): ToolCallSpecVariant<P> => ({ kind, request: kimiRequestFor(kind, facts), title: kimiCallTitle(facts), ...kimiUnreadResult(facts) })
}

/** The row's header words: the call's own description, then the tool's name. */
function kimiCallTitle(facts: KimiToolFacts): string {
  return pickString(facts.row.display, 'description') || pickString(facts.input, 'description') || facts.toolName || 'Tool'
}

/** The result of a kind whose answer this build cannot read into a shape. */
function kimiUnreadResult(facts: KimiToolFacts): { result?: ToolFailureResult | UnparsedToolResult } {
  if (!facts.resultAvailable)
    return {}
  if (facts.failed)
    return { result: failedResult(facts.text) }
  return facts.text ? { result: unparsedResult(facts.text) } : {}
}

/** The result every prose kind shares: none, the failure, or the words. */
function kimiProseResult(facts: KimiToolFacts): { result?: ProseResult | ToolFailureResult } {
  if (!facts.resultAvailable)
    return {}
  return facts.failed ? { result: failedResult(facts.text) } : { result: proseResult(facts.text) }
}

/** The result the generic card states: the words and the pictures together. */
function kimiGenericResult(facts: KimiToolFacts): { result?: GenericToolResult | ToolFailureResult } {
  if (!facts.resultAvailable)
    return {}
  if (facts.failed)
    return { result: failedResult(facts.text) }
  return {
    result: {
      content: [
        ...(facts.text ? [{ type: 'text' as const, text: facts.text }] : []),
        ...facts.images.map(source => ({ type: 'image' as const, source })),
      ],
    },
  }
}

/**
 * What a Glob or a Grep found.
 *
 * Glob prints one path for each line. Grep prints what its output mode asks for:
 * `path:line:text` lines for content, paths for a file list, and `path:count` for a
 * count. The body draws the text as the tool printed it, and the counters are read off
 * the lines.
 */
function kimiSearchResult(facts: KimiToolFacts, kind: 'glob' | 'grep'): { result?: SearchResult | ToolFailureResult } {
  if (!facts.resultAvailable)
    return {}
  if (facts.failed)
    return { result: failedResult(facts.text) }
  const lines = facts.text.split('\n').map(line => line.trimEnd()).filter(line => line !== '' && !/^<system>.*<\/system>$/.test(line))
  const empty = lines.length === 0 || /^No (?:files|matches) found\.?$/i.test(lines[0] ?? '')
  if (kind === 'glob') {
    const filenames = empty ? [] : lines
    return { result: { filenames, content: '', numFiles: filenames.length, numLines: 0, truncated: facts.truncated, fallbackContent: facts.text, empty } }
  }
  const mode = pickString(facts.input, 'output_mode')
  if (mode === 'files_with_matches') {
    const filenames = empty ? [] : lines
    return { result: { filenames, content: '', numFiles: filenames.length, numLines: 0, truncated: facts.truncated, fallbackContent: facts.text, empty, mode: 'files_with_matches' } }
  }
  const files = new Set<string>()
  for (const line of lines) {
    const colon = line.indexOf(':')
    if (colon > 0)
      files.add(line.slice(0, colon))
  }
  return {
    result: {
      filenames: [...files],
      content: empty ? '' : lines.join('\n'),
      numFiles: files.size,
      numLines: empty ? 0 : lines.length,
      truncated: facts.truncated,
      fallbackContent: facts.text,
      empty,
      ...(mode === 'count_matches' ? { mode: 'count' as const } : { mode: 'content' as const }),
    },
  }
}

/** The result `edit` and `write` share: the change the call made, or the failure. */
function kimiFileChangeResult(facts: KimiToolFacts, kind: 'edit' | 'write'): { result?: FileChangeResult | ToolFailureResult | UnparsedToolResult } {
  if (!facts.resultAvailable)
    return {}
  if (facts.failed)
    return { result: failedResult(facts.text) }
  const changes = kimiFileChanges(kind, facts.input)
  return changes.length > 0 ? { result: { changes } } : { result: unparsedResult(facts.text) }
}

/** The change an edit or a write asks for, from its arguments. */
export function kimiFileChanges(kind: 'edit' | 'write', args: Record<string, unknown>): FileEditDiff[] {
  const filePath = pickString(args, 'path')
  if (!filePath)
    return []
  if (kind === 'write')
    return [{ filePath, operation: 'add', oldStr: '', newStr: pickString(args, 'content'), structuredPatch: null }]
  return [{ filePath, operation: 'edit', oldStr: pickString(args, 'old_string'), newStr: pickString(args, 'new_string'), structuredPatch: null }]
}

/**
 * The run an `Agent` call reports.
 *
 * The result is a key-value header, then the subagent's summary:
 * `agent_id: agent-0`, `status: completed`, `[summary]`, the text, and a resume hint.
 */
export function kimiAgentRuns(text: string, description: string): AgentRun[] {
  const agentId = /^agent_id: (\S+)$/m.exec(text)?.[1]
  if (!agentId)
    return []
  const status = /^status: (\S+)$/m.exec(text)?.[1] ?? ''
  const agentType = /^actual_subagent_type: (\S+)$/m.exec(text)?.[1]
  const summary = /\[summary\]\n([\s\S]*?)(?:\n\nresume_hint:[\s\S]*)?$/.exec(text)?.[1]?.trim() ?? ''
  return [{
    description,
    agentId,
    outcome: kimiRunOutcome(status),
    ...(status && kimiRunOutcome(status) === 'unknown' ? { statusLabel: status } : {}),
    metadata: agentType ? [{ label: 'Type', value: agentType }] : [],
    body: summary,
  }]
}

/**
 * The runs an `AgentSwarm` call reports: one `<subagent>` element for each member,
 * with its item and its outcome.
 */
export function kimiSwarmRuns(text: string): AgentRun[] {
  const runs: AgentRun[] = []
  for (const match of text.matchAll(/<subagent agent_id="([^"]*)"(?: item="([^"]*)")? outcome="([^"]*)">([\s\S]*?)<\/subagent>/g)) {
    const [, agentId = '', item = '', outcome = '', body = ''] = match
    runs.push({
      description: item || agentId,
      agentId,
      outcome: kimiRunOutcome(outcome),
      ...(kimiRunOutcome(outcome) === 'unknown' && outcome ? { statusLabel: outcome } : {}),
      metadata: [],
      body: body.trim(),
    })
  }
  return runs
}

/** A subagent's status word, in the shared outcome vocabulary. */
function kimiRunOutcome(status: string): AgentRun['outcome'] {
  switch (status) {
    case 'completed':
      return 'completed'
    case 'failed':
    case 'timed_out':
      return 'failed'
    case 'cancelled':
    case 'killed':
      return 'stopped'
    case 'running':
      return 'running'
    default:
      return 'unknown'
  }
}

/**
 * The answers an `AskUserQuestion` result states: `{"answers":{"<question>":"<answer>"}}`,
 * the text the server wrote for the model. A dismissal states an empty map.
 */
function kimiQuestionAnswers(text: string): { header: string, answer: string | null }[] | null {
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  }
  catch {
    return null
  }
  const answers = pickObject(isObject(parsed) ? parsed : undefined, 'answers')
  if (!answers)
    return null
  return Object.entries(answers).map(([header, answer]) => ({ header, answer: typeof answer === 'string' ? answer : null }))
}

/** One Kimi Code tool call, as the kind-discriminated pair. */
export function kimiToolCall(row: KimiToolRow): ToolCall {
  const facts = kimiToolFacts(row)
  const envelope: ToolCallEnvelope = { id: row.toolCallId, name: row.toolName, lifecycle: row.lifecycle }
  const spec = readToolCallSpec(KIMI_TOOL_READERS, facts.kind, facts)
  // The tool's own name, which the icon tooltip states.
  const label = spec.label ?? (row.toolName || undefined)
  return createToolCall(envelope, { ...spec, ...(label !== undefined ? { label } : {}) })
}
