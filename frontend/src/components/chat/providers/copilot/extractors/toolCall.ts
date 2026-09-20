import type { FileEditDiff } from '../../../model/fileEditDiff'
import type { McpContentItem } from '../../../model/mcpToolCall'
import type { QuestionPrompt } from '../../../model/question'
import type { SearchResult } from '../../../model/searchResult'
import type { FileChangeKind, ProseResult, ToolCall, ToolCallLifecycleFacts, ToolCallSpecReaderTable, ToolCallSpecVariant, ToolFailureResult } from '../../../model/toolCall'
import type { ToolCallStatus } from '../../../model/toolCallStatus'
import type { ToolKind } from '../../../model/toolKind'
import type { ToolMetadataEntry } from '../../../model/toolMetadata'
import type { ToolRequestByKind } from '../../../model/tools'
import type { CommandLanguage } from '../../../model/tools/execute'
import type { FileChangeRequest } from '../../../model/tools/fileChange'
import type { GenericToolResult } from '../../../model/tools/generic'
import type { SearchRequest } from '../../../model/tools/search'
import type { ToolRequestOverrides } from '../../defaultToolRequests'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { COPILOT_EVENT, COPILOT_TOOL } from '~/generated/contracts/copilot-protocol'
import { withFallbackFilePath } from '~/lib/imageBlocks'
import { prettifyStructuredJson } from '~/lib/jsonFormat'
import { isObject, pickFirstString, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { applyPatchFileChanges } from '../../../model/applyPatch'
import { createToolCall } from '../../../model/createToolCall'
import { parseMcpContentItem } from '../../../model/mcpToolCall'
import { searchOutputMode } from '../../../model/searchOutputMode'
import { failedResult, isFileChangeKind, proseResult, readToolCallSpec, unparsedResult } from '../../../model/toolCall'
import { deriveToolCallStatus } from '../../../model/toolCallLifecycle'
import { toolRequestFor } from '../../defaultToolRequests'
import { retainedOutcome } from '../../registry'
import { TOOL_FILE_PATH_KEYS, toolInputPaths } from '../../toolInputKeys'
import { copilotAgentResult } from '../extractors/agent'
import { copilotPatchText } from '../extractors/fileEdit'
import { copilotReadResult } from '../extractors/read'
import { copilotChecklistItems } from '../extractors/todo'
import { copilotEventData } from '../protocol'
import { copilotToolKind } from '../toolKinds'

/** The trailer Copilot appends to a shell result. It repeats the exit code. */
const SHELL_COMPLETION = /(?:\r?\n)?<shellId: [^\r\n>]+ completed with exit code (-?\d+)>\s*$/

/** The text without that trailer. The row states the exit code in its own place. */
function stripShellTrailer(value: string): string {
  const trailer = value.match(SHELL_COMPLETION)
  return trailer ? value.slice(0, trailer.index) : value
}

/**
 * True when one grep argument asks for context lines around each match.
 *
 * The count decides whether the match total is meaningful: with context lines, one
 * output line is not one match. Only a positive count asks for them, so `0`, `""`,
 * `false` and `null` all leave the total in place.
 */
function requestsContext(value: unknown): boolean {
  if (typeof value === 'number')
    return Number.isFinite(value) && value > 0
  if (typeof value === 'string')
    return Number(value.trim()) > 0
  return false
}

/**
 * The argument keys one execute-kind call states its command under.
 *
 * Copilot takes four tools at this kind and each spells it differently. `input` is
 * last, because `apply_patch` uses the same word for its patch text -- that tool
 * takes the edit kind and never reaches this list.
 */
const COPILOT_COMMAND_KEYS = ['command', 'query', 'script', 'input'] as const

/** The three kinds that answer with matches. All three declare one request and one result. */
const COPILOT_SEARCH_KINDS = new Set<ToolKind>(['glob', 'grep', 'search'])

/** The language one execute-kind tool runs, for the highlighter. */
function copilotCommandLanguage(toolName: string): CommandLanguage | undefined {
  if (toolName === COPILOT_TOOL.Sql || toolName === COPILOT_TOOL.SessionStoreSql)
    return 'sql'
  return toolName === COPILOT_TOOL.WritePowerShell ? 'powershell' : undefined
}

/**
 * The questions one `ask_user` call asked, from its own arguments.
 *
 * The same pair `copilotQuestions` reads off the control request, because the tool
 * call and the request state the question the same way. A row that asked nothing
 * this build can read answers an empty list, which the renderer draws as no request
 * body rather than an empty one.
 */
function copilotToolQuestions(input: Record<string, unknown>): QuestionPrompt[] {
  const question = pickString(input, 'question')
  if (!question)
    return []
  const choices = Array.isArray(input.choices) ? input.choices.filter((choice): choice is string => typeof choice === 'string') : []
  return [{ question, options: choices.map(choice => ({ label: choice })) }]
}

/**
 * One tool call, resolved from the two native events that describe it.
 *
 * `tool.execution_start` carries the name and the arguments; `tool.execution_complete`
 * carries the outcome and nothing that identifies the tool beyond its call ID. A
 * result row therefore reads its name and its arguments from the paired start row --
 * which the shared message resolver supplies -- or, failing that, from the span type
 * the worker recorded.
 */
export interface CopilotToolRow {
  toolCallId: string
  toolName: string
  /**
   * The kind {@link copilotToolKind} reads from the tool name alone.
   *
   * A `ToolKind` rather than a bare string, so the reader table below is total over
   * exactly what this field can hold. As a string it needed a narrowing step that
   * could answer `other` -- a kind no Copilot tool takes, and the one the uncategorized
   * renderer draws as a wrench over a dump of the arguments.
   */
  kind: ToolKind
  /** The MCP server and tool the start event stated, for a dash-namespaced call. */
  mcp?: { server: string, tool: string }
  input: Record<string, unknown>
  /** True once the completion event arrived. */
  finished: boolean
  lifecycle: ToolCallLifecycleFacts
  /** The completion's `result` object, or its `error` object when the call failed. */
  raw: Record<string, unknown> | null
}

/**
 * The FILE one grep match line identifies, for counting how many files matched.
 *
 * Copilot writes a match either as `path:line:text` or, when the reader asked for no
 * line numbers, as `path:text`. Both spellings reach this counter, so it cuts at the
 * line number when there is one and at the first colon when there is not. Cutting
 * always at the first colon put every match of an absolute Windows path
 * (`C:\repo\a.ts:12:hit`) under the bucket `C`; cutting always at `:N:` left a
 * whole unnumbered line as the "file", so each match counted as its own.
 */
function grepMatchFile(line: string): string {
  const numbered = line.replace(/:\d+:.*/s, '')
  if (numbered !== line)
    return numbered
  const separator = /^[a-z]:[\\/]/i.test(line) ? line.indexOf(':', 2) : line.indexOf(':')
  return separator < 0 ? line : line.slice(0, separator)
}

/**
 * The lifecycle facts a completion event states: the runtime's own fault flag and
 * interruption code are the provider's conclusion, the completion column carries
 * what LeapMux retained, and the completion event itself is the one thing that
 * lands a result.
 */
function copilotCompletionLifecycle(data: Record<string, unknown>, completion?: MessageCompletion): ToolCallLifecycleFacts {
  // The runtime reports an aborted turn's tool as a failure with this code, which is
  // an interruption rather than a fault the user should read as an error.
  const interrupted = pickString(pickObject(data, 'error'), 'code') === 'interrupted'
  return {
    frameStatus: 'unstated',
    providerOutcome: interrupted ? 'interrupted' : data.success !== true ? 'failed' : null,
    retainedOutcome: retainedOutcome(completion),
    rowFinal: true,
    resultFrameLanded: true,
  }
}

/**
 * The MCP server and tool the START event states, or undefined for a call that is not
 * one.
 *
 * Copilot namespaces an MCP tool with a DASH (`github-mcp-server-web_search`), which
 * no splitter can divide: both halves may hold dashes of their own. The start event
 * states the pair in two fields instead, and it is the only place either appears.
 */
function copilotMcpIdentity(started: Record<string, unknown> | null | undefined): { server: string, tool: string } | undefined {
  const server = pickString(started, 'mcpServerName')
  const tool = pickString(started, 'mcpToolName')
  return server && tool ? { server, tool } : undefined
}

/**
 * Build one tool row from a persisted event.
 *
 * `request` is the paired `tool.execution_start`, and `spanType` is the worker's own
 * record of the tool name on every span row. Returns null for any row that is not a
 * tool event, so a caller uses it as a guard.
 */
export function copilotToolRow(
  parsed: unknown,
  spanType?: string,
  request?: ParsedMessageContent,
  completion?: MessageCompletion,
): CopilotToolRow | null {
  const started = copilotEventData(parsed, COPILOT_EVENT.ToolStarted)
  const pairedStart = copilotEventData(request?.parentObject, COPILOT_EVENT.ToolStarted)
  if (started) {
    const toolName = pickString(started, 'toolName') || spanType || ''
    // A RETAINED copy of the start frame is the call's end: the turn stopped before
    // the runtime sent a completion, so this frame is all there is. It carries no
    // result, so the row shows its header and nothing under it.
    //
    // `succeeded` does NOT become `completed` here, and that is the one outcome this
    // branch refuses. A start row carries no result, so `completed` over it claims an
    // answer that never arrived -- the turn ending well says nothing about a call
    // whose completion the runtime never sent. `failed` does reach the row, because
    // it explains why the row stops where it does.
    const outcome = retainedOutcome(completion)
    const identity = copilotMcpIdentity(started)
    return {
      toolCallId: pickString(started, 'toolCallId'),
      toolName,
      kind: copilotToolKind(toolName),
      ...(identity ? { mcp: identity } : {}),
      input: pickObject(started, 'arguments') ?? {},
      finished: outcome !== null,
      // A retained start frame lands NO result: the turn ending says nothing about
      // a call whose completion event the runtime never sent.
      lifecycle: { frameStatus: 'unstated', providerOutcome: null, retainedOutcome: outcome, rowFinal: outcome !== null, resultFrameLanded: false },
      raw: null,
    }
  }
  const completed = copilotEventData(parsed, COPILOT_EVENT.ToolCompleted)
  if (!completed)
    return null
  const toolCallId = pickString(completed, 'toolCallId')
  // The paired start belongs to this call alone. A start for another call would
  // supply the wrong name and the wrong arguments.
  const paired = pairedStart && pickString(pairedStart, 'toolCallId') === toolCallId ? pairedStart : null
  const toolName = pickString(paired, 'toolName') || spanType || ''
  const lifecycle = copilotCompletionLifecycle(completed, completion)
  const status = deriveToolCallStatus(lifecycle, true)
  const identity = copilotMcpIdentity(paired)
  return {
    toolCallId,
    toolName,
    kind: copilotToolKind(toolName),
    ...(identity ? { mcp: identity } : {}),
    input: pickObject(paired, 'arguments') ?? {},
    finished: true,
    lifecycle,
    // A FAILURE states its reason in `error`. Reading `result` first there would show
    // whatever partial output the call produced and hide why it stopped. A cancelled
    // call is not a fault: the turn stopped around it, and its `result` holds the
    // output it produced before that, so the result comes first there.
    raw: status === 'failed'
      ? pickObject(completed, 'error') ?? pickObject(completed, 'result')
      : pickObject(completed, 'result') ?? pickObject(completed, 'error'),
  }
}

/** The text the result carries. The detailed form holds more than the model received. */
function copilotOutput(row: CopilotToolRow): string {
  return pickString(row.raw, 'detailedContent', undefined)
    ?? pickString(row.raw, 'content', undefined)
    ?? pickString(row.raw, 'message', undefined)
    ?? ''
}

/**
 * Everything one row's specification reads, collected once.
 *
 * The readers below take this and nothing else. Before it, each kind re-entered the
 * same helpers with seven and eight positional parameters, and three of the
 * derivations ran two or three times per row -- the rich content blocks, the
 * prettified structured payload, and the `contents` array each helper re-read off
 * `raw`. Worse, a reader that wanted one more fact grew the parameter list of every
 * other reader with it.
 *
 * Two argument records, and the difference is load-bearing. {@link rawArgs} is what
 * the start event sent. {@link args} adds three derivations that the specification reads and
 * the picture blocks must NOT: a `path` folded onto a patch row would make a screenshot
 * claim the file the patch touched.
 */
export interface CopilotToolFacts {
  /** The arguments the start event stated, untouched. */
  rawArgs: Record<string, unknown>
  /**
   * Those arguments, with the three derivations the specification reads folded in: the read
   * window from `view_range`, the single search target, and the file one patch
   * operation identifies.
   */
  args: Record<string, unknown>
  toolName: string
  /** The kind the TOOL NAME states, before {@link copilotReclassify} runs. */
  wireKind: ToolKind
  /** The completion's `result` object, or its `error` object when the call failed. */
  raw: Record<string, unknown> | null
  /** The text the result carries. */
  output: string
  status: ToolCallStatus
  /**
   * True once the call ENDED, whether the runtime completed it or the turn retained it.
   *
   * A finished row that did not fail is `completed` or `cancelled`, because those are
   * the only three words {@link copilotToolRow} states for a finished call. Both of the
   * two answer the same body: the runtime attached a `result` object, and the row's own
   * status is the one place that says a reader stopped the turn around it.
   */
  finished: boolean
  /**
   * The runtime's own fault flag: `success !== true` on the completion event, or the
   * `failed` outcome the turn recorded.
   *
   * A CANCELLED row is not one of these, and the distinction is the whole point. The
   * reader stopped the turn around a call that still ran, so `copilotToolRow` keeps
   * the `result` object the runtime attached -- and every branch below then keeps
   * the typed body it builds out of that object. Reading the row STATUS here instead
   * replaced the lines, the hits and the diff with the same text the row already
   * prints. The `Interrupted` header is unaffected: `toolCallStatusOutcome` composes it
   * from {@link status} and never from the result.
   */
  failed: boolean
  /** The MCP server and tool the start event stated, for a dash-namespaced call. */
  mcp: { server: string, tool: string } | undefined
  /** The content blocks the result carries, unparsed; null when it states none at all. */
  contents: unknown[] | null
  /** The result's structured payload, prettified; undefined when it carries none. */
  structuredJson: string | undefined
  /** The rich content blocks that ride beside a recognized body. */
  extraContent: McpContentItem[] | undefined
  /** The patch text `apply_patch` carries; the empty string for every other tool. */
  patchText: string
  /** The changes that patch ASKED for, or null when this build cannot read it. */
  requestedChanges: FileEditDiff[] | null
  /**
   * The matches a search answered with.
   *
   * Filled for a `glob`, `grep` or `search` row that FINISHED without a fault, which
   * is the one state that has matches to state, and undefined everywhere else. Its
   * presence is therefore the same question as "did this search answer", which is what
   * both {@link copilotReclassify} and {@link copilotSearchParts} ask it.
   */
  search: SearchResult | undefined
  /** The row's header word. Never empty: it falls back to the tool name and then to `Tool`. */
  title: string
}

/**
 * The ONE operation a patch describes, or undefined for a patch with several.
 *
 * Several operations state no single file and no single verb, so the row keeps the
 * edit kind and lists the files instead.
 */
function copilotSingleOperation(requestedChanges: FileEditDiff[] | null): FileEditDiff | undefined {
  return requestedChanges?.length === 1 ? requestedChanges[0] : undefined
}

/**
 * The arguments the payload reads: the start event's own, with three derivations.
 *
 * Each derivation restates a Copilot spelling in the neutral one the requests read.
 * They land on a COPY, so {@link CopilotToolFacts.rawArgs} keeps what the runtime sent.
 */
function copilotToolArgs(row: CopilotToolRow, requestedChanges: FileEditDiff[] | null): Record<string, unknown> {
  const args = { ...row.input }
  if (row.kind === 'read' && Array.isArray(args.view_range)) {
    const [first, last] = args.view_range
    // ONE guard, and `limit` inside it. Splitting them let a `view_range` of
    // `[0, 100]` state a limit of 101 with no offset -- a header claiming 101 lines
    // for a range that asked to start at 0 -- and let a model-written `['1', 100]`
    // reach `100 >= '1'`, which coerces to true.
    if (typeof first === 'number' && Number.isSafeInteger(first) && first > 0) {
      args.offset = first
      if (typeof last === 'number' && Number.isSafeInteger(last) && last >= first)
        args.limit = last - first + 1
    }
  }
  // ONE target, under the neutral key, so the search request states the path the call
  // asked for whichever spelling carried it.
  if (row.kind === 'glob' || row.kind === 'grep') {
    const paths = toolInputPaths(args)
    if (paths.length === 1)
      args.path = paths[0]
  }
  const operation = copilotSingleOperation(requestedChanges)
  if (operation) {
    args.path = operation.filePath
    if (operation.operation === 'add')
      args.content = operation.newStr
  }
  return args
}

/** Collect everything the readers and the reclassification step read, in one pass. */
export function copilotToolFacts(row: CopilotToolRow): CopilotToolFacts {
  // The patch text off the UNTOUCHED arguments, and BEFORE the folded copy exists.
  // `apply_patch` takes the edit kind, so neither of the other two derivations touches
  // its arguments -- and reading the patch first is what lets the file it identifies
  // reach the folded copy.
  const patchText = copilotPatchText(row.toolName, row.input)
  const requestedChanges = patchText ? applyPatchFileChanges(patchText) : null
  const args = copilotToolArgs(row, requestedChanges)
  const output = copilotOutput(row)
  const status = deriveToolCallStatus(row.lifecycle, row.lifecycle.resultFrameLanded)
  const failed = status === 'failed'
  const contents = Array.isArray(row.raw?.contents) ? row.raw.contents : null
  const structuredJson = prettifyStructuredJson(row.raw?.structuredContent)
  const answered = row.finished && !failed
  return {
    rawArgs: row.input,
    args,
    toolName: row.toolName,
    wireKind: row.kind,
    raw: row.raw,
    output,
    status,
    finished: row.finished,
    failed,
    mcp: row.mcp,
    contents,
    structuredJson,
    extraContent: copilotRichContent(row.input, contents, structuredJson),
    patchText,
    requestedChanges,
    search: answered && COPILOT_SEARCH_KINDS.has(row.kind) ? copilotSearchSource(row.kind, args, output) : undefined,
    // A command states itself, and the shared header draws it. The execute reader
    // therefore states no title at all, which is why this one word serves every kind.
    title: pickString(args, 'description') || row.toolName || 'Tool',
  }
}

/**
 * The kind one row DRAWS, which is not always the kind its tool name states.
 *
 * {@link copilotToolKind} classifies by the tool name alone, and three facts the name
 * cannot carry change the answer. Each swap states the input that causes it.
 *
 * ONE named step, ahead of the readers. The three swaps used to sit inside the branch
 * that filled the specification, so a kind could be chosen in one place and its request
 * filled in another -- and a reader who wanted the list of kinds a Copilot row can
 * reach had to find all three by hand.
 */
export function copilotReclassify(facts: CopilotToolFacts): ToolKind {
  // A patch with ONE operation states what it did: the `apply_patch` name gives the
  // edit kind, and an `*** Add File:` section is a write, an `*** Delete File:` a
  // removal.
  const operation = copilotSingleOperation(facts.requestedChanges)
  if (operation?.operation === 'add')
    return 'write'
  if (operation?.operation === 'delete')
    return 'delete'
  // A `view` that answered PROSE rather than a file body is a report: the call
  // finished well and printed words, and either the result carries no `content` string
  // or the arguments identify no file for those lines to belong to.
  if (facts.wireKind === 'read' && facts.finished && !facts.failed && facts.output
    && !(typeof facts.raw?.content === 'string' && pickFirstString(facts.args, TOOL_FILE_PATH_KEYS))) {
    return 'report'
  }
  // A grep or a tool search that answered a FILE LIST reads as a glob: `files_with_matches`
  // is the mode that lists files, and the glob renderer is the one that draws a file
  // list. A `glob` row needs no swap, because it is already that kind.
  if (facts.search && facts.search.filenames.length > 0)
    return 'glob'
  // A file operation that identifies NO file is not a file operation this build can
  // draw: `createToolCall` refuses it (invariant I7) and answers the uncategorized row, whose
  // words are the fault. The provider knows more here -- the tool name, the arguments
  // and whatever the call printed -- so the degrade happens at the frame, where the
  // generic card states all three. An `apply_patch` whose one text blob this build
  // could not read is the case. The two swaps above are not: a patch that DID parse
  // names the file each of its operations acts on.
  if (isFileChangeKind(facts.wireKind) && !copilotFileChangeStatesAFile(facts, facts.wireKind))
    return 'mcp'
  return facts.wireKind
}

/**
 * Whether a file operation states the file it acts on. Invariant I7.
 *
 * The request is asked, and it is the one the specification will carry.
 * answer differently. An empty file name counts as no file: the row's header is
 * composed from this list, and an entry with no name heads the row with the operation
 * word alone.
 */
function copilotFileChangeStatesAFile(facts: CopilotToolFacts, kind: FileChangeKind): boolean {
  const { changes } = copilotRequestFor(kind, facts)
  return changes.length > 0 && changes.every(change => Boolean(change.filePath))
}

/**
 * The kinds Copilot reads differently from the shared request table, and what each one
 * reads them from.
 *
 * Two reasons put an entry here, and each entry's comment states which.
 *
 *   - The kind reads a FACT the arguments do not carry: the tool name, the patch this
 *     build parsed, the MCP identity the start event stated, or the result text.
 *   - The runtime spells the argument in a word no other provider sends -- `todos`,
 *     `choices`, `shellId`, `agent`, `skill`, `source`. The shared table holds the
 *     neutral spellings, so it cannot answer for those.
 *
 * `read` and `web_search` are absent on purpose. Copilot spells both exactly as the
 * shared table does, so both take the shared entry and each has one reading.
 *
 * The whole deviation list is this object's own keys, and `COPILOT_TOOL_REQUEST_OVERRIDES`
 * in `createToolCall.test.ts` pins them. No type can do that job: an entry that reads `args`
 * alone satisfies a slot that supplies `args` and the facts, so a spread of the shared
 * table -- or one stray key that shadows a kind -- still compiles.
 *
 * Each entry DECLARES its return type, and the annotation is load-bearing rather than
 * decorative. TypeScript runs its excess-property check on a fresh object literal in
 * an ANNOTATED position only. The contextual signature this mapped type supplies is not
 * one: `fetch: args => ({ url, patchText })` compiles with the undeclared key, and the
 * model then carries a field no renderer reads. `fetch: (args): ToolRequestByKind['fetch'] =>`
 * rejects it.
 */
export const COPILOT_TOOL_REQUEST_OVERRIDES: ToolRequestOverrides<CopilotToolFacts> = {
  // The four shell tools and `read_agent` share this kind, and the TOOL NAME is what
  // says which of them ran. `shellId` is Copilot's own spelling of the target.
  task: (args, facts): ToolRequestByKind['task'] => {
    const taskId = pickString(args, 'shellId') || pickString(args, 'shell_id')
    return {
      action: facts.toolName === COPILOT_TOOL.StopBash
        ? 'stop'
        : facts.toolName === COPILOT_TOOL.ListBash ? 'list' : facts.toolName === COPILOT_TOOL.WriteBash ? 'input' : 'output',
      ...(taskId ? { taskId } : {}),
    }
  },
  // The TOOL NAME states the language for the highlighter, and four tools spell the
  // command four ways: only `bash` and `local_shell` send a `command`.
  execute: (args, facts): ToolRequestByKind['execute'] => {
    const language = copilotCommandLanguage(facts.toolName)
    const description = pickString(args, 'description') || undefined
    return {
      command: pickFirstString(args, COPILOT_COMMAND_KEYS) ?? '',
      ...(language !== undefined ? { language } : {}),
      ...(description !== undefined ? { description } : {}),
    }
  },
  // The PATCH this build parsed, which no argument states: `apply_patch` sends one
  // text blob, and the three edit-family kinds read the operations out of it.
  edit: (args, facts): ToolRequestByKind['edit'] => copilotFileChangeRequest(args, facts),
  write: (args, facts): ToolRequestByKind['write'] => copilotFileChangeRequest(args, facts),
  delete: (args, facts): ToolRequestByKind['delete'] => copilotFileChangeRequest(args, facts),
  // The MCP identity the START event stated. Copilot namespaces an MCP tool with a
  // dash, so the pair exists nowhere else; a call that is not one keeps its tool name.
  mcp: (args, facts): ToolRequestByKind['mcp'] => facts.mcp
    ? { server: facts.mcp.server, tool: facts.mcp.tool, args }
    : { server: '', tool: facts.toolName, args },
  // The SKILL the call ran, not the tool that ran it. The tool name is the fallback,
  // and `skill` is Copilot's own spelling, ahead of the neutral `name`.
  skill: (args, facts): ToolRequestByKind['skill'] => ({ name: pickString(args, 'skill') || pickString(args, 'name') || facts.toolName }),
  // The RESULT text, for a message the call stated in neither `message` nor `content`:
  // `write_agent` answers with the text it sent. `agent` is its word for the recipient.
  message: (args, facts): ToolRequestByKind['message'] => {
    const to = pickString(args, 'agent') || undefined
    return {
      ...(to !== undefined ? { to } : {}),
      text: pickString(args, 'message') || pickString(args, 'content') || facts.output,
    }
  },
  // The subagent's own LABEL beside the instruction: a launch states one or the other.
  // `agent_type` is Copilot's word for which subagent ran.
  agent: (args): ToolRequestByKind['agent'] => ({
    description: pickString(args, 'description') || pickString(args, 'name'),
    agentType: pickString(args, 'agent_type'),
    prompt: pickString(args, 'prompt'),
  }),
  // The checklist `update_todo` carries as ONE markdown string. No other provider
  // states its list that way, so the shared entry answers an empty list.
  todo: (args): ToolRequestByKind['todo'] => ({ items: copilotChecklistItems(pickString(args, 'todos')) }),
  // The question and the CHOICES `ask_user` offered. The shared entry answers an empty
  // list, so the row stated nothing it had asked.
  question: (args): ToolRequestByKind['question'] => ({ questions: copilotToolQuestions(args) }),
  // `source` is Copilot's word for where the file came from, and the shared move list
  // holds neither that spelling nor `path` for the destination.
  move: (args): ToolRequestByKind['move'] => {
    const previousPath = pickString(args, 'source') || undefined
    return {
      changes: [{
        filePath: pickString(args, 'path') || '',
        ...(previousPath !== undefined ? { previousPath } : {}),
        operation: 'move',
        oldStr: '',
        newStr: '',
        structuredPatch: null,
      }],
    }
  },
  // `path` FIRST, which is where `copilotToolArgs` puts the single target it recovered.
  // The shared entry reads the neutral key order instead, so the two answers differ for
  // a call that states `filePath` and `path` at once -- which no Copilot search tool does.
  glob: (args): ToolRequestByKind['glob'] => copilotSearchRequest(args),
  grep: (args): ToolRequestByKind['grep'] => copilotSearchRequest(args),
  search: (args): ToolRequestByKind['search'] => copilotSearchRequest(args),
  // Copilot's `web_fetch` sends `url` and never the `uri` the shared entry also reads.
  fetch: (args): ToolRequestByKind['fetch'] => ({ url: pickString(args, 'url') || '' }),
  // `exit_plan_mode` states the mode it leaves for, and no worktree target.
  switch_mode: (args): ToolRequestByKind['switch_mode'] => {
    const mode = pickString(args, 'mode') || undefined
    return { ...(mode !== undefined ? { mode } : {}) }
  },
  // The WHOLE argument record is the note, including an empty one: `context_board`
  // reads and writes the board with the same tool, and a read states no fields.
  memory: (args): ToolRequestByKind['memory'] => ({ payload: args }),
  report: (args): ToolRequestByKind['report'] => ({ payload: args }),
  // `list_agents` filters with `query` alone, and states no channel.
  agents: (args): ToolRequestByKind['agents'] => {
    const query = pickString(args, 'query') || undefined
    return { ...(query !== undefined ? { query } : {}) }
  },
}

/** One kind's declared request: Copilot's own reading where it states one, the shared one elsewhere. */
function copilotRequestFor<K extends ToolKind>(kind: K, facts: CopilotToolFacts): ToolRequestByKind[K] {
  return toolRequestFor(kind, facts.args, facts, COPILOT_TOOL_REQUEST_OVERRIDES)
}

/**
 * What a search LOOKED for. `glob`, `grep` and `search` declare one request, so one
 * reading serves the three override entries.
 *
 * The DECLARED return type is what puts the literal under the excess-property check,
 * exactly as each table entry's own annotation does. An un-annotated
 * `const request = { ... }` that a `return` then hands back by reference escapes that
 * check, and a `patchText` key once rode the edit request out of that hole.
 */
function copilotSearchRequest(args: Record<string, unknown>): SearchRequest {
  // The folded `path` FIRST, which is the single target `copilotToolArgs` recovered
  // from whichever key carried it.
  const path = pickString(args, 'path')
  return { pattern: pickString(args, 'pattern') || pickString(args, 'query'), paths: path ? [path] : toolInputPaths(args) }
}

/**
 * The file change one edit-family call ASKED for.
 *
 * Three sources, in order: the patch this build parsed, the replacement the native
 * edit tools carry, and -- for a removal, which has nothing to diff -- the path alone.
 * Without that last one the branch built no change, and the row drew the word "Delete"
 * over a card that stated no file.
 */
function copilotFileChangeRequest(args: Record<string, unknown>, facts: CopilotToolFacts): FileChangeRequest {
  if (facts.requestedChanges)
    return { changes: facts.requestedChanges }
  if (facts.wireKind === 'edit' || facts.wireKind === 'write') {
    const edit = copilotFileEdit(facts)
    return { changes: edit ? [edit] : [] }
  }
  const removed = pickFirstString(args, TOOL_FILE_PATH_KEYS)
  return {
    changes: facts.wireKind === 'delete' && removed
      ? [{ filePath: removed, operation: 'delete', oldStr: '', newStr: '', structuredPatch: null }]
      : [],
  }
}

/**
 * The file change one native edit or create tool states.
 *
 * Copilot's `edit` and `str_replace_editor` carry the replacement directly, and
 * `create` carries the whole new file. Returns null for a call that states no
 * change -- a `str_replace_editor` view, or a shape this build does not read.
 */
function copilotFileEdit(facts: CopilotToolFacts): FileEditDiff | null {
  const filePath = pickString(facts.args, 'path')
  if (!filePath)
    return null
  const command = pickString(facts.args, 'command')
  const fileText = pickString(facts.args, 'file_text', undefined)
  if (facts.toolName === COPILOT_TOOL.Create || command === 'create') {
    return fileText === undefined
      ? null
      : { filePath, operation: 'add', oldStr: '', newStr: fileText, structuredPatch: null, showLineNumbers: false }
  }
  const oldStr = pickString(facts.args, 'old_str', undefined)
  const newStr = pickString(facts.args, 'new_str', undefined)
  if (oldStr === undefined && newStr === undefined)
    return null
  return { filePath, operation: 'edit', oldStr: oldStr ?? '', newStr: newStr ?? '', structuredPatch: null, showLineNumbers: false }
}

/**
 * The answer of a kind whose result IS words, across the three states of the call.
 *
 * ONE ladder for the five prose kinds Copilot reaches: `skill`, `switch_mode`,
 * `memory`, `agents` and `report`. Each state used to be spelled per kind, so a case
 * corrected in one state and not in the other compiled and drew a different card
 * depending on whether the call had finished.
 *
 * `format` is REQUIRED, and each kind states its own. `ProseResultBody` reads it to
 * pick between the markdown body and a `<pre>` block, so it is a per-kind decision and
 * not a shared default: a roster or a report whose words are markdown draws its
 * asterisks, dashes and table pipes as literal characters under `plain`. The same
 * listing from Claude and from the Agent Client Protocol family draws formatted, so a
 * default here would put one provider's card out of step with every other one.
 */
function copilotProseAnswer(facts: CopilotToolFacts, format: ProseResult['format']): { result?: ProseResult | ToolFailureResult } {
  if (!facts.finished)
    return {}
  return { result: facts.failed ? failedResult(facts.output) : proseResult(facts.output, format) }
}

/**
 * The rich content blocks a specification carries, as a spread-in half.
 *
 * `extraContent` is optional on every specification, so the key stays absent for a call
 * that stated no blocks rather than present with `undefined` -- which every reader of
 * the field treats as none.
 */
function copilotExtraContent(extraContent: McpContentItem[] | undefined): { extraContent?: McpContentItem[] } {
  return extraContent !== undefined ? { extraContent } : {}
}

/**
 * The two halves of a file-change card, for the three kinds that declare one request
 * and one result.
 *
 * `kind` is the RECLASSIFIED one, which a patch can move off the tool name's own word.
 * A removal states the same change on both sides and needs no title, because the card
 * already identifies the file.
 */
function copilotFileChangeParts(facts: CopilotToolFacts, kind: 'edit' | 'write' | 'delete'): Omit<ToolCallSpecVariant<'edit'>, 'kind'> {
  const request = copilotRequestFor(kind, facts)
  const extraContent = facts.extraContent
  // Both halves are optional on the specification, so each stays absent when the facts state
  // none rather than present with `undefined`.
  const sides = {
    ...(extraContent !== undefined ? { extraContent } : {}),
  }
  // A patch with several operations lists its files; the shared title words a landed
  // change 'changed', so the request keeps the plain count. One this build cannot read
  // keeps the tool name, which identifies what ran.
  const title = facts.requestedChanges && facts.requestedChanges.length > 1
    ? `${new Set(facts.requestedChanges.map(source => source.filePath)).size} files`
    : facts.patchText && !facts.requestedChanges ? facts.toolName : undefined
  const titled = title !== undefined ? { ...sides, title } : sides
  if (!facts.finished)
    return { request, ...titled }
  // The REQUEST stays on a failure. `RequestedChangesBody` refuses to draw a failed
  // call's diff for every provider, so keeping the list adds nothing to the body -- but
  // the row's TITLE is composed from it, and an empty list heads a failed change with
  // the bare kind word and no way to tell which file the call was about.
  if (facts.failed)
    return { request, ...titled, result: failedResult(facts.output) }
  if (kind === 'delete')
    return { request, ...sides, result: { changes: request.changes } }
  const edit = copilotFileEdit(facts)
  if (edit)
    return { request, ...titled, result: { changes: [edit] } }
  if (facts.requestedChanges)
    return { request, ...titled, result: { changes: facts.requestedChanges } }
  return { request, ...titled, result: unparsedResult(facts.output || facts.patchText) }
}

/**
 * The two halves of a search card, for the three kinds that declare one request and one
 * result.
 *
 * `search` is filled exactly when the call finished without a fault, so its absence is
 * the two states above that one: a running call states its pattern alone, and a failed
 * one states the reason it printed.
 */
function copilotSearchParts(facts: CopilotToolFacts): Omit<ToolCallSpecVariant<'search'>, 'kind'> {
  const request = copilotSearchRequest(facts.args)
  const source = facts.search
  // The blocks ride EVERY state of the call, exactly as the prose family states below.
  // Copilot carries each PICTURE in `extraContent` rather than in the call's own image
  // list, so a state that drops it loses the rich blocks and every image with them --
  // and the image tab, which addresses a picture by its index, loses the same ones.
  // The field is optional on the specification, so it stays absent when the call stated none.
  const extraContent = facts.extraContent
  const sides = extraContent !== undefined ? { extraContent } : {}
  if (!source)
    return facts.failed ? { request, ...sides, result: failedResult(facts.output) } : { request, ...sides }
  // A match list keeps the grep kind and states its counts, so the summary reads
  // the total; a file list reads as a glob; a count mode as a grep.
  // A match list is the one mode that counts MATCHES without listing files:
  // count mode states its own total, and a file list states files.
  const matchList = source.mode !== 'count' && source.matchCount !== undefined && source.filenames.length === 0
  return {
    request,
    // The WIRE kind words the label, never the reclassified one: a grep that answered
    // a file list draws through the glob renderer and still ran `grep`.
    label: facts.wireKind === 'glob' ? 'Glob' : facts.wireKind === 'search' ? 'Search' : 'Grep',
    ...sides,
    result: matchList
      // `source.matchCount` is 0 for a grep that matched nothing, and `''.split('\n')`
      // is `['']`, so counting the output there reported "1 file" for no matches.
      ? { ...source, numLines: source.matchCount ?? 0, numFiles: source.matchCount ? new Set(facts.output.trim().split('\n').map(grepMatchFile)).size : 0 }
      : source,
  }
}

/**
 * A kind Copilot's tool table never states.
 *
 * The entry exists all the same, because the table is total over `ToolKind` -- which is
 * what makes a kind that reached no branch a compile error rather than a row that drew
 * the raw arguments. The request comes from `DEFAULT_TOOL_REQUESTS`, so the row states
 * the kind's declared fields the moment a later release does map a tool onto it.
 */
function copilotUnreachedKind<P extends ToolKind>(kind: P): (facts: CopilotToolFacts) => ToolCallSpecVariant<P> {
  return (facts): ToolCallSpecVariant<P> => ({ kind, request: copilotRequestFor(kind, facts) })
}

/**
 * One reader for each kind, each checked against its OWN kind's request and result.
 *
 * A chain of `if (kind === ...)` cannot do this. It tests a value, so the specification it
 * returns is checked against the union of every kind's shape, and a branch that states
 * one kind while it fills another kind's request compiles. That is the exact defect
 * class this table removes: a kind cannot be stated here without its own declared
 * request beside it.
 *
 * A mapped table cannot state that lie. Each entry's value type mentions its own `P`,
 * so no entry answers for another kind: `'grep': facts => ({ kind: 'search', ... })` is
 * a type error at the key.
 *
 * Each entry DECLARES its return type as well, for the reason
 * {@link COPILOT_TOOL_REQUEST_OVERRIDES} states: the excess-property check runs on an
 * ANNOTATED position and not on a contextual signature, so the annotation is what keeps
 * an undeclared key off the specification.
 *
 * The lifecycle stays INSIDE each reader rather than in a shared ladder above them,
 * because Copilot's kinds genuinely disagree about it: `execute` and `task` draw a
 * failed call themselves from its exit code, the prose family answers the same words in
 * two states, and the search family drops its label on a failure.
 */
export const COPILOT_TOOL_READERS: ToolCallSpecReaderTable<CopilotToolFacts> = {
  agent: (facts): ToolCallSpecVariant<'agent'> => {
    const request = copilotRequestFor('agent', facts)
    return {
      kind: 'agent',
      request,
      // The instruction, the subagent's label, or the shared word -- the same pair the
      // request states, so the header and the card cannot drift.
      title: request.description || 'Task',
      ...copilotExtraContent(facts.extraContent),
      ...(facts.finished ? { result: { agents: [copilotAgentResult(facts)] } } : {}),
    }
  },
  todo: (facts): ToolCallSpecVariant<'todo'> => {
    const request = copilotRequestFor('todo', facts)
    // No title: `todoRenderer` composes the same words from the request this specification
    // carries, and a copy here is a second place for the wording to drift.
    if (facts.failed)
      return { kind: 'todo', request, ...copilotExtraContent(facts.extraContent), result: failedResult(facts.output) }
    return { kind: 'todo', request, ...copilotExtraContent(facts.extraContent), ...(facts.finished ? { result: { items: request.items } } : {}) }
  },
  task: (facts): ToolCallSpecVariant<'task'> => {
    const request = copilotRequestFor('task', facts)
    if (!facts.finished)
      return { kind: 'task', request, title: facts.title }
    // The `read_bash` family answers with a background shell's own output, in the
    // shape `bash` answers with: a trailer that repeats the exit code, and a
    // `shell_exit` block that states the shell, its directory and its output file.
    // Reading none of it printed the trailer as part of the output, dropped those
    // three rows, and reported `completed` for a shell that exited 1.
    const shell = copilotCommandParts(facts)
    // The state of the SHELL this call asked about, which is not the outcome word of
    // the call itself. The exit code is the shell's own answer and wins wherever it
    // reported one. A call the reader stopped before that answer arrived states
    // `stopped`: `statesOwnOutcome` suppresses the shared `Interrupted` header for this
    // body, so `completed` here would be the only word the row draws for a read that
    // never finished.
    const outcome = facts.failed
      ? 'failed'
      : shell.exitCode !== undefined
        ? (shell.exitCode === 0 ? 'completed' : 'failed')
        : facts.status === 'cancelled' ? 'stopped' : 'completed'
    return {
      kind: 'task',
      request,
      title: facts.title,
      ...(shell.metadata !== undefined ? { metadata: shell.metadata } : {}),
      // `copilotCommandParts` reads the same blocks the rich content does and drops
      // the two this row already draws: the exit block and any block that repeats the
      // output.
      ...copilotExtraContent(shell.extraContent),
      result: { title: facts.title, outcome, output: shell.text },
    }
  },
  execute: (facts): ToolCallSpecVariant<'execute'> => {
    const request = copilotRequestFor('execute', facts)
    // No title: a command states itself in the shared header, and the tool word
    // `bash` above the very command it ran states nothing more.
    if (!facts.finished)
      return { kind: 'execute', request }
    const { exitCode, text, extraContent, metadata } = copilotCommandParts(facts)
    return {
      kind: 'execute',
      request,
      ...(metadata !== undefined ? { metadata } : {}),
      ...copilotExtraContent(extraContent),
      // The CODE is optional on the command: a shell that has not reported one states
      // neither it nor a signal.
      result: { commands: [{ output: text, ...(exitCode !== undefined ? { exitCode } : {}) }], unresolvedTerminals: [] },
    }
  },
  edit: (facts): ToolCallSpecVariant<'edit'> => ({ kind: 'edit', ...copilotFileChangeParts(facts, 'edit') }),
  write: (facts): ToolCallSpecVariant<'write'> => ({ kind: 'write', ...copilotFileChangeParts(facts, 'write') }),
  delete: (facts): ToolCallSpecVariant<'delete'> => ({ kind: 'delete', ...copilotFileChangeParts(facts, 'delete') }),
  read: (facts): ToolCallSpecVariant<'read'> => {
    const request = copilotRequestFor('read', facts)
    if (!facts.finished)
      return { kind: 'read', request }
    // The blocks ride every state of the call, for the reason `copilotSearchParts` gives.
    if (facts.failed)
      return { kind: 'read', request, ...copilotExtraContent(facts.extraContent), result: failedResult(facts.output) }
    if (typeof facts.raw?.content === 'string' && request.path)
      return { kind: 'read', request, result: copilotReadResult(facts.raw, facts.args), ...copilotExtraContent(facts.extraContent) }
    // A row reaches this only when the call printed NOTHING. A view that printed prose
    // and identified no file is a `report`, which {@link copilotReclassify} states.
    return { kind: 'read', request, ...copilotExtraContent(facts.extraContent), result: unparsedResult(facts.output) }
  },
  glob: (facts): ToolCallSpecVariant<'glob'> => ({ kind: 'glob', ...copilotSearchParts(facts) }),
  grep: (facts): ToolCallSpecVariant<'grep'> => ({ kind: 'grep', ...copilotSearchParts(facts) }),
  search: (facts): ToolCallSpecVariant<'search'> => ({ kind: 'search', ...copilotSearchParts(facts) }),
  web_search: (facts): ToolCallSpecVariant<'web_search'> => {
    const request = copilotRequestFor('web_search', facts)
    if (!facts.finished)
      return { kind: 'web_search', request }
    return { kind: 'web_search', request, ...copilotExtraContent(facts.extraContent), result: { links: [], summary: facts.output } }
  },
  question: (facts): ToolCallSpecVariant<'question'> => {
    const request = copilotRequestFor('question', facts)
    // No title: `questionRenderer` composes the same sentence from the request this
    // payload carries, and a copy here is a second place for the wording to drift.
    // The ANSWER's header is that sentence too, never the row title -- which falls
    // back to the tool name, so a finished question read "ask_user - <the answer>".
    const header = request.questions[0]?.question || 'Question'
    return {
      kind: 'question',
      request,
      ...copilotExtraContent(facts.extraContent),
      ...(facts.finished ? { result: { answers: facts.output ? [{ header, answer: facts.output }] : [] } } : {}),
    }
  },
  mcp: (facts): ToolCallSpecVariant<'mcp'> => {
    const request = copilotRequestFor('mcp', facts)
    // No extra content: an uncategorized card draws its blocks inside the result, so
    // stating them here too would draw each one twice.
    if (!facts.finished)
      return { kind: 'mcp', request }
    return { kind: 'mcp', request, result: copilotGenericToolResult(facts) }
  },
  move: (facts): ToolCallSpecVariant<'move'> => {
    const request = copilotRequestFor('move', facts)
    if (!facts.finished)
      return { kind: 'move', request, ...copilotExtraContent(facts.extraContent) }
    // The REQUEST stays, for the reason `copilotFileChangeParts` gives: a move whose
    // request is empty heads the row `Move` with neither source nor destination.
    if (facts.failed)
      return { kind: 'move', request, ...copilotExtraContent(facts.extraContent), result: failedResult(facts.output) }
    return { kind: 'move', request, ...copilotExtraContent(facts.extraContent), result: { changes: request.changes } }
  },
  fetch: (facts): ToolCallSpecVariant<'fetch'> => {
    const request = copilotRequestFor('fetch', facts)
    if (!facts.finished)
      return { kind: 'fetch', request }
    // The blocks ride every state of the call, for the reason `copilotSearchParts` gives.
    if (facts.failed)
      return { kind: 'fetch', request, ...copilotExtraContent(facts.extraContent), result: failedResult(facts.output) }
    return { kind: 'fetch', request, result: { result: facts.output }, ...copilotExtraContent(facts.extraContent) }
  },
  message: (facts): ToolCallSpecVariant<'message'> => {
    const request = copilotRequestFor('message', facts)
    if (!facts.finished)
      return { kind: 'message', request }
    return { kind: 'message', request, result: proseResult(facts.output), ...copilotExtraContent(facts.extraContent) }
  },
  // The five kinds whose answer is words. The blocks ride EVERY state of the call, not
  // the success one alone: a failed skill carries the same content the runtime attached.
  //
  // The three PLAIN ones each answer a short line the runtime composed, not a document:
  // `skill` states which skill loaded, `switch_mode` states the mode the session moved
  // to, and `memory` states what the scratch board holds. Claude and the Agent Client
  // Protocol family draw all three plain, and a `<pre>` block keeps the runtime's own
  // line breaks and indentation exactly as it sent them.
  skill: (facts): ToolCallSpecVariant<'skill'> => ({ kind: 'skill', request: copilotRequestFor('skill', facts), title: facts.title, ...copilotExtraContent(facts.extraContent), ...copilotProseAnswer(facts, 'plain') }),
  switch_mode: (facts): ToolCallSpecVariant<'switch_mode'> => ({ kind: 'switch_mode', request: copilotRequestFor('switch_mode', facts), title: facts.title, ...copilotExtraContent(facts.extraContent), ...copilotProseAnswer(facts, 'plain') }),
  memory: (facts): ToolCallSpecVariant<'memory'> => ({ kind: 'memory', request: copilotRequestFor('memory', facts), title: facts.title, ...copilotExtraContent(facts.extraContent), ...copilotProseAnswer(facts, 'plain') }),
  // A roster of subagents, which `list_agents` writes as a markdown table or list.
  agents: (facts): ToolCallSpecVariant<'agents'> => ({ kind: 'agents', request: copilotRequestFor('agents', facts), title: facts.title, ...copilotExtraContent(facts.extraContent), ...copilotProseAnswer(facts, 'markdown') }),
  report: (facts): ToolCallSpecVariant<'report'> => ({
    kind: 'report',
    request: copilotRequestFor('report', facts),
    // A `view` that {@link copilotReclassify} moved here states NO title: the tool word
    // `view` over a page of prose says nothing the body does not. A native
    // `task_complete` or `report_progress` states its own.
    ...(facts.wireKind !== 'read' ? { title: facts.title } : {}),
    ...copilotExtraContent(facts.extraContent),
    // MARKDOWN: all three sources of this kind are a written page. `task_complete` and
    // `report_progress` are the turn's own summary, and the reclassified `view` reached
    // here exactly because it printed prose rather than a file body.
    ...copilotProseAnswer(facts, 'markdown'),
  }),
  // The eight kinds no Copilot tool takes, and that no swap reaches either. The
  // `COPILOT_TOOL_READERS` cases in `createToolCall.test.ts` pin that list against the
  // contract, so a release that maps a tool onto one of them fails the suite here.
  unspecified: copilotUnreachedKind('unspecified'),
  other: copilotUnreachedKind('other'),
  chart: copilotUnreachedKind('chart'),
  image: copilotUnreachedKind('image'),
  list: copilotUnreachedKind('list'),
  think: copilotUnreachedKind('think'),
  trigger: copilotUnreachedKind('trigger'),
  wait: copilotUnreachedKind('wait'),
}

/**
 * One kind's specification from the facts.
 *
 * Generic over the kind, which keeps `kind` and the specification one
 * correlated pair. The caller's `ToolKind` satisfies the parameter member by member,
 * so no assertion stands between the table and the result --
 * the assertion ban in `eslint.config.ts` refuses exactly that assertion.
 */
function copilotSpecFor<K extends ToolKind>(facts: CopilotToolFacts, kind: K): { [P in K]: ToolCallSpecVariant<P> }[K] {
  return readToolCallSpec(COPILOT_TOOL_READERS, kind, facts)
}

/**
 * One Copilot tool call, as the kind-discriminated pair.
 *
 * Three steps, each with a name of its own: collect the facts, decide the kind, fill
 * that kind's declared specification.
 */
export function copilotToolCall(row: CopilotToolRow): ToolCall {
  const facts = copilotToolFacts(row)
  return createToolCall(
    { id: row.toolCallId, name: row.toolName, lifecycle: row.lifecycle },
    copilotSpecFor(facts, copilotReclassify(facts)),
  )
}

/** The exit code, the displayed text, and the content blocks of a shell result. */
function copilotCommandParts(facts: CopilotToolFacts): {
  exitCode: number | undefined
  text: string
  extraContent: McpContentItem[] | undefined
  metadata: ToolMetadataEntry[] | undefined
} {
  const contents = facts.contents ?? []
  const exits = contents.filter(isObject).filter(item => item.type === 'shell_exit')
  // The BLOCK and the CODE are two decisions. One `shell_exit` block states the shell,
  // the directory and the output file whether or not it reports a usable code, and the
  // rows below read it for those. Only a safe integer states the code itself.
  const exitBlock = exits.length === 1 ? exits[0] : undefined
  const reportedExit = pickNumber(exitBlock, 'exitCode', undefined)
  const knownExit = reportedExit !== undefined && Number.isSafeInteger(reportedExit) ? reportedExit : undefined
  const trailer = facts.output.match(SHELL_COMPLETION)
  // The TRAILER states the code in decimal text of unbounded length, so it takes the
  // same safe-integer gate as the structured block two lines above. Without it a
  // trailer reading `exit code 99999999999999999999` headed the row
  // `Error (exit 100000000000000000000)`, a number no platform can report.
  const trailerExit = trailer ? Number(trailer[1]) : undefined
  const exitCode = knownExit ?? (trailerExit !== undefined && Number.isSafeInteger(trailerExit) ? trailerExit : undefined)
  const text = stripShellTrailer(facts.output)
  // Every text the result itself carries is already on the row: the displayed text and
  // each field `copilotOutput` chose between. A content block that repeats one of them
  // would draw the same text a second time.
  const shown = new Set([facts.output, text])
  for (const key of ['content', 'detailedContent', 'message']) {
    const value = pickString(facts.raw, key, undefined)
    if (value !== undefined) {
      shown.add(value)
      shown.add(stripShellTrailer(value))
    }
  }
  const extra = contents.filter(item => item !== exitBlock
    && (!isObject(item) || !(item.type === 'text' && shown.has(pickString(item, 'text')))))
  // Each entry is one LABEL/KEY pair, so the tuple states the shape the destructure reads.
  const metadata = ([['Shell ID', 'shellId'], ['Directory', 'cwd'], ['Output file', 'outputFilePath']] as const)
    .flatMap(([label, key]) => {
      const value = pickString(exitBlock, key)
      return value ? [{ label, value }] : []
    })
  return {
    exitCode,
    text,
    extraContent: extra.length || facts.structuredJson
      ? [...extra.map(parseMcpContentItem), ...(facts.structuredJson ? [{ type: 'unknown' as const, raw: facts.structuredJson }] : [])]
      : undefined,
    metadata: metadata.length ? metadata : undefined,
  }
}

/**
 * The rich content a Copilot result carries beside its recognized body.
 *
 * Copilot's rich content always rides a rendered body rather than the call's own
 * image list: a generic body when the result is content blocks, and the command
 * body's own extra content when it is a shell result. `imagesForRow` reads it from
 * `extraContent`, which is why every reader that can carry one states it.
 *
 * An image block that carries no `uri` of its own takes the call's own file path, so a
 * screenshot OF a file keeps the file it came from -- `onOpenImage` then opens that
 * path at full resolution instead of the scaled-down bytes in the block. It reads the
 * UNTOUCHED arguments for that path: the derived copy carries the file a patch
 * operation identifies, which the call itself never sent.
 */
function copilotRichContent(rawArgs: Record<string, unknown>, contents: unknown[] | null, structuredJson: string | undefined): McpContentItem[] | undefined {
  if (!contents && !structuredJson)
    return undefined
  const fallbackPath = pickFirstString(rawArgs, TOOL_FILE_PATH_KEYS)
  const items = (contents ?? []).map(parseMcpContentItem).map(item =>
    item.type === 'image' && fallbackPath ? { ...item, source: withFallbackFilePath(item.source, fallbackPath) } : item)
  // The pretty STRING, exactly as the sibling execute path passes it. A round trip
  // through `JSON.parse` re-reads every number as a double, so a structured result
  // carrying an id past 2^53 drew a different id than the runtime sent.
  return [...items, ...(structuredJson ? [{ type: 'unknown' as const, raw: structuredJson }] : [])]
}

/** The generic result an unrecognized or MCP-shaped call answers with. */
function copilotGenericToolResult(facts: CopilotToolFacts): GenericToolResult {
  const parsed = (facts.contents ?? []).map(parseMcpContentItem)
  return {
    // An unmatched completion identifies no tool: its content text IS the result. A
    // FAILED call states that text as the error alone -- `GenericToolBody` draws
    // both fields with no guard, so putting it in each drew the same words twice.
    content: parsed.length > 0 ? parsed : !facts.failed && facts.output ? [{ type: 'text', text: facts.output }] : [],
    ...(facts.structuredJson ? { structuredJson: facts.structuredJson } : {}),
    ...(facts.failed && facts.output ? { error: facts.output } : {}),
  }
}

/** A glob or grep result: Copilot returns its matches as plain lines. */
function copilotSearchSource(wireKind: ToolKind, args: Record<string, unknown>, output: string): SearchResult {
  const glob = wireKind === 'glob'
  const empty = output.trim() === '' || /^No (?:files|matches) (?:found|matched)[.!]?$/i.test(output.trim())
  const lines = empty ? [] : output.trim().split('\n').filter(Boolean)
  const mode = searchOutputMode(args.output_mode)
  const fileList = glob || mode === 'files_with_matches'
  const countMode = mode === 'count'
  const counts = countMode ? lines.map(line => line.match(/:(\d+)\s*$/)).filter(match => match !== null) : []
  const hasContext = ['A', 'B', 'C', '-A', '-B', '-C', 'context', 'after_context', 'before_context']
    .some(key => requestsContext(args[key]))
  // One count, whichever mode produced it: the row words it from the kind and the
  // mode, and the two can no longer be set at once to disagree.
  const matchCount = countMode
    ? (counts.length === lines.length ? counts.reduce((total, match) => total + Number(match[1]), 0) : undefined)
    : (!fileList && !hasContext ? lines.length : undefined)
  // `mode` and `matchCount` are optional on the result, so each stays ABSENT when the
  // source stated none rather than present with `undefined`.
  return {
    filenames: fileList ? lines : [],
    content: fileList || empty ? '' : output,
    numFiles: fileList || countMode ? lines.length : 0,
    numLines: 0,
    ...(mode !== undefined ? { mode } : {}),
    ...(matchCount !== undefined ? { matchCount } : {}),
    truncated: false,
    fallbackContent: empty ? '' : output,
    empty,
  }
}
