import type { ToolSpanRowRole } from '../../../model/row'
import type { ToolCall, ToolCallEnvelope, ToolCallLifecycleFacts, ToolCallSpecReaderTable, ToolCallSpecVariant, ToolFailureResult, ToolResult, UnparsedToolResult } from '../../../model/toolCall'
import type { ToolKind } from '../../../model/toolKind'
import type { ToolRequestByKind } from '../../../model/tools'
import type { AgentRequest, AgentRun } from '../../../model/tools/agent'
import type { FileChangeRequest } from '../../../model/tools/fileChange'
import type { SearchRequest } from '../../../model/tools/search'
import type { ToolRequestOverrides } from '../../defaultToolRequests'
import type { PiTodoSource } from './todo'
import type { PiToolExecution } from './toolCommon'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ImageResultSource } from '~/lib/imageBlocks'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { PI_EVENT, PI_TOOL } from '~/generated/contracts/pi-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { createToolCall } from '../../../model/createToolCall'
import { mcpToolCallDisplayName } from '../../../model/mcpToolCall'
import { failedResult, proseResult, readToolCallSpec, unparsedResult } from '../../../model/toolCall'
import { deriveToolCallStatus } from '../../../model/toolCallLifecycle'
import { toolRequestFor } from '../../defaultToolRequests'
import { retainedOutcome, retainedRowIsFinal } from '../../registry'
import { PI_POWERSHELL_TOOL } from '../protocol'
import { piQuestionsFromArgs, piQuestionTitle } from '../questionSource'
import { piToolKind } from '../toolKinds'
import { piAgentRequest, piAgentResult } from './agent'
import { extractPiCommand, piCommandResult } from './execute'
import { extractPiRead, piFallbackDiffSources, piResolveDiffSources, resolvePiResultDiff } from './fileEdit'
import { piGenericToolSource, piMcpIdentity } from './generic'
import { piToolResultImages } from './image'
import { extractPiSearch } from './search'
import { piTodoSource } from './todo'
import { piExtractTool, piPairedRequest } from './toolCommon'
import { piWorkflowRequest, piWorkflowResult } from './workflow'

/** The display name of a tool whose wire name is not the word a reader wants. */
const PI_TOOL_LABELS: ReadonlyMap<string, string> = new Map<string, string>([
  [PI_TOOL.Bash, 'Bash'],
  [PI_POWERSHELL_TOOL, 'PowerShell'],
  [PI_TOOL.Read, 'Read'],
  [PI_TOOL.Write, 'Write'],
  [PI_TOOL.Edit, 'Edit'],
])

/**
 * Read one tool call from its frame and the two events that describe it.
 *
 * Pi sends arguments only on tool_execution_start. A result row uses that exact paired request.
 * Resolve the to-do source once. Both its body and outcome need the same result.
 * A to-do failure can appear in details.error while Pi reports isError: false.
 */
export interface PiToolRow {
  payload: Record<string, unknown>
  tool: PiToolExecution
  /** The paired `tool_execution_start`, or undefined when the store resolved none. */
  request: ParsedMessageContent | undefined
  /** The paired `tool_execution_end`, or undefined when the store resolved none. */
  result: ParsedMessageContent | undefined
  /** The resolved checklist of a to-do call, or null for every other tool. */
  todo: PiTodoSource | null
  /** True when this row is the last one of its call. */
  finished: boolean
  /** The call failed, whether Pi flagged it or stated it in its own fields. */
  isError: boolean
}

/** Build one tool row, or null for a row that is not a Pi tool call. */
export function piToolRow(
  parsed: unknown,
  request: ParsedMessageContent | undefined,
  result: ParsedMessageContent | undefined,
  completion?: MessageCompletion,
): PiToolRow | null {
  if (!isObject(parsed))
    return null
  const tool = piExtractTool(parsed)
  if (!tool)
    return null
  // A retained start frame is the last row when the turn stops before native completion.
  const finished = pickString(parsed, 'type') === PI_EVENT.ToolExecutionEnd || retainedRowIsFinal(completion)
  const todo = tool.toolName === PI_TOOL.Todo ? piTodoSource(parsed, request, result) : null
  return { payload: parsed, tool, request, result, todo, finished, isError: tool.isError || !!todo?.error }
}

/**
 * Read the request side while a call runs. Read the result side when the call finishes.
 *
 * A retained start frame counts as a finished result side.
 * Its turn ended before Pi sent a completion event, so no later frame can replace it.
 */
