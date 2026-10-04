import type { ToolSpanRowRole } from '../../../model/row'
import type { ToolCall, ToolCallEnvelope, ToolCallLifecycleFacts, ToolCallSpecReaderTable, ToolCallSpecVariant, UnparsedToolResult } from '../../../model/toolCall'
import type { ToolKind } from '../../../model/toolKind'
import type { ProviderToolOutcome } from '../../../model/toolOutcome'
import type { ToolRequestByKind } from '../../../model/tools'
import type { WebSearchLink } from '../../../model/tools/webSearch'
import type { ToolRequestOverrides } from '../../defaultToolRequests'
import type { AmpToolResult, AmpToolUse } from './toolCommon'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { AMP_SHELL_TOOL } from '~/generated/contracts/amp-protocol'
import { isObject, pickNumber, pickString, stringArray } from '~/lib/jsonPick'
import { createToolCall } from '../../../model/createToolCall'
import { parseMcpContentItem, parseMcpToolName } from '../../../model/mcpToolCall'
import { failedResult, proseResult, readToolCallSpec, unparsedResult } from '../../../model/toolCall'
import { toolRequestFor } from '../../defaultToolRequests'
import { retainedOutcome, retainedRowIsFinal } from '../../registry'
import { ampToolKind } from '../toolKinds'
import { AMP_TOOL_NAME } from '../toolNames'
import { ampAgentRequest, ampAgentResult } from './agent'
import { ampCommandResult, ampShellCommand, ampShellTaskResult } from './execute'
import { ampCreateFileChange, ampEditFileChange, ampEditFileResultChange, ampPatchRequestChanges, ampPatchResultChanges } from './fileEdit'
import { ampReadRequest, ampReadResult } from './read'
import { ampGlobResult, ampGrepResult } from './search'
import { ampToolResult, ampToolUse } from './toolCommon'

/** The display name of a tool whose wire name is not the word a reader wants. */
const AMP_TOOL_LABELS: ReadonlyMap<string, string> = new Map<string, string>([
  [AMP_SHELL_TOOL.ShellCommand, 'Shell'],
  [AMP_TOOL_NAME.AsyncShellCommand, 'Shell'],
  [AMP_TOOL_NAME.Bash, 'Bash'],
  [AMP_SHELL_TOOL.ShellCommandStatus, 'Shell Status'],
  [AMP_SHELL_TOOL.ShellCommandKill, 'Stop Shell'],
  [AMP_TOOL_NAME.ApplyPatch, 'Apply Patch'],
  [AMP_TOOL_NAME.EditFile, 'Edit'],
  [AMP_TOOL_NAME.CreateFile, 'Create'],
  [AMP_TOOL_NAME.DeleteFile, 'Delete'],
  [AMP_TOOL_NAME.Read, 'Read'],
  [AMP_TOOL_NAME.ViewMedia, 'View Media'],
  [AMP_TOOL_NAME.Grep, 'Grep'],
  [AMP_TOOL_NAME.Glob, 'Glob'],
  [AMP_TOOL_NAME.GlobAlias, 'Glob'],
  [AMP_TOOL_NAME.WebSearch, 'Web Search'],
  [AMP_TOOL_NAME.ReadWebPage, 'Web Page'],
  [AMP_TOOL_NAME.Painter, 'Painter'],
  [AMP_TOOL_NAME.Skill, 'Skill'],
  [AMP_TOOL_NAME.Sleep, 'Sleep'],
])

/**
 * The words that open a result Amp did not let the tool produce.
 *
 * Amp writes a refused or cancelled call as the text of its result, and it does not
 * always flag it as an error: a call that a permission rule rejected arrives with
 * `is_error: false`. The prefix is what states the outcome.
 *
 *   - `Tool rejected by plugin:` -- a permission rule rejected the call, or the
 *     LeapMux helper refused it with no reason.
 *   - `Plugin error:` -- the LeapMux helper refused the call, and its reason follows:
 *     the reader's own words, or why no decision could be made.
 *   - `Tool execution rejected by user:` -- a reader of Amp's own interface declined.
 *   - `Tool execution cancelled:` -- the turn stopped while the call ran.
 */
