import type { ToolSpanRowRole } from '../../../model/row'
import type { ToolCall, ToolCallEnvelope, ToolCallLifecycleFacts, ToolCallSpecReaderTable, ToolCallSpecVariant, ToolFailureResult, UnparsedToolResult } from '../../../model/toolCall'
import type { ToolKind } from '../../../model/toolKind'
import type { ToolRequestByKind } from '../../../model/tools'
import type { WebSearchLink } from '../../../model/tools/webSearch'
import type { ToolRequestOverrides } from '../../defaultToolRequests'
import type { OhMyPiTodoSource } from './todo'
import type { OhMyPiToolExecution } from './toolCommon'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ImageResultSource } from '~/lib/imageBlocks'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { OH_MY_PI_EVENT, OH_MY_PI_FRAME_FIELD, OH_MY_PI_TOOL } from '~/generated/contracts/ohmypi-protocol'
import { asContentArray, splitToolResultContent } from '~/lib/contentBlocks'
import { withFallbackFilePath } from '~/lib/imageBlocks'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { createToolCall } from '../../../model/createToolCall'
import { parseMcpContentItem } from '../../../model/mcpToolCall'
import { failedResult, proseResult, readToolCallSpec, unparsedResult } from '../../../model/toolCall'
import { deriveToolCallStatus } from '../../../model/toolCallLifecycle'
import { DEFAULT_TOOL_REQUESTS, toolRequestFor } from '../../defaultToolRequests'
import { retainedOutcome, retainedRowIsFinal } from '../../registry'
import { OH_MY_PI_DEVICE_SCHEME, OH_MY_PI_MCP_TOOL_PREFIX, OH_MY_PI_READ_KIND_URL, OH_MY_PI_YIELD_STATUS_ABORTED } from '../protocol'
import { ohMyPiToolKind } from '../toolKinds'
import { ohMyPiAgentRequest, ohMyPiAgentRuns } from './agent'
import { ohMyPiCommandOutcome, ohMyPiEvalRequest, ohMyPiEvalResults } from './execute'
import { ohMyPiPatchChanges, ohMyPiResultChanges, ohMyPiWriteChange } from './fileEdit'
import { ohMyPiHubIsProcessOp, ohMyPiHubProcessRequest } from './hub'
import { ohMyPiQuestionAnswers, ohMyPiQuestionPrompts } from './question'
import { ohMyPiReadIsUrl, ohMyPiReadResult } from './read'
import { ohMyPiGlobResult, ohMyPiGrepResult, ohMyPiSearchPattern, ohMyPiTextSearchResult } from './search'
import { ohMyPiTodoSource } from './todo'
import { ohMyPiExtractTool, ohMyPiPairedRequest, ohMyPiPairedResult } from './toolCommon'

/** The display name of a tool whose wire name is not the word a reader wants. */
const OH_MY_PI_TOOL_LABELS: ReadonlyMap<string, string> = new Map<string, string>([
  [OH_MY_PI_TOOL.Read, 'Read'],
  [OH_MY_PI_TOOL.Bash, 'Bash'],
  [OH_MY_PI_TOOL.Eval, 'Eval'],
  [OH_MY_PI_TOOL.Edit, 'Edit'],
  [OH_MY_PI_TOOL.ApplyPatch, 'Apply Patch'],
  [OH_MY_PI_TOOL.Write, 'Write'],
  [OH_MY_PI_TOOL.Glob, 'Glob'],
  [OH_MY_PI_TOOL.Grep, 'Grep'],
  [OH_MY_PI_TOOL.Find, 'Find'],
  [OH_MY_PI_TOOL.AstGrep, 'AST Grep'],
  [OH_MY_PI_TOOL.Task, 'Task'],
  [OH_MY_PI_TOOL.Hub, 'Hub'],
  [OH_MY_PI_TOOL.Todo, 'Todo'],
  [OH_MY_PI_TOOL.WebSearch, 'Web Search'],
  [OH_MY_PI_TOOL.Ask, 'Ask'],
  [OH_MY_PI_TOOL.Yield, 'Yield'],
  [OH_MY_PI_TOOL.Goal, 'Goal'],
  [OH_MY_PI_TOOL.Think, 'Think'],
])