export function piToolSpanRowRole(row: PiToolRow): ToolSpanRowRole {
  return row.finished ? 'result' : 'request'
}

/**
 * Collect the facts that each Pi tool reader needs.
 *
 * Each reader uses one resolved set of facts. It does not recover fields independently after kind selection.
 * This keeps request and result decisions consistent.
 *
 * isError reports Pi's flag on the current frame. The derived status also includes LeapMux's retained completion.
 * A retained start frame can therefore fail without a native error flag.
 *
 * Do not use the derived failed status to select an edit's native error branch.
 * That branch reads Pi's error text. A retained start frame carries no native error text,
 * so it must preserve the requested edits instead of an empty failure reason.
 * The MCP reader uses the derived status only to avoid a redundant status override.
 */
export interface PiToolFacts {
  /** The frame this row was read from. The per-tool extractors take it whole. */
  payload: Record<string, unknown>
  /** The paired `tool_execution_start`, or undefined when the store resolved none. */
  request: ParsedMessageContent | undefined
  /** The paired `tool_execution_end`, or undefined when the store resolved none. */
  result: ParsedMessageContent | undefined
  /** Pi's own tool name, which is all Pi states about which tool ran. */
  toolName: string
  /** The tool's display name: the table's word, else the wire name. */
  label: string | undefined
  /** The arguments of the call, which only the opening event carries. */
  args: Record<string, unknown>
  /** The text the call returned, or the partial text a stopped turn left behind. */
  text: string
  /** The `details` record of THIS frame's own result. The `switch_mode` entry reads the plan from it. */
  details: Record<string, unknown> | undefined
  /** The resolved checklist of a to-do call, or null for every other tool. */
  todo: PiTodoSource | null
  /** The pictures the result carries. The `read` and `mcp` entries state them. */
  images: ImageResultSource[]
  /** True when this row is the last one of its call. */
  finished: boolean
  /** Pi flagged THIS frame as an error. Read the note above: this is not a status. */
  isError: boolean
  /** Raw lifecycle facts. The shared derivation decides their precedence. */
  lifecycle: ToolCallLifecycleFacts
  /** Whether Pi supplied a result body on this frame or its resolved result side. */
  resultAvailable: boolean
}

/** Collect everything the payload decisions read, in one pass. */
export function piToolFacts(row: PiToolRow, completion: MessageCompletion | undefined): PiToolFacts {
  const toolName = row.tool.toolName
  const pairedResult = piExtractTool(row.result?.parentObject)
  // Result rows read arguments from the exact paired opening event.
  // Arguments on this frame take precedence when present.
  const paired = pickObject(piPairedRequest(row.payload, row.request)?.parentObject, 'args')
  return {
    payload: row.payload,
    request: row.request,
    result: row.result,
    toolName,
    label: PI_TOOL_LABELS.get(toolName) ?? (toolName || undefined),
    args: Object.keys(row.tool.args).length > 0 ? row.tool.args : paired ?? {},
    text: row.tool.result?.text ?? row.tool.partialResult?.text ?? '',
    details: row.tool.result?.details,
    todo: row.todo,
    images: piToolResultImages(row.payload, undefined, row.request),
    finished: row.finished,
    isError: row.tool.isError,
    lifecycle: {
      frameStatus: 'unstated',
      providerOutcome: row.isError ? 'failed' : null,
      retainedOutcome: retainedOutcome(completion),
      rowFinal: row.finished,
      resultFrameLanded: pickString(row.payload, 'type') === PI_EVENT.ToolExecutionEnd || row.result !== undefined,
    },
    resultAvailable: row.tool.result !== undefined
      || row.tool.partialResult !== undefined
      || pairedResult?.result !== undefined
      || pairedResult?.partialResult !== undefined,
  }
}

/**
 * Select the row kind before any reader builds its payload.
 *
 * This is the only step that changes a declared kind.
 * An unknown tool needs the rich-content MCP card. An invalid to-do result needs that card also.
 * Do not delay either change until completion. One call must keep one card kind while it runs and after it ends.
 *
 * Export this step so the tool-call tests can check each change and unchanged kind.
 */
export function piReclassify(facts: PiToolFacts): ToolKind {
  const declared = piToolKind(facts.toolName)
  // Unknown extensions and MCP bridges return content blocks.
  // The generic card displays those blocks with the native tool identity.
  if (declared === 'unspecified')
    return 'mcp'
  // An unsupported or invalid to-do checklist still retains its native content blocks.
  // The generic card can display those blocks without inventing a checklist.
  if (declared === 'todo' && !facts.todo)
    return 'mcp'
  return declared
}