const DECLINED_PREFIXES = ['Tool rejected by plugin:', 'Plugin error:', 'Tool execution rejected by user:']
const CANCELLED_PREFIX = 'Tool execution cancelled:'

/** How Amp says a call ended, from its result: declined, cancelled, failed, or null for a result. */
export function ampResultOutcome(result: AmpToolResult | undefined): ProviderToolOutcome | null {
  if (!result)
    return null
  const text = result.content.trimStart()
  if (DECLINED_PREFIXES.some(prefix => text.startsWith(prefix)))
    return 'declined'
  if (text.startsWith(CANCELLED_PREFIX))
    return 'interrupted'
  return result.isError ? 'failed' : null
}

/** One Amp tool call, resolved from its row and the two rows that describe it. */
export interface AmpToolRow {
  call: AmpToolUse
  /** The call's result, from this row or from the paired result row. */
  result: AmpToolResult | undefined
  /** True when this row is the last one of its call. */
  finished: boolean
}

/**
 * Build one tool row, or null for a row that is not an Amp tool row.
 *
 * Only a side of THIS call counts: one message can run several calls, and a sibling's
 * row is no side of this one. A result row whose request the store did not resolve
 * still draws, as the generic card of a tool with no name.
 */
export function ampToolRow(
  payload: Record<string, unknown> | undefined,
  request: ParsedMessageContent | undefined,
  result: ParsedMessageContent | undefined,
  completion?: MessageCompletion,
): AmpToolRow | null {
  const ownCall = ampToolUse(payload)
  const ownResult = ampToolResult(payload)
  if (ownCall) {
    const paired = ampToolResult(result?.parentObject)
    return {
      call: ownCall,
      result: paired?.toolUseId === ownCall.id ? paired : undefined,
      // A retained request row is the call's END: the turn stopped before the result.
      finished: retainedRowIsFinal(completion),
    }
  }
  if (!ownResult)
    return null
  const paired = ampToolUse(request?.parentObject)
  return {
    call: paired?.id === ownResult.toolUseId ? paired : { id: ownResult.toolUseId, name: '', input: {} },
    result: ownResult,
    finished: true,
  }
}

/** Which SIDE of its span one row draws: the request while the call runs, the answer once it finished. */
export function ampToolSpanRowRole(row: AmpToolRow): ToolSpanRowRole {
  return row.finished ? 'result' : 'request'
}

/** Everything one payload decision reads, collected ONCE for the row. */
export interface AmpToolFacts {
  callId: string
  toolName: string
  label: string | undefined
  args: Record<string, unknown>
  /** The result text, or '' before the result landed. */
  text: string
  /** Amp refused, cancelled or failed the call, so the text states why. */
  failed: boolean
  finished: boolean
  lifecycle: ToolCallLifecycleFacts
  resultAvailable: boolean
}

/** Collect everything the payload decisions read, in one pass. */
export function ampToolFacts(row: AmpToolRow, completion: MessageCompletion | undefined): AmpToolFacts {
  const outcome = ampResultOutcome(row.result)
  const toolName = row.call.name
  return {
    callId: row.call.id,
    toolName,
    label: AMP_TOOL_LABELS.get(toolName) ?? (toolName || undefined),
    args: row.call.input,
    text: row.result?.content ?? '',
    failed: outcome !== null,
    finished: row.finished,
    lifecycle: {
      frameStatus: 'unstated',
      providerOutcome: outcome,
      retainedOutcome: retainedOutcome(completion),
      rowFinal: row.finished,
      resultFrameLanded: row.result !== undefined,
    },
    resultAvailable: row.result !== undefined,
  }
}

/**
 * The kind one row draws with. A tool the table does not list -- a tool of a plugin,
 * of a Model Context Protocol server, or of a later Amp -- answers with text, so it
 * takes the generic card rather than the uncategorized kind.
 */
export function ampToolCallKind(facts: AmpToolFacts): ToolKind {
  const declared = ampToolKind(facts.toolName)
  return declared === 'unspecified' ? 'mcp' : declared
}

/**
 * The kinds Amp reads from its OWN arguments. Every other kind takes the shared
 * `DEFAULT_TOOL_REQUESTS` entry, which reads the arguments alone.
 *
 * `delete`, `fetch`, `grep`, `image` and `skill` are absent on purpose: Amp spells
 * their arguments the way the shared table reads them.
 */