/**
 * One omp tool call, resolved from its row and the two frames that describe it.
 *
 * omp's `tool_execution_end` carries no arguments, so a result row reads them from the
 * paired `tool_execution_start`.
 */
export interface OhMyPiToolRow {
  payload: Record<string, unknown>
  tool: OhMyPiToolExecution
  /** The paired `tool_execution_start`, or undefined when the store resolved none. */
  request: ParsedMessageContent | undefined
  /** The paired `tool_execution_end`, or undefined when the store resolved none. */
  result: ParsedMessageContent | undefined
  /** True when this row is the last one of its call. */
  finished: boolean
}

/** Build one tool row, or null for a row that is not an omp tool frame. */
export function ohMyPiToolRow(
  parsed: unknown,
  request: ParsedMessageContent | undefined,
  result: ParsedMessageContent | undefined,
  completion?: MessageCompletion,
): OhMyPiToolRow | null {
  if (!isObject(parsed))
    return null
  const tool = ohMyPiExtractTool(parsed)
  if (!tool)
    return null
  // A retained `tool_execution_start` is the call's END: the turn stopped before omp
  // sent the end frame, so this frame is the last one there is.
  const finished = pickString(parsed, 'type') === OH_MY_PI_EVENT.ToolExecutionEnd || retainedRowIsFinal(completion)
  return { payload: parsed, tool, request, result, finished }
}

/**
 * Which SIDE of its span one row draws: the request while the call runs, the answer
 * once it finished.
 */
export function ohMyPiToolSpanRowRole(row: OhMyPiToolRow): ToolSpanRowRole {
  return row.finished ? 'result' : 'request'
}

/**
 * Everything one payload decision reads, collected ONCE for the row.
 *
 * `isError` and the row's status answer two different questions. `isError` is omp's
 * own flag on this frame. The status also carries LeapMux's own completion: a turn that
 * ended while the call ran leaves no end frame, so its outcome lives in the completion
 * column alone. Every decision below reads `isError`; the lifecycle derives the status.
 */
export interface OhMyPiToolFacts {
  payload: Record<string, unknown>
  request: ParsedMessageContent | undefined
  result: ParsedMessageContent | undefined
  toolName: string
  label: string | undefined
  /** The call's arguments, which only the start frame carries. */
  args: Record<string, unknown>
  /**
   * The short reason the model gave for the call, which only the start frame carries,
   * or '' when omp states none.
   */
  intent: string
  /** The text the call returned, or the partial text a stopped turn left behind. */
  text: string
  /** The `details` of this frame's own result, or of its partial result. */
  details: Record<string, unknown>
  /** The raw content blocks of the result, which a generic card draws. */
  contentBlocks: unknown[]
  /** The pictures the result carries. */
  images: ImageResultSource[]
  /** The checklist a `todo` call states, or null for every other tool. */
  todo: OhMyPiTodoSource | null
  finished: boolean
  /** omp flagged THIS frame as an error. This is not the row's status. */
  isError: boolean
  lifecycle: ToolCallLifecycleFacts
  /** Whether omp supplied a result on this frame or on its paired end frame. */
  resultAvailable: boolean
}

/** The result record one frame carries: its final result, else its partial one. */
function frameResult(payload: Record<string, unknown> | undefined): Record<string, unknown> | undefined {
  return pickObject(payload, OH_MY_PI_FRAME_FIELD.Result) ?? pickObject(payload, OH_MY_PI_FRAME_FIELD.PartialResult) ?? undefined
}