/**
 * Override shared request readers only when Pi's native facts require another interpretation.
 *
 * All other kinds use DEFAULT_TOOL_REQUESTS, which reads the arguments alone.
 * The read and search kinds use that shared table. switch_mode uses it also.
 * Pi spells those arguments in the common format.
 *
 * Question tools remain here because piQuestionsFromArgs reads a Pi-specific record.
 * Each entry declares its own return type to enforce excess-property checks.
 * PI_TOOL_READERS explains that TypeScript requirement.
 */
export const PI_TOOL_REQUEST_OVERRIDES: ToolRequestOverrides<PiToolFacts> = {
  // Pi describes an agent launch across this frame and its paired events.
  // The shared request reader sees only arguments.
  agent: (_args, facts): ToolRequestByKind['agent'] => piRowAgentRequest(facts),
  // Read substitutions from the opening event.
  // Pi does not repeat them in the result arguments.
  // The shared reader cannot identify the requested changes from that result alone.
  edit: (_args, facts): ToolRequestByKind['edit'] => piFileChangeRequest(facts),
  write: (_args, facts): ToolRequestByKind['write'] => piFileChangeRequest(facts),
  // Pi sends the same argument shape for PowerShell and Bash.
  // Its tool name selects the command language.
  execute: (args, facts): ToolRequestByKind['execute'] => {
    // pickString returns an empty string for an absent field.
    // An empty description leaves the command as the header source.
    const description = pickString(args, 'description') || undefined
    return {
      command: pickString(args, 'command'),
      ...(facts.toolName === PI_POWERSHELL_TOOL ? { language: 'powershell' as const } : {}),
      ...(description !== undefined ? { description } : {}),
    }
  },
  // Native result details supply the original server and tool. The arguments contain neither field.
  mcp: (args, facts): ToolRequestByKind['mcp'] => {
    const identity = piMcpIdentity(facts.payload, facts.result)
    return identity ? { server: identity.server, tool: identity.tool, args } : { server: '', tool: facts.toolName, args }
  },
  // All four Pi question tools use piQuestionsFromArgs.
  // The shared reader cannot interpret that native record.
  // Pi must retain each question and its options.
  question: (args): ToolRequestByKind['question'] => ({ questions: piQuestionsFromArgs(args) }),
  // Pi sends the checklist in its result.
  // Reuse the resolved checklist because the argument reader cannot supply it.
  todo: (_args, facts): ToolRequestByKind['todo'] => {
    const todo = facts.todo
    // Include the note only when a checklist resolves.
    // An absent note stays absent.
    return { items: todo?.list.todos ?? [], ...(todo ? { note: todo.description } : {}) }
  },
}

/** One kind's declared request: Pi's own reading where it states one, the shared table's elsewhere. */
function piRequestFor<K extends ToolKind>(kind: K, facts: PiToolFacts): ToolRequestByKind[K] {
  return toolRequestFor(kind, facts.args, facts, PI_TOOL_REQUEST_OVERRIDES)
}

/**
 * Read the tool display name and title for a shared header.
 *
 * A reader can replace the title with its own fields.
 * The execute reader uses no title because its command already supplies the header.
 */
function piHeader(facts: PiToolFacts): { label?: string, title: string } {
  // Include a label only when the tool table or native tool name supplies one.
  return { ...(facts.label !== undefined ? { label: facts.label } : {}), title: facts.label ?? 'Tool' }
}

/**
 * Build a declared request without a result for a kind that Pi does not produce.
 *
 * The reader table must cover every shared kind.
 * A new kind must retain its own request instead of an untyped dump of the arguments.
 */
function piDeclaredOnly<K extends ToolKind>(kind: K): (facts: PiToolFacts) => ToolCallSpecVariant<K> {
  // Declare the inner arrow return type explicitly.
  // A contextual signature does not preserve object-literal freshness.
  // The explicit type keeps excess-property checks active for each returned request.
  return (facts): ToolCallSpecVariant<K> => ({ kind, ...piHeader(facts), request: piRequestFor(kind, facts) })
}