export const AMP_TOOL_REQUEST_OVERRIDES: ToolRequestOverrides<AmpToolFacts> = {
  // Each specialist states its request under a key of its own, and the call's id is
  // the key of the registry row.
  agent: (args, facts): ToolRequestByKind['agent'] => ampAgentRequest(facts.toolName, args, facts.callId),
  // `apply_patch` states a `*** Begin Patch` text, and `edit_file` states `old_str`
  // and `new_str`, which are none of the shared spellings.
  edit: (args, facts): ToolRequestByKind['edit'] => {
    if (facts.toolName === AMP_TOOL_NAME.ApplyPatch)
      return { changes: ampPatchRequestChanges(args) ?? [] }
    const change = ampEditFileChange(args)
    return { changes: change ? [change] : [], ...(args.replace_all === true ? { replaceAll: true } : {}) }
  },
  // `create_file` states the written text as `content`, which is none of the shared spellings.
  write: (args): ToolRequestByKind['write'] => {
    const change = ampCreateFileChange(args)
    return { changes: change ? [change] : [] }
  },
  // `Read` states its lines as an inclusive `read_range` pair.
  read: (args): ToolRequestByKind['read'] => ampReadRequest(args),
  // A shell call states its directory as `workdir`.
  execute: (args): ToolRequestByKind['execute'] => {
    const cwd = pickString(args, 'workdir')
    return { command: ampShellCommand(args), ...(cwd ? { cwd } : {}) }
  },
  // The status and the kill of a background command state its process id.
  task: (args, facts): ToolRequestByKind['task'] => {
    const pid = pickNumber(args, 'pid', undefined)
    const timeoutMs = pickNumber(args, 'timeout_ms', undefined)
    return {
      action: facts.toolName === AMP_SHELL_TOOL.ShellCommandKill ? 'stop' : 'output',
      ...(pid !== undefined ? { taskId: String(pid) } : {}),
      ...(timeoutMs !== undefined ? { timeoutMs } : {}),
    }
  },
  // `glob` states its pattern as `filePattern`.
  glob: (args): ToolRequestByKind['glob'] => ({ pattern: pickString(args, 'filePattern') || pickString(args, 'pattern'), paths: [] }),
  // A web search states its goal as `objective`, and the keyword queries beside it.
  web_search: (args): ToolRequestByKind['web_search'] => {
    const queries = stringArray(args.search_queries)
    return { query: pickString(args, 'objective') || queries[0] || '', ...(queries.length > 0 ? { queries } : {}) }
  },
  // A tool of a Model Context Protocol server spells its server and tool in its name.
  mcp: (args, facts): ToolRequestByKind['mcp'] => {
    const parsed = parseMcpToolName(facts.toolName)
    return { server: parsed?.server ?? '', tool: parsed?.tool ?? facts.toolName, args }
  },
}

/** One kind's declared request: Amp's own reading where it states one, the shared table's elsewhere. */
function requestFor<K extends ToolKind>(kind: K, facts: AmpToolFacts): ToolRequestByKind[K] {
  return toolRequestFor(kind, facts.args, facts, AMP_TOOL_REQUEST_OVERRIDES)
}

/** The words above one tool row: the tool's display name, and a title. */
interface AmpRowHeader {
  label?: string
  title?: string
}

/** The tool's display name and the words above it. */
function header(facts: AmpToolFacts): AmpRowHeader {
  return { ...(facts.label !== undefined ? { label: facts.label } : {}), title: facts.label ?? 'Tool' }
}

/**
 * The spec of a call that has no result yet, or that Amp refused, cancelled or failed,
 * or null for a call whose result the kind reads.
 */
function unfinished<K extends ToolKind>(
  kind: K,
  facts: AmpToolFacts,
  request: ToolRequestByKind[K],
  head: AmpRowHeader,
): ToolCallSpecVariant<K> | null {
  if (!facts.resultAvailable)
    return { kind, ...head, request }
  if (facts.failed)
    return { kind, ...head, request, result: failedResult(facts.text) }
  return null
}

/**
 * The words a finished call returned, when no reader of its kind could read them. No
 * words state no result at all.
 */