/** Collect everything the payload decisions read, in one pass. */
export function ohMyPiToolFacts(row: OhMyPiToolRow, completion: MessageCompletion | undefined): OhMyPiToolFacts {
  const toolName = row.tool.toolName
  const pairedStart = ohMyPiPairedRequest(row.payload, row.request)?.parentObject
  const pairedEnd = ohMyPiPairedResult(row.payload, row.result)?.parentObject
  const pairedEndTool = ohMyPiExtractTool(pairedEnd)
  // The arguments live on the start frame alone. The frame's OWN arguments win where it
  // carries any.
  const args = Object.keys(row.tool.args).length > 0 ? row.tool.args : pickObject(pairedStart, OH_MY_PI_FRAME_FIELD.Args) ?? {}
  const intent = (row.tool.intent || ohMyPiExtractTool(pairedStart)?.intent || '').trim()
  // A start row whose end already landed reads the end's result, so the running call's
  // card and the finished call's card state one answer.
  const own = row.tool.result ?? row.tool.partialResult
  const source = own ?? pairedEndTool?.result
  const rawResult = frameResult(row.payload) ?? frameResult(pairedEnd)
  const blocks = asContentArray(rawResult?.content) ?? []
  const filePath = pickString(args, 'path')
  const images = splitToolResultContent(blocks, { text: 'text' }).images.map(image => withFallbackFilePath(image, filePath || undefined))
  const isError = row.tool.isError || (own === undefined && pairedEndTool?.isError === true)
  const details = source?.details ?? {}
  return {
    payload: row.payload,
    request: row.request,
    result: row.result,
    toolName,
    label: OH_MY_PI_TOOL_LABELS.get(toolName) ?? (toolName || undefined),
    args,
    intent,
    text: source?.text ?? '',
    details,
    contentBlocks: blocks,
    images,
    todo: toolName === OH_MY_PI_TOOL.Todo && !isError ? ohMyPiTodoSource(args, source ? details : undefined) : null,
    finished: row.finished,
    isError,
    lifecycle: {
      frameStatus: 'unstated',
      providerOutcome: isError ? 'failed' : null,
      retainedOutcome: retainedOutcome(completion),
      rowFinal: row.finished,
      resultFrameLanded: pickString(row.payload, 'type') === OH_MY_PI_EVENT.ToolExecutionEnd || pairedEnd !== undefined,
    },
    resultAvailable: source !== undefined,
  }
}

/**
 * The kind one row draws with, after the corrections the tool NAME cannot make.
 *
 * A NAMED step, and the only place a kind changes, so a call draws one card from its
 * first row to its last.
 */