/**
 * Read output text with the result brand that the derived status permits.
 *
 * The unparsed brand means a successful result whose format the parser cannot read.
 * A failed row must use the failure brand instead. The model rejects an unparsed failed result,
 * which would discard the tool kind and request.
 *
 * A retained failed turn can keep partial text without a native error flag.
 * Return no result when that text is empty. The row's status already explains the failure.
 */
function piUnreadResult(facts: PiToolFacts, text: string): UnparsedToolResult | ToolFailureResult | undefined {
  if (!text)
    return undefined
  if (deriveToolCallStatus(facts.lifecycle, facts.resultAvailable) !== 'failed')
    return unparsedResult(text)
  return failedResult(text)
}

/**
 * Read the request and result of an edit or write.
 *
 * Both kinds use the same file-change shape. Each table entry still declares its own literal kind.
 * A generic kind parameter would weaken the compiler's checks.
 */
function piFileChangeParts(kind: 'edit' | 'write', facts: PiToolFacts): { request: FileChangeRequest, result?: ToolResult<'edit' | 'write'> } {
  const request = piRequestFor(kind, facts)
  if (!facts.resultAvailable)
    return { request }
  // Select the error branch from Pi's native flag.
  // A retained failed turn can have no native error flag.
  // That row must preserve its requested substitutions.
  // Keep the request list for the file-specific title even when the failed result draws no diff.
  if (facts.isError)
    return { request, result: failedResult(facts.text) }
  const sources = piResolveDiffSources(facts.payload, facts.request)
  if (sources.length > 0)
    return { request, result: { changes: sources } }
  // Preserve unreadable diff text for display and Copy.
  // Empty text supplies no result body.
  const unread = piUnreadResult(facts, resolvePiResultDiff(facts.payload, facts.args).rawDiff || facts.text)
  return unread !== undefined ? { request, result: unread } : { request }
}

/** The search card's two halves. `grep` and `glob` declare the same pair, as above. */
function piSearchParts(kind: 'grep' | 'glob', facts: PiToolFacts): { request: SearchRequest, result?: ToolResult<'grep' | 'glob'> } {
  const request = piRequestFor(kind, facts)
  if (!facts.resultAvailable)
    return { request }
  if (facts.isError)
    return { request, result: failedResult(facts.text) }
  const search = extractPiSearch(facts.payload)
  if (search)
    return { request, result: search }
  const unread = piUnreadResult(facts, facts.text)
  return unread !== undefined ? { request, result: unread } : { request }
}

/**
 * Read each kind through its own declared request and result types.
 *
 * A condition can narrow a value without narrowing its generic type parameter.
 * A union-returning branch could therefore omit the request that its kind requires.
 * This mapped table checks the request and result at each kind.
 *
 * Every arrow must declare its return type. An inferred arrow return loses object-literal freshness
 * before the contextual signature checks it, so excess properties escape validation.
 * For example, an inferred read entry accepts an undeclared patchText field.
 * An explicit ToolCallSpecVariant<'read'> return rejects that field.
 *
 * Do not store a request in an unannotated variable before returning it.
 * That variable also loses freshness and bypasses the excess-property check.
 * toolTableEntriesAreAnnotated.test.ts checks both forms.
 *
 * Each kind owns its lifecycle decisions. Checklists and questions can show content before completion.
 * switch_mode keeps failure prose. execute keeps command output and the exit code.
 *
 * Export the table so tests can check its exact keys and result kinds.
 * Tests also check that every kind outside PI_TOOL_REQUEST_OVERRIDES uses the shared request reader.
 */