function unreadResult(facts: AmpToolFacts): UnparsedToolResult | undefined {
  return facts.text ? unparsedResult(facts.text) : undefined
}

/** The declared payload of a kind Amp never produces: its request, and no result. */
function declaredOnly<K extends ToolKind>(kind: K): (facts: AmpToolFacts) => ToolCallSpecVariant<K> {
  return (facts): ToolCallSpecVariant<K> => ({ kind, ...header(facts), request: requestFor(kind, facts) })
}

/** The links one `web_search` result lists, or none for a result that is not a list. */
function webSearchLinks(text: string): WebSearchLink[] {
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  }
  catch {
    return []
  }
  const entries = Array.isArray(parsed) ? parsed : isObject(parsed) && Array.isArray(parsed.results) ? parsed.results : []
  return entries.filter(isObject).flatMap((entry) => {
    const url = pickString(entry, 'url')
    return url ? [{ title: pickString(entry, 'title') || url, url }] : []
  })
}

/**
 * One reader for each kind, each checked against its OWN kind's request and result.
 *
 * Every entry declares its own return type, because a contextual signature is not an
 * annotated position: without it an entry takes a stray key without a word.
 */
export const AMP_TOOL_READERS: ToolCallSpecReaderTable<AmpToolFacts> = {
  agent: (facts): ToolCallSpecVariant<'agent'> => {
    const request = requestFor('agent', facts)
    // The subagent's task heads the row, because the tool name says only that a
    // subagent ran.
    const head: AmpRowHeader = { ...header(facts), title: request.description }
    return unfinished('agent', facts, request, head)
      ?? { kind: 'agent', ...head, request, result: ampAgentResult(request, facts.text, false) }
  },
  execute: (facts): ToolCallSpecVariant<'execute'> => {
    const request = requestFor('execute', facts)
    // No title: the command is the header on every surface, and a title would put the
    // tool's name above the very command it ran.
    const head: AmpRowHeader = facts.label !== undefined ? { label: facts.label } : {}
    return unfinished('execute', facts, request, head)
      ?? { kind: 'execute', ...head, request, result: { commands: [ampCommandResult(facts.text)], unresolvedTerminals: [] } }
  },
  task: (facts): ToolCallSpecVariant<'task'> => {
    const request = requestFor('task', facts)
    const head = header(facts)
    return unfinished('task', facts, request, head)
      ?? { kind: 'task', ...head, request, result: ampShellTaskResult(facts.text, facts.toolName === AMP_SHELL_TOOL.ShellCommandKill) }
  },
  read: (facts): ToolCallSpecVariant<'read'> => {
    const request = requestFor('read', facts)
    const head = header(facts)
    const early = unfinished('read', facts, request, head)
    if (early)
      return early
    const outcome = ampReadResult(facts.text)
    if (outcome)
      return { kind: 'read', ...head, request, result: outcome.result, images: outcome.images }
    const unread = unreadResult(facts)
    return { kind: 'read', ...head, request, ...(unread !== undefined ? { result: unread } : {}) }
  },
  fetch: (facts): ToolCallSpecVariant<'fetch'> => {
    const request = requestFor('fetch', facts)
    const head = header(facts)
    return unfinished('fetch', facts, request, head) ?? { kind: 'fetch', ...head, request, result: { result: facts.text } }
  },
  edit: (facts): ToolCallSpecVariant<'edit'> => {
    const request = requestFor('edit', facts)
    const head = header(facts)
    const early = unfinished('edit', facts, request, head)
    if (early)
      return early
    const landed = facts.toolName === AMP_TOOL_NAME.ApplyPatch
      ? ampPatchResultChanges(facts.text)
      : [ampEditFileResultChange(facts.text, request.changes[0] ?? null)].filter(change => change !== null)
    if (landed && landed.length > 0)
      return { kind: 'edit', ...head, request, result: { changes: landed } }
    const unread = unreadResult(facts)
    return { kind: 'edit', ...head, request, ...(unread !== undefined ? { result: unread } : {}) }
  },
  write: (facts): ToolCallSpecVariant<'write'> => {
    const request = requestFor('write', facts)
    const head = header(facts)
    const early = unfinished('write', facts, request, head)
    if (early)
      return early
    // Amp confirms the write in words, and the file it wrote is the one the call stated.
    if (request.changes.length > 0)
      return { kind: 'write', ...head, request, result: { changes: request.changes } }
    const unread = unreadResult(facts)
    return { kind: 'write', ...head, request, ...(unread !== undefined ? { result: unread } : {}) }
  },
  delete: (facts): ToolCallSpecVariant<'delete'> => {
    const request = requestFor('delete', facts)
    const head = header(facts)
    const early = unfinished('delete', facts, request, head)
    if (early)
      return early
    if (request.changes.length > 0)
      return { kind: 'delete', ...head, request, result: { changes: request.changes } }
    const unread = unreadResult(facts)
    return { kind: 'delete', ...head, request, ...(unread !== undefined ? { result: unread } : {}) }
  },
  grep: (facts): ToolCallSpecVariant<'grep'> => {
    const request = requestFor('grep', facts)
    const head = header(facts)
    return unfinished('grep', facts, request, head) ?? { kind: 'grep', ...head, request, result: ampGrepResult(facts.text) }
  },
  glob: (facts): ToolCallSpecVariant<'glob'> => {
    const request = requestFor('glob', facts)
    const head = header(facts)
    return unfinished('glob', facts, request, head) ?? { kind: 'glob', ...head, request, result: ampGlobResult(facts.text) }
  },
  web_search: (facts): ToolCallSpecVariant<'web_search'> => {
    const request = requestFor('web_search', facts)
    const head = header(facts)
    const early = unfinished('web_search', facts, request, head)
    if (early)
      return early
    const links = webSearchLinks(facts.text)
    return { kind: 'web_search', ...head, request, result: { links, summary: links.length > 0 ? '' : facts.text } }
  },
  image: (facts): ToolCallSpecVariant<'image'> => {
    const request = requestFor('image', facts)
    const head = header(facts)
    const early = unfinished('image', facts, request, head)
    if (early)
      return early
    const unread = unreadResult(facts)
    return { kind: 'image', ...head, request, result: unread ?? {} }
  },
  skill: (facts): ToolCallSpecVariant<'skill'> => {
    const request = requestFor('skill', facts)
    const head = header(facts)
    return unfinished('skill', facts, request, head) ?? { kind: 'skill', ...head, request, result: proseResult(facts.text, 'markdown') }
  },
  wait: (facts): ToolCallSpecVariant<'wait'> => {
    const request = requestFor('wait', facts)
    const head = header(facts)
    return unfinished('wait', facts, request, head) ?? { kind: 'wait', ...head, request, result: proseResult(facts.text) }
  },
  mcp: (facts): ToolCallSpecVariant<'mcp'> => {
    const request = requestFor('mcp', facts)
    const head = header(facts)
    if (!facts.resultAvailable)
      return { kind: 'mcp', ...head, request }
    return {
      kind: 'mcp',
      ...head,
      request,
      result: {
        content: [parseMcpContentItem({ type: 'text', text: facts.text })],
        ...(facts.failed ? { error: facts.text } : {}),
      },
    }
  },
  // The kinds Amp states no tool for. Each still declares its own request, so the day
  // one of them arrives it draws its own card rather than a dump.
  unspecified: declaredOnly('unspecified'),
  agents: declaredOnly('agents'),
  chart: declaredOnly('chart'),
  list: declaredOnly('list'),
  memory: declaredOnly('memory'),
  message: declaredOnly('message'),
  move: declaredOnly('move'),
  other: declaredOnly('other'),
  question: declaredOnly('question'),
  report: declaredOnly('report'),
  search: declaredOnly('search'),
  switch_mode: declaredOnly('switch_mode'),
  think: declaredOnly('think'),
  todo: declaredOnly('todo'),
  trigger: declaredOnly('trigger'),
}

/** One Amp tool call, as the kind-discriminated pair. */
export function ampToolCall(row: AmpToolRow, completion?: MessageCompletion): ToolCall {
  const facts = ampToolFacts(row, completion)
  const envelope: ToolCallEnvelope = { id: facts.callId, name: facts.toolName, lifecycle: facts.lifecycle }
  return createToolCall(envelope, readToolCallSpec(AMP_TOOL_READERS, ampToolCallKind(facts), facts))
}