export function ohMyPiReclassify(facts: OhMyPiToolFacts): ToolKind {
  const declared = ohMyPiToolKind(facts.toolName)
  // A tool the table does not list: an extension tool, a Model Context Protocol tool,
  // or a tool of a later omp. Each answers with content blocks, so it takes the generic
  // card rather than the uncategorized kind, whose wrench states nothing that ran.
  if (declared === 'unspecified')
    return 'mcp'
  // `write` to an `xd://` path runs a DEVICE tool -- the language server, the debugger
  // -- with its JSON arguments as the content. It is not a file write.
  if (declared === 'write' && pickString(facts.args, 'path').startsWith(OH_MY_PI_DEVICE_SCHEME))
    return 'mcp'
  // `read` of a URL fetches a web page, whether the argument or the result says so.
  if (declared === 'read' && (/^https?:\/\//i.test(pickString(facts.args, 'path')) || ohMyPiReadIsUrl(facts.details, OH_MY_PI_READ_KIND_URL)))
    return 'fetch'
  // `hub` supervises project processes as well as it messages agents. A process
  // operation runs a command, or acts on one that runs, so it draws as a command.
  if (facts.toolName === OH_MY_PI_TOOL.Hub && ohMyPiHubIsProcessOp(facts.args))
    return 'execute'
  return declared
}

/**
 * The kinds omp reads from its OWN facts. Every other kind takes the shared
 * `DEFAULT_TOOL_REQUESTS` entry, which reads the arguments alone.
 *
 * `read`, `grep`, `glob` and `think` are absent on purpose: omp spells their arguments
 * the way the shared table reads them.
 */
export const OH_MY_PI_TOOL_REQUEST_OVERRIDES: ToolRequestOverrides<OhMyPiToolFacts> = {
  // The tasks and their shared context, which omp states as a list the shared entry
  // does not read.
  agent: (args): ToolRequestByKind['agent'] => ohMyPiAgentRequest(args),
  // A hashline or apply-patch patch in `input`, which states each file in its own
  // header. The `replace` mode's `{path, old_string, new_string}` is what the shared
  // entry reads, so it answers for that mode.
  edit: (args): ToolRequestByKind['edit'] => {
    const changes = ohMyPiPatchChanges(args)
    return changes !== null ? { changes } : DEFAULT_TOOL_REQUESTS.edit(args)
  },
  // omp states the written text as `content`, which is none of the shared spellings.
  write: (args): ToolRequestByKind['write'] => {
    const change = ohMyPiWriteChange(args, '')
    return { changes: change ? [change] : [] }
  },
  // `eval` states its code and its language rather than a command, and a process
  // operation of `hub` states an application or a process; `bash` states a command,
  // which the shared entry reads.
  execute: (args, facts): ToolRequestByKind['execute'] => {
    if (facts.toolName === OH_MY_PI_TOOL.Eval)
      return ohMyPiEvalRequest(args)
    if (facts.toolName === OH_MY_PI_TOOL.Hub)
      return ohMyPiHubProcessRequest(args)
    return DEFAULT_TOOL_REQUESTS.execute(args)
  },
  // `find` states a query, and `ast_grep` a pattern; the shared entry reads the
  // pattern and the paths under the same keys, so this only supplies the query word.
  search: (args): ToolRequestByKind['search'] => ({ pattern: ohMyPiSearchPattern(args), paths: DEFAULT_TOOL_REQUESTS.search(args).paths }),
  // The hub states an operation, a recipient and a message under its own keys. A
  // process operation draws as a command instead (`ohMyPiReclassify`).
  message: (args): ToolRequestByKind['message'] => {
    const to = pickString(args, 'to')
    const op = pickString(args, 'op')
    return {
      ...(to ? { to } : {}),
      text: pickString(args, 'message') || pickString(args, 'text'),
      ...(op ? { summary: op } : {}),
    }
  },
  // omp's own question record, which no other provider sends.
  question: (args): ToolRequestByKind['question'] => ({ questions: ohMyPiQuestionPrompts(args.questions) }),
  // The checklist the result states. omp puts the whole list in the RESULT, so a
  // running call states none yet.
  todo: (_args, facts): ToolRequestByKind['todo'] => ({ items: facts.todo?.items ?? [], ...(facts.todo?.op ? { note: facts.todo.op } : {}) }),
  // The goal and the yield state a payload rather than a report of their own.
  report: (args): ToolRequestByKind['report'] => (Object.keys(args).length > 0 ? { payload: args } : {}),
  // The server and the tool. omp spells a Model Context Protocol tool
  // `mcp__<server>_<tool>`, and a server name can itself hold `_`, so the name does
  // not split reliably: the tool keeps the whole name after the prefix.
  mcp: (args, facts): ToolRequestByKind['mcp'] => ({
    server: '',
    tool: facts.toolName.startsWith(OH_MY_PI_MCP_TOOL_PREFIX) ? facts.toolName.slice(OH_MY_PI_MCP_TOOL_PREFIX.length) : facts.toolName,
    args,
  }),
}

/** One kind's declared request: omp's own reading where it states one, the shared table's elsewhere. */
function requestFor<K extends ToolKind>(kind: K, facts: OhMyPiToolFacts): ToolRequestByKind[K] {
  return toolRequestFor(kind, facts.args, facts, OH_MY_PI_TOOL_REQUEST_OVERRIDES)
}

/** The tool's display name and the words above it. */
function header(facts: OhMyPiToolFacts): { label?: string, title: string } {
  return { ...(facts.label !== undefined ? { label: facts.label } : {}), title: facts.label ?? 'Tool' }
}

/**
 * The words a finished call printed, when no reader of its kind could read them.
 *
 * A failed row carries the failure brand, never the unparsed one: the unparsed brand
 * states that the call COMPLETED and this build could not read the payload. No words
 * state no result at all.
 */
function unreadResult(facts: OhMyPiToolFacts): UnparsedToolResult | ToolFailureResult | undefined {
  if (!facts.text)
    return undefined
  return deriveToolCallStatus(facts.lifecycle, facts.resultAvailable) === 'failed' ? failedResult(facts.text) : unparsedResult(facts.text)
}

/** The declared payload of a kind omp never produces: its request, and no result. */
function declaredOnly<K extends ToolKind>(kind: K): (facts: OhMyPiToolFacts) => ToolCallSpecVariant<K> {
  return (facts): ToolCallSpecVariant<K> => ({ kind, ...header(facts), request: requestFor(kind, facts) })
}

/** The links and the summary one `web_search` result states in its details. */
function webSearchLinks(details: Record<string, unknown>): { links: WebSearchLink[], summary: string } {
  const response = pickObject(details, 'response')
  const sources = Array.isArray(response?.sources) ? response.sources.filter(isObject) : []
  const links = sources.flatMap((source) => {
    const url = pickString(source, 'url')
    return url ? [{ title: pickString(source, 'title') || url, url }] : []
  })
  return { links, summary: pickString(response, 'answer') }
}

/**
 * One reader for each kind, each checked against its OWN kind's request and result.
 *
 * Every entry declares its own return type, because a contextual signature is not an
 * annotated position: without it an entry takes a stray key without a word.
 */
export const OH_MY_PI_TOOL_READERS: ToolCallSpecReaderTable<OhMyPiToolFacts> = {
  agent: (facts): ToolCallSpecVariant<'agent'> => {
    const request = requestFor('agent', facts)
    // What the subagents were asked to do heads the row, because the tool name says
    // only that a subagent ran.
    const title = request.description
    if (!facts.resultAvailable)
      return { kind: 'agent', ...header(facts), title, request }
    if (facts.isError)
      return { kind: 'agent', ...header(facts), title, request, result: failedResult(facts.text) }
    const agents = ohMyPiAgentRuns(facts.details, facts.text)
    if (agents.length > 0)
      return { kind: 'agent', ...header(facts), title, request, result: { agents } }
    const unread = unreadResult(facts)
    return { kind: 'agent', ...header(facts), title, request, ...(unread !== undefined ? { result: unread } : {}) }
  },
  execute: (facts): ToolCallSpecVariant<'execute'> => {
    const request = requestFor('execute', facts)
    // No title: the command is the header on every surface, and a title would put
    // `Bash` above the very command it ran.
    const label = facts.label
    if (!facts.resultAvailable)
      return { kind: 'execute', ...(label !== undefined ? { label } : {}), request }
    if (facts.toolName === OH_MY_PI_TOOL.Eval) {
      const cells = ohMyPiEvalResults(facts.details)
      if (cells)
        return { kind: 'execute', ...(label !== undefined ? { label } : {}), request, result: { commands: cells, unresolvedTerminals: [] } }
    }
    // A process operation of `hub` reports in omp's words, and a process it acts on
    // states no exit code of the operation itself.
    if (facts.toolName === OH_MY_PI_TOOL.Hub)
      return { kind: 'execute', ...(label !== undefined ? { label } : {}), request, result: { commands: [{ output: facts.text }], unresolvedTerminals: [] } }
    // A command omp moved to the background has not ended: its output arrives later,
    // and a SHELL row follows it. The row states what omp said, and no exit code.
    const background = isObject(facts.details.async)
    const ended = facts.lifecycle.resultFrameLanded && !facts.isError && !background
    const outcome = ohMyPiCommandOutcome(facts.text, facts.details, ended)
    return {
      kind: 'execute',
      ...(label !== undefined ? { label } : {}),
      request,
      result: { commands: [outcome.result], unresolvedTerminals: [] },
      ...(outcome.cancelled ? { statusOverride: 'cancelled' as const } : {}),
    }
  },
  read: (facts): ToolCallSpecVariant<'read'> => {
    const request = requestFor('read', facts)
    const head = header(facts)
    if (!facts.resultAvailable)
      return { kind: 'read', ...head, request }
    if (facts.isError)
      return { kind: 'read', ...head, request, result: failedResult(facts.text) }
    return { kind: 'read', ...head, request, result: ohMyPiReadResult(facts.text, facts.details), images: facts.images }
  },
  fetch: (facts): ToolCallSpecVariant<'fetch'> => {
    const request: ToolRequestByKind['fetch'] = { url: pickString(facts.args, 'path') || pickString(facts.details, 'url') }
    if (!facts.resultAvailable)
      return { kind: 'fetch', ...header(facts), title: 'Fetch', request }
    if (facts.isError)
      return { kind: 'fetch', ...header(facts), title: 'Fetch', request, result: failedResult(facts.text) }
    return { kind: 'fetch', ...header(facts), title: 'Fetch', request, result: { result: facts.text } }
  },
  edit: (facts): ToolCallSpecVariant<'edit'> => {
    const request = requestFor('edit', facts)
    const head = header(facts)
    if (!facts.resultAvailable)
      return { kind: 'edit', ...head, request }
    if (facts.isError)
      return { kind: 'edit', ...head, request, result: failedResult(facts.text) }
    const changes = ohMyPiResultChanges(facts.details, request.changes[0]?.filePath ?? '')
    if (changes.length > 0)
      return { kind: 'edit', ...head, request, result: { changes } }
    const unread = unreadResult(facts)
    return { kind: 'edit', ...head, request, ...(unread !== undefined ? { result: unread } : {}) }
  },
  write: (facts): ToolCallSpecVariant<'write'> => {
    const request = requestFor('write', facts)
    const head = header(facts)
    if (!facts.resultAvailable)
      return { kind: 'write', ...head, request }
    if (facts.isError)
      return { kind: 'write', ...head, request, result: failedResult(facts.text) }
    const change = ohMyPiWriteChange(facts.args, pickString(facts.details, 'resolvedPath'))
    return { kind: 'write', ...head, request, ...(change ? { result: { changes: [change] } } : {}) }
  },
  grep: (facts): ToolCallSpecVariant<'grep'> => {
    const request = requestFor('grep', facts)
    const head = header(facts)
    if (!facts.resultAvailable)
      return { kind: 'grep', ...head, request }
    if (facts.isError)
      return { kind: 'grep', ...head, request, result: failedResult(facts.text) }
    return { kind: 'grep', ...head, request, result: ohMyPiGrepResult(facts.text, facts.details) }
  },
  glob: (facts): ToolCallSpecVariant<'glob'> => {
    const request = requestFor('glob', facts)
    const head = header(facts)
    if (!facts.resultAvailable)
      return { kind: 'glob', ...head, request }
    if (facts.isError)
      return { kind: 'glob', ...head, request, result: failedResult(facts.text) }
    return { kind: 'glob', ...head, request, result: ohMyPiGlobResult(facts.text, facts.details) }
  },
  search: (facts): ToolCallSpecVariant<'search'> => {
    const request = requestFor('search', facts)
    const head = header(facts)
    if (!facts.resultAvailable)
      return { kind: 'search', ...head, request }
    if (facts.isError)
      return { kind: 'search', ...head, request, result: failedResult(facts.text) }
    return { kind: 'search', ...head, request, result: ohMyPiTextSearchResult(facts.text, facts.details) }
  },
  message: (facts): ToolCallSpecVariant<'message'> => {
    const request = requestFor('message', facts)
    const head = header(facts)
    if (!facts.resultAvailable)
      return { kind: 'message', ...head, request }
    return { kind: 'message', ...head, request, result: facts.isError ? failedResult(facts.text) : proseResult(facts.text) }
  },
  todo: (facts): ToolCallSpecVariant<'todo'> => {
    const request = requestFor('todo', facts)
    // No title: the checklist heads itself with its own count, as every provider's does.
    const label = facts.label
    if (!facts.resultAvailable)
      return { kind: 'todo', ...(label !== undefined ? { label } : {}), request }
    if (facts.isError)
      return { kind: 'todo', ...(label !== undefined ? { label } : {}), request, result: failedResult(facts.text) }
    // A result that states no list is one this build cannot read, and the row draws
    // omp's own words.
    if (!facts.todo) {
      const unread = unreadResult(facts)
      return { kind: 'todo', ...(label !== undefined ? { label } : {}), request, ...(unread !== undefined ? { result: unread } : {}) }
    }
    return {
      kind: 'todo',
      ...(label !== undefined ? { label } : {}),
      request,
      result: { items: request.items, ...(request.note !== undefined ? { note: request.note } : {}) },
    }
  },
  web_search: (facts): ToolCallSpecVariant<'web_search'> => {
    const request = requestFor('web_search', facts)
    const head = header(facts)
    if (!facts.resultAvailable)
      return { kind: 'web_search', ...head, request }
    if (facts.isError)
      return { kind: 'web_search', ...head, request, result: failedResult(facts.text) }
    const { links, summary } = webSearchLinks(facts.details)
    return { kind: 'web_search', ...head, request, result: { links, summary: summary || facts.text } }
  },
  question: (facts): ToolCallSpecVariant<'question'> => {
    const request = requestFor('question', facts)
    const first = request.questions[0]
    // One question heads the row with its own words; several keep the tool's name.
    const title = request.questions.length === 1 && first ? first.question : header(facts).title
    if (!facts.resultAvailable)
      return { kind: 'question', ...header(facts), title, request }
    if (facts.isError)
      return { kind: 'question', ...header(facts), title, request, result: failedResult(facts.text) }
    const answers = ohMyPiQuestionAnswers(facts.details)
    return {
      kind: 'question',
      ...header(facts),
      title,
      request,
      result: { answers: answers ?? [{ header: first?.header || first?.question || 'Answer', answer: facts.text || null }] },
    }
  },
  report: (facts): ToolCallSpecVariant<'report'> => {
    const request = requestFor('report', facts)
    const head = header(facts)
    if (!facts.resultAvailable)
      return { kind: 'report', ...head, request }
    // A subagent that gives up yields an `error`. omp answers that call with a result
    // it does NOT flag as an error, and states `status: "aborted"` in its details
    // (`tools/yield.ts`), so the status is what states the failure.
    if (facts.toolName === OH_MY_PI_TOOL.Yield && pickString(facts.details, 'status') === OH_MY_PI_YIELD_STATUS_ABORTED)
      return { kind: 'report', ...head, request, result: failedResult(pickString(facts.details, 'error') || facts.text), statusOverride: 'failed' }
    return { kind: 'report', ...head, request, result: facts.isError ? failedResult(facts.text) : proseResult(facts.text, 'markdown') }
  },
  think: (facts): ToolCallSpecVariant<'think'> => {
    const request = requestFor('think', facts)
    const head = header(facts)
    if (!facts.resultAvailable)
      return { kind: 'think', ...head, request }
    return { kind: 'think', ...head, request, result: facts.isError ? failedResult(facts.text) : proseResult(facts.text, 'markdown') }
  },
  mcp: (facts): ToolCallSpecVariant<'mcp'> => {
    const request = requestFor('mcp', facts)
    const head = header(facts)
    // NO result until the call finishes: the row draws the live output the worker
    // broadcasts only while the call runs AND its result is absent.
    if (!facts.resultAvailable || !facts.finished)
      return { kind: 'mcp', ...head, request }
    const content = facts.contentBlocks.map(parseMcpContentItem)
    return {
      kind: 'mcp',
      ...head,
      request,
      result: {
        content,
        ...(facts.isError ? { error: facts.text } : {}),
      },
    }
  },
  // The kinds omp states no tool for. Each still declares its own request, so the day
  // one of them arrives it draws its own card rather than a dump.
  unspecified: declaredOnly('unspecified'),
  agents: declaredOnly('agents'),
  chart: declaredOnly('chart'),
  delete: declaredOnly('delete'),
  image: declaredOnly('image'),
  list: declaredOnly('list'),
  memory: declaredOnly('memory'),
  move: declaredOnly('move'),
  other: declaredOnly('other'),
  skill: declaredOnly('skill'),
  switch_mode: declaredOnly('switch_mode'),
  task: declaredOnly('task'),
  trigger: declaredOnly('trigger'),
  wait: declaredOnly('wait'),
}

/**
 * One omp tool call, as the kind-discriminated pair.
 *
 * The intent the model gave for the call rides beside every kind, because omp states
 * it for every tool when intent tracing is on, which is its default.
 */
export function ohMyPiToolCall(row: OhMyPiToolRow, completion?: MessageCompletion): ToolCall {
  const facts = ohMyPiToolFacts(row, completion)
  const envelope: ToolCallEnvelope = { id: row.tool.toolCallId, name: facts.toolName, lifecycle: facts.lifecycle }
  const spec = readToolCallSpec(OH_MY_PI_TOOL_READERS, ohMyPiReclassify(facts), facts)
  return createToolCall(envelope, facts.intent ? { ...spec, metadata: [...(spec.metadata ?? []), { label: 'Intent', value: facts.intent }] } : spec)
}