export const PI_TOOL_READERS: ToolCallSpecReaderTable<PiToolFacts> = {
  agent: (facts): ToolCallSpecVariant<'agent'> => {
    const request = piRequestFor('agent', facts)
    return {
      kind: 'agent',
      ...piHeader(facts),
      // Use the requested task as the header.
      // The tool name alone identifies no particular task.
      title: request.description,
      request,
      ...(facts.resultAvailable ? { result: { agents: [piAgentRun(facts)] } } : {}),
    }
  },
  todo: (facts): ToolCallSpecVariant<'todo'> => {
    const request = piRequestFor('todo', facts)
    const header = piHeader(facts)
    // Use the native checklist title or listing count.
    const title = facts.todo?.list.title ?? header.title
    const metadata = facts.todo?.metadata.length ? facts.todo.metadata : undefined
    // Pi can report a refused operation in details.error while its error flag stays false.
    // The resolved checklist supplies that failure reason.
    if (facts.todo?.error)
      return { kind: 'todo', ...header, title, ...(metadata !== undefined ? { metadata } : {}), request, result: failedResult(facts.todo.error) }
    // Use the same resolved items and note on both sides of the card.
    const emptyText = facts.todo?.list.emptyText
    return {
      kind: 'todo',
      ...header,
      title,
      ...(metadata !== undefined ? { metadata } : {}),
      request,
      ...(facts.resultAvailable
        ? {
            result: {
              items: request.items,
              ...(emptyText !== undefined ? { emptyText } : {}),
              ...(request.note !== undefined ? { note: request.note } : {}),
            },
          }
        : {}),
    }
  },
  execute: (facts): ToolCallSpecVariant<'execute'> => {
    const request = piRequestFor('execute', facts)
    const label = facts.label
    if (!facts.resultAvailable)
      return { kind: 'execute', ...(label !== undefined ? { label } : {}), request }
    const resolved = extractPiCommand(facts.payload)
    if (!resolved) {
      const unread = piUnreadResult(facts, facts.text)
      return { kind: 'execute', ...(label !== undefined ? { label } : {}), request, ...(unread !== undefined ? { result: unread } : {}) }
    }
    // Pi flags both stopped and timed-out commands as errors.
    // The marker parser distinguishes their native outcomes and preserves the explaining marker.
    return {
      kind: 'execute',
      ...(label !== undefined ? { label } : {}),
      request,
      result: { commands: [piCommandResult(resolved)], unresolvedTerminals: [] },
      ...(resolved.cancelled ? { statusOverride: 'cancelled' as const } : {}),
    }
  },
  read: (facts): ToolCallSpecVariant<'read'> => {
    const request = piRequestFor('read', facts)
    const header = piHeader(facts)
    if (!facts.resultAvailable)
      return { kind: 'read', ...header, request }
    if (facts.isError)
      return { kind: 'read', ...header, request, result: failedResult(facts.text) }
    const read = extractPiRead(facts.payload, facts.args)
    if (read)
      return { kind: 'read', ...header, request, result: read, images: facts.images }
    const unread = piUnreadResult(facts, facts.text)
    return { kind: 'read', ...header, request, ...(unread !== undefined ? { result: unread } : {}) }
  },
  edit: (facts): ToolCallSpecVariant<'edit'> => ({ kind: 'edit', ...piHeader(facts), ...piFileChangeParts('edit', facts) }),
  write: (facts): ToolCallSpecVariant<'write'> => ({ kind: 'write', ...piHeader(facts), ...piFileChangeParts('write', facts) }),
  grep: (facts): ToolCallSpecVariant<'grep'> => ({ kind: 'grep', ...piHeader(facts), ...piSearchParts('grep', facts) }),
  glob: (facts): ToolCallSpecVariant<'glob'> => ({ kind: 'glob', ...piHeader(facts), ...piSearchParts('glob', facts) }),
  list: (facts): ToolCallSpecVariant<'list'> => {
    const request = piRequestFor('list', facts)
    const header = piHeader(facts)
    if (!facts.resultAvailable)
      return { kind: 'list', ...header, request }
    if (facts.isError)
      return { kind: 'list', ...header, request, result: failedResult(facts.text) }
    // Pi's directory listing arrives as the search result's file paths.
    const search = extractPiSearch(facts.payload)
    if (search) {
      const notice = search.notice
      return { kind: 'list', ...header, request, result: { entries: search.filenames.map(path => ({ path })), truncated: search.truncated, ...(notice !== undefined ? { notice } : {}) } }
    }
    const unread = piUnreadResult(facts, facts.text)
    return { kind: 'list', ...header, request, ...(unread !== undefined ? { result: unread } : {}) }
  },
  question: (facts): ToolCallSpecVariant<'question'> => {
    const request = piRequestFor('question', facts)
    const header = piHeader(facts)
    const title = piQuestionTitle(request.questions) ?? header.title
    // Pi returns the chosen option as text.
    // Keep those exact words under the question's header.
    return {
      kind: 'question',
      ...header,
      title,
      request,
      ...(facts.resultAvailable && facts.text ? { result: { answers: [{ header: request.questions[0]?.header || title || 'Answer', answer: facts.text }] } } : {}),
    }
  },
  switch_mode: (facts): ToolCallSpecVariant<'switch_mode'> => {
    // Read the written plan from its native details record.
    const plan = pickString(facts.details, 'plan').trim()
    return {
      kind: 'switch_mode',
      ...piHeader(facts),
      request: piRequestFor('switch_mode', facts),
      ...(facts.resultAvailable ? { result: proseResult(plan || facts.text, 'markdown') } : {}),
      ...(facts.isError ? { statusOverride: 'failed' as const, result: failedResult(facts.text) } : {}),
    }
  },
  mcp: (facts): ToolCallSpecVariant<'mcp'> => {
    const request = piRequestFor('mcp', facts)
    const header = request.server ? { title: mcpToolCallDisplayName(request) } : piHeader(facts)
    // Attach a result only after the call finishes.
    // A running call needs an absent result so ToolMessage can display live output.
    // This rule covers Pi extensions and MCP bridges under the generic kind.
    // The tool vocabulary invariant and opening-frame tests enforce the same rule.
    if (!facts.resultAvailable)
      return { kind: 'mcp', ...header, request }
    // piToolRow refuses frames without a tool call before this reader runs.
    // Optional reads still account for the nullable helper return type.
    const source = piGenericToolSource(facts.payload, facts.request, facts.result)
    // Images remain in their content blocks.
    // A separate image list would duplicate every result image.
    const content = source?.content.length
      ? source.content
      : facts.images.map(image => ({ type: 'image' as const, source: image }))
    const structuredJson = source?.structuredJson
    const structuredJsonRole = source?.structuredJsonRole
    const error = source?.error
    const durationMs = source?.durationMs
    return {
      kind: 'mcp',
      ...header,
      request,
      result: {
        content,
        ...(structuredJson !== undefined ? { structuredJson } : {}),
        ...(structuredJson !== undefined && structuredJsonRole !== undefined ? { structuredJsonRole } : {}),
        ...(error !== undefined ? { error } : {}),
        ...(durationMs !== undefined ? { durationMs } : {}),
      },
      ...(source?.failed && deriveToolCallStatus(facts.lifecycle, facts.resultAvailable) !== 'failed' ? { statusOverride: 'failed' as const } : {}),
    }
  },
  // Keep declared requests for kinds that Pi does not currently produce.
  // A newly supported kind can then retain its own card and request.
  unspecified: piDeclaredOnly('unspecified'),
  agents: piDeclaredOnly('agents'),
  chart: piDeclaredOnly('chart'),
  delete: piDeclaredOnly('delete'),
  fetch: piDeclaredOnly('fetch'),
  image: piDeclaredOnly('image'),
  memory: piDeclaredOnly('memory'),
  message: piDeclaredOnly('message'),
  move: piDeclaredOnly('move'),
  other: piDeclaredOnly('other'),
  report: piDeclaredOnly('report'),
  search: piDeclaredOnly('search'),
  skill: piDeclaredOnly('skill'),
  task: piDeclaredOnly('task'),
  think: piDeclaredOnly('think'),
  trigger: piDeclaredOnly('trigger'),
  wait: piDeclaredOnly('wait'),
  web_search: piDeclaredOnly('web_search'),
}

/** One Pi tool call, as the kind-discriminated pair. */
export function piToolCall(row: PiToolRow, completion?: MessageCompletion): ToolCall {
  const facts = piToolFacts(row, completion)
  const envelope: ToolCallEnvelope = { id: row.tool.toolCallId, name: facts.toolName, lifecycle: facts.lifecycle }
  return createToolCall(envelope, readToolCallSpec(PI_TOOL_READERS, piReclassify(facts), facts))
}

/** The subagent card of a launch, a workflow run, or one of the two control tools. */
function piRowAgentRequest(facts: PiToolFacts): AgentRequest {
  return facts.toolName === PI_TOOL.SubagentWorkflow
    ? piWorkflowRequest(facts.payload, facts.request, facts.result)
    : piAgentRequest(facts.payload, facts.request)
}

/** The subagent report one finished agent call states. */
function piAgentRun(facts: PiToolFacts): AgentRun {
  return facts.toolName === PI_TOOL.SubagentWorkflow
    ? piWorkflowResult(facts.payload, facts.request)
    : piAgentResult(facts.payload, facts.request)
}

/**
 * Read requested file changes from the opening event.
 *
 * A resolved request remains authoritative even when it contains no changes.
 * Use this frame only when no request resolved.
 */
function piFileChangeRequest(facts: PiToolFacts): FileChangeRequest {
  return { changes: piFallbackDiffSources(facts.toolName, facts.request ? facts.request.parentObject : facts.payload) }
}
