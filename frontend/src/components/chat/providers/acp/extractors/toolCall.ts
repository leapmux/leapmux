import type { ToolCall, ToolCallLifecycleFacts, ToolCallSpec, ToolCallSpecVariant } from '../../../model/toolCall'
import type { ToolCallStatus } from '../../../model/toolCallStatus'
import type { ToolKind } from '../../../model/toolKind'
import type { RetainedToolOutcome } from '../../../model/toolOutcome'
import type { ToolRequestByKind } from '../../../model/tools'
import type { FileChangeRequest, FileChangeResult } from '../../../model/tools/fileChange'
import type { GenericToolResult } from '../../../model/tools/generic'
import type { McpRequest } from '../../../model/tools/mcp'
import type { ToolRequestOverrides } from '../../defaultToolRequests'
import type {} from '../../registry'
import type { ACPToolSupplement } from '../toolSupplement'
import type { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ContentBlock } from '~/lib/contentBlocks'
import type { ImageResultSource } from '~/lib/imageBlocks'
import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { isFinishedToolCallStatus, toolCallStatus } from '~/components/chat/model/toolCallStatus'
import { ACP_SUPPLEMENT_REQUEST } from '~/generated/contracts/acp-protocol'
import { prettifyArgsJson, prettifyJson } from '~/lib/jsonFormat'
import { isObject, pickFirstString, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { createToolCall } from '../../../model/createToolCall'
import { fileEditHasDiff } from '../../../model/fileEditDiff'
import { parseMcpContentItem } from '../../../model/mcpToolCall'
import { failedResult, isGenericKind, proseResult, unparsedResult } from '../../../model/toolCall'
import { deriveToolCallStatus } from '../../../model/toolCallLifecycle'
import { toolKind } from '../../../model/toolKind'
import { humanizeWireWord } from '../../../rendererUtils'
import { toolRequestFor } from '../../defaultToolRequests'
import { retainedOutcome, retainedRowIsFinal } from '../../registry'
import { TOOL_FILE_PATH_KEYS, toolInputPaths } from '../../toolInputKeys'
import { collectAcpToolTextFromContent, flattenAcpContent } from '../content'
import { acpExecuteFromToolCall } from '../extractors/execute'
import { acpFileEditFromToolCallContent, acpFileEditsFromToolCallRawInput } from '../extractors/fileEdit'
import { acpImagesFromContent } from '../extractors/image'
import { acpReadFromToolCall } from '../extractors/read'
import { acpSearchFromToolCall } from '../extractors/search'
import { acpTerminalResults } from '../extractors/terminal'
import { acpWebFetchFromToolCall } from '../extractors/webFetch'
import { unwrapACPResult } from '../resultWrapper'
import { acpSupplementProtocol, acpToolSupplement } from '../toolSupplement'
import { ACP_SESSION_UPDATE } from '../updateVocabulary'

// The kind groups these tables ask about, as sets the compiler checks against the
// closed ToolKind union. `['read', …].includes(kind)` infers a `string[]`, so a
// misspelled or renamed member stayed a branch that simply never fired.
const FILE_TARGET_KINDS = new Set<ToolKind>(['read', 'edit', 'write', 'delete'])

/**
 * Each provider keeps its native fields and tool semantics in its own adapter.
 *
 * `row` states where this row sits in its tool span, which a provider needs to place
 * something exactly once across the two rows of ONE call. The tool's own fields cannot do
 * that job: `resolveACPToolCall` merges the request into the result, so both rows can
 * carry the same `sessionUpdate` and `status`, and neither `acpToolFinished` nor the
 * stored `sessionUpdate` separates them by the time an adapter runs.
 */
export interface ACPToolRow {
  /** Where the row sits in its span. `createMessageRenderSources` decides it by message id. */
  role?: ToolSpanRole
  /** True when a completing row is resolved beside this one. */
  hasResult?: boolean
}

/** Everything the shared ACP build knows before a provider adapter runs. */
export interface ACPToolFacts {
  /** After resolveACPToolCall, the retained-outcome status override, and the rawInput repair. */
  tool: Record<string, unknown>
  extra: ACPToolSupplement | undefined
  /** The kind the WIRE states, narrowed; `''` and `other` are NOT folded to `mcp` here. */
  wireKind: ToolKind
  /** The object form of the call's arguments. See {@link NormalizedToolInput}. */
  args: Record<string, unknown>
  /**
   * The text form of the call's arguments, which the generic card draws.
   *
   * Separate from `args`, because ACP declares `rawInput` as arbitrary JSON: a call
   * whose whole argument is one bare string has nothing to put in `args`, and it
   * reached the reader as a card with no arguments at all.
   */
  argsText?: string
  /**
   * The call states an input, and it is NOT an object.
   *
   * No typed request can be filled from one: every typed reader indexes `args`.
   * {@link acpBaseSpec} therefore builds such a call at the generic kind, where
   * `argsText` states the whole argument.
   *
   * An ABSENT input leaves this false, and that is a different case: a file tool
   * often states no arguments of its own and recovers its path from `locations`.
   */
  scalarInput: boolean
  status: ToolCallStatus
  /** The outcome LeapMux's completion column retained for the turn, or null. */
  retained: RetainedToolOutcome | null
  /** The call's lifecycle as RAW FACTS; the shared derivation owns the precedence. */
  lifecycle: ToolCallLifecycleFacts
  /**
   * Whether the call ENDED: its own status states it, or the retained outcome does.
   *
   * The ONE answer to that question. A second field asked `status === 'completed'`
   * alone, which is false for a row the turn retained after the provider stopped
   * sending -- so each builder that read it dropped the file content, the hits, the
   * page or the diff of a call that had in fact finished.
   */
  finished: boolean
  /** The collected text of the call's content blocks. */
  text: string
  /** The content blocks, normalized once for text, images, and generic results. */
  content: ContentBlock[]
  /** The structured raw output formatted once for a generic result. */
  structuredJson?: string
  images: ImageResultSource[]
  terminals: ReturnType<typeof acpTerminalResults>
  place: ACPToolRow
}

/**
 * The generic card's two halves: the arguments as the tool sent them, and the
 * content blocks it answered with.
 *
 * Shared by the three kinds that state no vocabulary. Only the REQUEST differs
 * between them, and it differs by widening -- `McpRequest extends GenericToolRequest` --
 * so one shape satisfies all three.
 *
 * The lifecycle is NOT here. {@link acpSpecFor} owns it for every kind, so this
 * states the answer of a call that finished and let it stand.
 */
function acpGenericCard(facts: ACPToolFacts): { request: McpRequest, result: GenericToolResult } {
  const args = facts.args
  const title = acpCallTitle(facts)
  // `facts.argsText` rather than a second format of `args`: a scalar input has no
  // object to format, and this card is the one surface that can still show it.
  const request: McpRequest = { server: '', tool: facts.wireKind === 'other' && pickString(facts.tool, 'kind') !== 'other' ? pickString(facts.tool, 'kind') : title, args, ...(facts.argsText !== undefined ? { argsText: facts.argsText } : {}) }
  const content = facts.content.length > 0 ? facts.content.map(parseMcpContentItem) : facts.text ? [{ type: 'text' as const, text: facts.text }] : []
  return { request, result: { content, ...(facts.structuredJson !== undefined ? { structuredJson: facts.structuredJson } : {}) } }
}

/**
 * The DECLARED request of one kind, filled from the arguments, with no result yet.
 *
 * A provider adapter that reclassifies supplies its own specification. This is what the row
 * draws until it does, and it states each kind's declared request rather than the raw
 * arguments: the renderers read those fields with no guard, so a `{ args }` here
 * reaches `call.request.changes[0]` on a running call and throws the whole message
 * into the ErrorBoundary.
 *
 * The RESULT is {@link acpSpecFor}'s: a finished call with no typed answer takes
 * the words it printed. Before the ladder these twenty kinds answered nothing at all,
 * and twelve of them declare no request body either -- so a finished row of one of
 * them drew its header and an empty card.
 */
function acpArgumentsOnly<P extends ToolKind>(kind: P): ACPSpecEntry<P> {
  // The inner arrow states its OWN return type, although `ACPSpecEntry<P>` already
  // declares it. A contextual signature is not an annotated position, so without this
  // the literal escapes the excess-property check -- the same hole every `build` above
  // closes, one level down.
  return { build: (facts): ToolCallSpecVariant<P> => ({ kind, request: acpDefaultRequestFor(kind, facts), title: acpCallTitle(facts) }) }
}

/**
 * The table's entry for one kind: how to build its specification, and the two questions
 * the shared code asks about it.
 *
 * `build` states the kind's own request and, where the kind can answer one, its own
 * result. It does NOT state the lifecycle -- {@link acpSpecFor} owns that for
 * every kind, so a builder cannot forget the retained state or the failed one.
 */
interface ACPSpecEntry<P extends ToolKind> {
  build: (facts: ACPToolFacts) => ToolCallSpecVariant<P>
  /**
   * The kind draws a FAILED call itself, so the ladder leaves its result alone.
   *
   * `execute` alone. Its renderer declares `statesOwnOutcome` for a result that
   * carries commands, and `ToolMessage` suppresses the shared error header for
   * exactly that; the command body takes the call's status and is built to draw a
   * failed command's own output beside its exit code. Replacing that with the
   * reason in words is what makes a row read `Error` where every other provider
   * reads `Error (exit 1)`.
   */
  ownsFailure?: true
  /**
   * Whether a row of this kind still needs its result side resolved, read from the
   * TYPED request the build produced.
   *
   * Absent means the shared default: a call whose arguments are empty states
   * nothing, so the result row is the only thing that can.
   */
  needsResult?: (request: ToolRequestByKind[P], facts: ACPToolFacts) => boolean
}

/**
 * The file-change card's two halves. `edit` and `write` answer the same way, and
 * declare the same request and result, so one builder serves both -- but each entry
 * states its OWN kind, because a generic `P` would put the pair beyond the checker
 * again, which is the whole defect this table exists to remove.
 */
function acpFileChangeParts(kind: 'edit' | 'write', facts: ACPToolFacts): { request: FileChangeRequest, result?: FileChangeResult } {
  const args = facts.args

  // EVERY substitution the arguments state, and not the first one: a multi-edit asks
  // for several in one file, so a reader that took one drew a request that described
  // part of the call.
  //
  // A change that DRAWS no diff is kept, for the reason `unnamedFileChange` states in
  // `test-support/toolVocabulary.ts`: the row composes its header from this list at
  // every state of the call, and `RequestedFileChanges` states the file on its own
  // line for exactly a change with no body. Dropping it headed a failed edit with the
  // word "Edit" and no file.
  const changes = acpFileEditsFromToolCallRawInput(kind === 'write' ? 'write' : 'edit', args)
    .map(source => ({ ...source, showLineNumbers: false }))
  const request: FileChangeRequest = { changes }
  const sources = Array.isArray(facts.tool.content)
    ? facts.tool.content.flatMap((entry) => {
        const source = acpFileEditFromToolCallContent([entry])
        return fileEditHasDiff(source) ? [source] : []
      })
    : []
  return sources.length > 0 ? { request, result: { changes: sources } } : { request }
}

/**
 * Whether a file-family call still needs its result side to state the file.
 *
 * The typed request answers first, and the raw arguments answer after it: an edit
 * that states a path and no replacement text carries no drawable change, so its
 * `changes` list is empty while the arguments do state the file.
 */
function fileChangesNeedResult(request: { changes: readonly unknown[] }, facts: ACPToolFacts): boolean {
  return request.changes.length === 0 && !pickFirstString(facts.args, TOOL_FILE_PATH_KEYS)
}

/**
 * One builder for each kind, each checked against its OWN kind's request and result.
 *
 * A generic `switch (kind)` cannot do this. TypeScript narrows the VALUE the switch
 * tests and never the type parameter `K`, so every case had to be asserted back to
 * `K`'s payload at the return -- and a case that answered the WRONG kind compiled.
 * `acpSpecFor(facts, 'mcp')` did exactly that: no case list held `mcp`, so it fell
 * to the generic case and answered `kind: 'unspecified'` from behind a declared
 * `ToolCallSpec<'mcp'>`. Cursor's MCP branch tested `shared.kind === 'mcp'`,
 * never matched, and drew the card with no body at all.
 *
 * A mapped table cannot state that lie: each entry's value type mentions its own `P`,
 * so no entry can answer for another kind and no assertion is available to hide it.
 * The table is total over `ToolKind` by its own type, which is the guarantee the
 * `default`-less switch gave, stated once rather than per case.
 *
 * EVERY `build` declares its own return type, and the annotation is load-bearing rather
 * than decorative. The mapped type states which kind each entry answers for; it does not
 * put the builder's literal under the excess-property check. TypeScript infers an
 * un-annotated arrow's return type from the literals it returns, so the object loses its
 * freshness before any property is checked, and the payload then carries a key no
 * renderer reads. The annotation goes on `build` and not on the entry, because the entry
 * is an object rather than a function. `needsResult` needs none: it answers a boolean,
 * which holds no property to be excess.
 *
 * The rule holds one step in as well. A `request` lifted into a `const` is not a fresh
 * literal, so each of the four builders that lifts one declares that `const`'s type too.
 * `toolTableEntriesAreAnnotated.test.ts` keeps both forms in place.
 *
 * The table is EXPORTED for its own cases in `createToolCall.test.ts`, which pin the three
 * statements no type makes: the keys are exactly `TOOL_KINDS`, each entry answers at
 * the key that states it, and every kind outside the provider's own readings fills the
 * shared declared request. The last one is the only mechanical check that
 * {@link ACP_TOOL_REQUEST_OVERRIDES} has not grown past its deviations.
 */
export const ACP_SPEC_READERS: { [P in ToolKind]: ACPSpecEntry<P> } = {
  execute: { ownsFailure: true, needsResult: request => !request.command, build: (facts): ToolCallSpecVariant<'execute'> => {
    const kind = 'execute' as const
    const args = facts.args

    const description = pickString(args, 'description')
    const request: ToolRequestByKind['execute'] = { command: pickString(args, 'command') || '', ...(description ? { description } : {}) }
    const merged = acpExecuteFromToolCall(facts.tool)
    // The call's own text blocks and its terminals are two different outputs: an
    // agent that wrote a preamble beside a terminal reference meant both to show.
    // A terminal id that resolved to no terminal says LeapMux could not recover
    // the stream, which `outputUnavailable` states -- `[no output]` would claim
    // the command printed nothing.
    const ownOutput = merged?.output && !facts.terminals.entries.some(entry => entry.output === merged.output)
      ? [{ ...merged, outputUnavailable: false }]
      : []
    const commands = facts.terminals.entries.length > 0
      ? [...ownOutput, ...facts.terminals.entries]
      : merged
        ? [{ ...merged, outputUnavailable: facts.terminals.unresolved.length > 0 && !merged.output }]
        : []
    // A command states itself in the shared header; a title that merely repeats
    // it would sit above the very command it ran.
    const frameTitle = pickString(facts.tool, 'title')
    const title = frameTitle && frameTitle !== request.command && frameTitle !== pickString(facts.tool, 'kind') ? frameTitle : undefined
    return { kind, request, ...(title !== undefined ? { title } : {}), result: { commands, unresolvedTerminals: facts.terminals.unresolved } }
  } },
  read: { needsResult: (request, facts) => !request.path && !pickFirstString(facts.args, TOOL_FILE_PATH_KEYS), build: (facts): ToolCallSpecVariant<'read'> => {
    const kind = 'read' as const
    const args = facts.args
    const title = acpCallTitle(facts)

    const offset = pickNumber(args, 'offset', undefined)
    const limit = pickNumber(args, 'limit', undefined)
    const request: ToolRequestByKind['read'] = { path: pickFirstString(args, TOOL_FILE_PATH_KEYS) ?? '', ...(offset !== undefined ? { offset } : {}), ...(limit !== undefined ? { limit } : {}) }
    const source = acpReadFromToolCall(facts.tool)
    if (source?.lines !== null && source)
      return { kind, request, title, result: source }
    return { kind, request, title, images: facts.images }
  } },
  edit: { needsResult: fileChangesNeedResult, build: (facts): ToolCallSpecVariant<'edit'> => ({ kind: 'edit', ...acpFileChangeParts('edit', facts) }) },
  write: { needsResult: fileChangesNeedResult, build: (facts): ToolCallSpecVariant<'write'> => ({ kind: 'write', ...acpFileChangeParts('write', facts) }) },
  search: { needsResult: request => !request.pattern, build: (facts): ToolCallSpecVariant<'search'> => {
    const kind = 'search' as const
    const args = facts.args
    const title = acpCallTitle(facts)

    const request: ToolRequestByKind['search'] = { pattern: pickString(args, 'pattern') || pickString(args, 'query') || '', paths: toolInputPaths(args) }
    const source = acpSearchFromToolCall(facts.tool)
    return source ? { kind, request, title, result: source } : { kind, request, title }
  } },
  fetch: { needsResult: request => !request.url, build: (facts): ToolCallSpecVariant<'fetch'> => {
    const kind = 'fetch' as const
    const args = facts.args
    const title = acpCallTitle(facts)

    const request: ToolRequestByKind['fetch'] = { url: pickString(args, 'url') || '' }
    const source = acpWebFetchFromToolCall(facts.tool)
    return source ? { kind, request, title, result: source } : { kind, request, title }
  } },
  switch_mode: { build: (facts): ToolCallSpecVariant<'switch_mode'> => {
    const kind = 'switch_mode' as const
    // The one kind in this list the protocol itself defines. Its answer is the
    // sentence the switch wrote, which is the prose the kind draws.
    //
    // The ARGUMENTS come from the shared table, which every other kind here reads.
    // This branch spelled its own request once, so the table entry for the kind was
    // unreachable and read a different key set -- no test could see the two disagree,
    // and a reader who corrected the table changed nothing.
    const request = acpDefaultRequestFor(kind, facts)
    return { kind, request, title: acpCallTitle(facts), result: proseResult(facts.text) }
  } },
  // The three kinds that state no vocabulary. Each states its OWN kind, which is what
  // the shared `default` case could not do.
  unspecified: { build: (facts): ToolCallSpecVariant<'unspecified'> => ({ kind: 'unspecified', ...acpGenericCard(facts) }) },
  other: { build: (facts): ToolCallSpecVariant<'other'> => ({ kind: 'other', ...acpGenericCard(facts) }) },
  mcp: { build: (facts): ToolCallSpecVariant<'mcp'> => ({ kind: 'mcp', ...acpGenericCard(facts) }) },
  // The kinds whose DECLARED result is the words the tool wrote. `unparsedResult`
  // states "this build could not read the payload into the kind's shape", which is
  // never true where the shape IS the words, so each answers prose instead. `think`
  // is the fifth of them and sits with the rest of the table below, because its words
  // reach it through its own request rather than through the content blocks.
  agents: { build: (facts): ToolCallSpecVariant<'agents'> => ({ kind: 'agents', request: acpDefaultRequestFor('agents', facts), title: acpCallTitle(facts), result: proseResult(facts.text, 'markdown') }) },
  memory: { build: (facts): ToolCallSpecVariant<'memory'> => ({ kind: 'memory', request: acpDefaultRequestFor('memory', facts), title: acpCallTitle(facts), result: proseResult(facts.text) }) },
  message: { build: (facts): ToolCallSpecVariant<'message'> => ({ kind: 'message', request: acpDefaultRequestFor('message', facts), title: acpCallTitle(facts), result: proseResult(facts.text) }) },
  report: { build: (facts): ToolCallSpecVariant<'report'> => ({ kind: 'report', request: acpDefaultRequestFor('report', facts), title: acpCallTitle(facts), result: proseResult(facts.text, 'markdown') }) },
  // An agent launch and a to-do list both answer on the row that COMPLETES them, and
  // neither states its answer in the arguments, so both always need the result side.
  agent: { ...acpArgumentsOnly('agent'), needsResult: () => true },
  todo: { ...acpArgumentsOnly('todo'), needsResult: () => true },
  delete: { ...acpArgumentsOnly('delete'), needsResult: fileChangesNeedResult },
  move: { ...acpArgumentsOnly('move'), needsResult: fileChangesNeedResult },
  glob: { ...acpArgumentsOnly('glob'), needsResult: request => !request.pattern },
  grep: { ...acpArgumentsOnly('grep'), needsResult: request => !request.pattern },
  web_search: { ...acpArgumentsOnly('web_search'), needsResult: request => !request.query },
  chart: acpArgumentsOnly('chart'),
  // The PICTURES are the answer and they ride `call.images`, so the typed result
  // states only a prompt the generator revised. It is stated all the same: a
  // completed call with no result at all draws its header over an empty card.
  //
  // An `{}` here states no prompt, so {@link acpSpecFor} replaces it with the words
  // the call printed whenever it printed any. That is what carries the reason a
  // cancelled or failed generation gave beside the picture it never produced.
  image: { build: (facts): ToolCallSpecVariant<'image'> => ({ kind: 'image', request: acpDefaultRequestFor('image', facts), title: acpCallTitle(facts), result: {} }) },
  list: acpArgumentsOnly('list'),
  question: acpArgumentsOnly('question'),
  skill: acpArgumentsOnly('skill'),
  task: acpArgumentsOnly('task'),
  // PROSE, like the four kinds above, and built from the request rather than from
  // `facts.text`: {@link ACP_TOOL_REQUEST_OVERRIDES} already folds the thought's two
  // arrival points -- a `thought` argument and the call's own content -- into one
  // field. Without a result the ladder answered `unparsedResult(facts.text)`, whose
  // brand `parsedCall` strips, so `thinkRenderer` read `result` as absent, drew its
  // request line, and the row printed the first line of the thought as a summary above
  // the whole thought again.
  think: { build: (facts): ToolCallSpecVariant<'think'> => {
    const kind = 'think' as const
    const request: ToolRequestByKind['think'] = acpDefaultRequestFor(kind, facts)
    return { kind, request, title: acpCallTitle(facts), result: proseResult(request.text) }
  } },
  trigger: acpArgumentsOnly('trigger'),
  wait: acpArgumentsOnly('wait'),
}

/**
 * The shared default payload of ONE kind from the facts. Total over ToolKind.
 *
 * The LIFECYCLE ladder lives here rather than in the builders, so no kind can forget
 * a state of it. A call that has not finished carries no result; one that FAILED
 * answers the reason it stated in words; one that finished and stated no typed answer
 * takes the words it printed.
 *
 * The last step is what twenty kinds were missing. `ToolResult<K>` admits the
 * unparsed and the failed brand for EVERY kind, and no kind's own result declares
 * either word, so both are unambiguous wherever they land.
 *
 * A CANCELLED call is NOT a failure here, and it keeps whatever body the builder read.
 * The reader stopped the turn around a call that still ran, so the lines, the hits
 * and the diff that arrived are exactly what they asked to see. The reason branch
 * would replace all of it with the same text the row already prints, and the two rows
 * then differ in their bodies while both head `Interrupted`. ZCode, Pi, Claude and
 * Codex each key this branch on the PROVIDER's own error flag, so one lifecycle now
 * holds for every provider. The header is unaffected: `toolCallStatusOutcome` composes
 * it from the row's own status and never from the result.
 *
 * The last step also replaces a typed result that states NOTHING, which
 * {@link acpResultStatesNothing} defines. Otherwise a builder that answers `{}` puts
 * an empty typed result where the ladder can never reach, and the words the call
 * printed are lost -- `image` was that case at every one of its states.
 */
/**
 * The payload with no result slot at all.
 *
 * The exact-optional rule spells "no result" as an ABSENT key, and the ladder needs
 * that state for a call whose builder stated one: an unfinished call carries none
 * (I1), and so does a finished call that printed nothing and answered nothing typed.
 * Every reader of the slot tests its VALUE (`result !== undefined`), so the stripped
 * payload reads exactly as the old present-but-undefined one did.
 */
function withoutResult<P extends ToolKind>(spec: ToolCallSpecVariant<P>): ToolCallSpecVariant<P> {
  const { result, ...rest } = spec
  return result === undefined ? spec : rest
}

export function acpSpecFor<K extends ToolKind>(facts: ACPToolFacts, kind: K): { [P in K]: ToolCallSpecVariant<P> }[K] {
  const entry = ACP_SPEC_READERS[kind]
  const spec = entry.build(facts)
  if (!acpResultAvailable(facts))
    return withoutResult(spec)
  if (entry.ownsFailure)
    return spec
  if (facts.status === 'failed')
    return { ...spec, result: failedResult(facts.text) }
  if (spec.result !== undefined && !acpResultStatesNothing(spec.result))
    return spec
  if (facts.text)
    return { ...spec, result: unparsedResult(facts.text) }
  // A result a builder stated that says nothing (see {@link acpResultStatesNothing})
  // still states "no answer", which absent does not.
  if (spec.result !== undefined)
    return spec
  return withoutResult(spec)
}

/**
 * Whether this frame supplied result data, including a retained one-row body.
 *
 * A row that a turn end closed is finished with no result frame when the agent never
 * answered the call, and it holds no answer then. An adapter that builds a result of
 * its own asks this first, as {@link acpSpecFor} does.
 */
export function acpResultAvailable(facts: ACPToolFacts): boolean {
  return facts.lifecycle.resultFrameLanded
    || (facts.finished && (
      facts.content.length > 0
      || facts.images.length > 0
      || facts.tool.rawOutput !== undefined
      || facts.terminals.entries.length > 0
    ))
}

/**
 * Whether one typed result states NOTHING: it holds no property with a value.
 *
 * The predicate is over the VALUES, never over the key count, and that is what makes
 * it safe. A result whose declared field is an empty list or an empty string is a
 * real answer -- `{ content: [] }`, `{ changes: [] }`, `{ items: [] }`,
 * `{ text: '', format: 'plain' }` -- and each of those renderers draws an empty state
 * of its own, from `TodoListBody`'s cleared list to `EMPTY_RESULT_NOTICE`. Each holds
 * a defined value, so none of them is empty here.
 *
 * Only a result whose fields are ALL optional can reach `true`, and `image` is the one
 * kind in `ToolResultByKind` whose shape is that: `{ revisedPrompt?: string }`. Every other
 * kind declares at least one required field, so the compiler already refuses the empty
 * object there. `UnparsedToolResult` and `ToolFailureResult` each carry a brand and a `text`,
 * so neither reads as empty either.
 *
 * EXPORTED for its own cases in `createToolCall.test.ts`. No ACP builder answers an
 * all-empty-but-present result today, so the exclusions this predicate rests on are
 * demonstrable here and nowhere else.
 */
export function acpResultStatesNothing(result: object): boolean {
  return Object.values(result).every(value => value === undefined)
}

/**
 * The two kinds ACP reads from its own facts. Every other kind takes the shared
 * `DEFAULT_TOOL_REQUESTS` entry, which reads the arguments alone.
 *
 * This is the WHOLE deviation list, and it stays that short on purpose: an entry here
 * is a shape no other provider gets, so a reading that the arguments can supply in a
 * provider-NEUTRAL way belongs in the shared table where every provider reads it.
 *
 * Both entries declare their own return type, for the reason
 * {@link ACP_SPEC_READERS} gives: a contextual signature is not an annotated
 * position, so an un-annotated entry takes a stray key without a word.
 */
export const ACP_TOOL_REQUEST_OVERRIDES: ToolRequestOverrides<ACPToolFacts> = {
  // The frame's own TITLE, which an ACP launch states there rather than in its
  // arguments; the shared entry sees the arguments alone.
  agent: (args, facts): ToolRequestByKind['agent'] => ({ description: acpCallTitle(facts), prompt: pickString(args, 'prompt') || pickString(args, 'instructions') || '' }),
  // The call's collected TEXT, which is where an ACP thought arrives when the tool
  // states no `thought` argument; the shared entry sees the arguments alone.
  think: (args, facts): ToolRequestByKind['think'] => ({ text: pickString(args, 'thought') || pickString(args, 'text') || facts.text }),
}

function acpDefaultRequestFor<K extends ToolKind>(kind: K, facts: ACPToolFacts): ToolRequestByKind[K] {
  return toolRequestFor(kind, facts.args, facts, ACP_TOOL_REQUEST_OVERRIDES)
}

/** The row's own header word when the arguments state one. */
function acpCallTitle(facts: ACPToolFacts): string {
  return pickString(facts.args, 'description') || pickString(facts.tool, 'title')
    || (facts.wireKind === 'other' && pickString(facts.tool, 'kind') !== 'other' ? humanizeWireWord(pickString(facts.tool, 'kind')) : '') || 'Tool'
}

/**
 * The shared payload at the kind the WIRE stated, with the generic trio folded to
 * `mcp`.
 *
 * The uncategorized card states the server and the tool, which a wrench and the word
 * "Other" do not. An adapter that rebuilt its own facts asks for this rather than
 * repeating the fold, which is one more place for the two to disagree.
 */
export function acpBaseSpec(facts: ACPToolFacts): ToolCallSpec {
  return acpSpecFor(facts, acpSpecKind(facts))
}

/**
 * The kind the shared payload builds at.
 *
 * The generic trio folds to `mcp`, and so does a KNOWN kind whose input is a scalar.
 * No typed request can be filled from one -- every typed reader indexes `args` -- so
 * such a call used to draw an empty request, a `read` with no path or an `execute`
 * with no command, and the argument the tool actually sent reached nobody. The
 * generic card states the whole argument as text, so the call degrades to it rather
 * than claiming a request the frame never carried.
 *
 * An ADAPTER may still override the kind, and that is correct: a provider that knows
 * its own tool's scalar convention is the one layer allowed to read it.
 */
function acpSpecKind(facts: ACPToolFacts): ToolKind {
  if (facts.wireKind === 'unspecified' || facts.wireKind === 'other' || facts.scalarInput)
    return 'mcp'
  return facts.wireKind
}

/** The provider's own reading. `base()` = {@link acpBaseSpec} over the same facts. */
export type ACPToolCallAdapter = (facts: ACPToolFacts, base: () => ToolCallSpec) => ToolCallSpec

/** One call joined from the facts, the shared specification, and the provider's adapter. */
export function acpToolCall(rawTool: Record<string, unknown>, adapter: ACPToolCallAdapter | undefined, supplemental: unknown, completion?: MessageCompletion, row?: ACPToolRow): ToolCall {
  const facts = acpToolFacts(rawTool, supplemental, completion, row)
  const base = () => acpBaseSpec(facts)
  const spec = adapter ? adapter(facts, base) : base()
  const name = pickString(rawTool, 'kind') || 'tool'
  const label = facts.wireKind === 'other' && name !== 'other' ? humanizeWireWord(name) : undefined
  // The generic trio folds to `mcp`: the uncategorized card states the server and
  // the tool, which a wrench and the word "Other" do not.
  // The request is REBUILT, not relabelled. `McpRequest` declares `server` and `tool`
  // and `GenericToolRequest` declares neither, so stamping the new kind over the old
  // request asserted two fields that an adapter answering `{ kind: 'unspecified', request:
  // { args } }` never supplied -- and `mcpToolCallDisplayName` then drew the header
  // as the string `undefined`. The shared card states both, so the spread keeps them.
  const folded = spec.kind === 'unspecified' || spec.kind === 'other'
    ? { ...spec, kind: 'mcp' as const, request: { server: '', tool: '', ...spec.request } }
    : spec
  const rest = folded
  // The protocol carries no tool NAME, so an adapter that knows the registry id
  // states it: the shared build had only the wire kind, which identifies nothing.
  // `createToolCall` applies the specification's own name over this one and ignores an
  // undefined, so no strip is needed here.
  // The frame's TITLE is the call's own header word, which an execute row shows
  // when its description states none.
  // Pictures ride the envelope for every kind whose result is TYPED; the generic
  // trio carries them inside `result.content` instead, which is what
  // `ToolCallBase.images` states. Attaching them here rather than per case is
  // why a `fetch` of an image URL or an `execute` that returned a screenshot
  // still reaches the image tab.
  const images = rest.images ?? (isGenericKind(folded.kind) ? [] : facts.images)
  // The specification's own label wins; absent on both means no label, which the
  // exact-optional rule spells by omitting the key rather than writing `undefined`.
  const callLabel = folded.label ?? label
  return createToolCall(
    { id: pickString(rawTool, 'toolCallId'), name, lifecycle: facts.lifecycle },
    { title: acpCallTitle(facts), ...rest, images, ...(callLabel !== undefined ? { label: callLabel } : {}) },
  )
}

/** Whether this row needs its result side resolved, read from the TYPED request fields. */
export function acpToolCallNeedsResult(tool: Record<string, unknown>, adapter: ACPToolCallAdapter | undefined, supplemental?: unknown): boolean {
  const facts = acpToolFacts(tool, supplemental)
  const base = () => acpBaseSpec(facts)
  return acpSpecNeedsResult(adapter ? adapter(facts, base) : base(), facts)
}

/**
 * The kind's own test, over the request the build produced.
 *
 * Generic over the kind: `spec.kind` and `spec.request` stay one correlated pair.
 * The table gives the entry for that kind, and `needsResult` takes its request.
 * The caller's union satisfies the parameter member by member. An `if`-chain over the
 * kind cast the typed request back to `Record<string, unknown>` to ask the same
 * questions -- the one cast that discards exactly what the typed table exists to
 * create -- and it re-listed the kind set a third time.
 */
function acpSpecNeedsResult<K extends ToolKind>(spec: ToolCallSpecVariant<K>, facts: ACPToolFacts): boolean {
  const needsResult = ACP_SPEC_READERS[spec.kind].needsResult
  // The ARGUMENTS, not the typed request: every request this build produces
  // carries at least one key, so asking the request answers "no" for every call.
  return needsResult ? needsResult(spec.request, facts) : Object.keys(facts.args).length === 0
}

/** Collect everything the shared build and the adapters read, in one pass. */
export function acpToolFacts(rawTool: Record<string, unknown>, supplemental?: unknown, completion?: MessageCompletion, place?: ACPToolRow): ACPToolFacts {
  const extra = acpToolSupplement(rawTool, supplemental)
  // The retained outcome stays a FACT of its own: the shared derivation owns the
  // precedence between it and the frame's own word, so nothing here rewrites the
  // frame before the payload readers see it.
  const outcome = retainedOutcome(completion)
  let tool: Record<string, unknown> = rawTool
  // An absent or empty kind states no kind at all, so it reads as `unspecified`.
  // `toolKind('')` answers `other`, which means "a kind word LeapMux does not know",
  // and each reader of that answer then drew the empty word as the header and as the
  // card's tool name.
  const rawKind = pickString(tool, 'kind')
  const wireKind = toolKind(rawKind || undefined)
  // Read BEFORE the kind is chosen, because the kind now depends on it: a known kind
  // whose input is a scalar cannot fill its typed request. The test itself needs no
  // kind, so there is no cycle.
  const scalarInput = hasScalarToolInput(tool)
  const uncategorized = wireKind === 'other' || wireKind === 'unspecified' || scalarInput
  const kind = uncategorized ? 'mcp' as const : wireKind
  const { args, text: argsText } = acpToolInput(tool, kind)
  // Write the RECOVERED arguments back onto the frame, so every later read of the
  // request key sees the path that came from `locations`. A NON-object input is left
  // exactly as the tool sent it: overwriting it with `{}` is what used to lose a
  // scalar, and `collectAcpToolText` reads that key for a bare string. Only the
  // string case was excluded before, so a number, a boolean and an array were lost.
  if (isObject(tool[ACP_SUPPLEMENT_REQUEST.RawInput]) && tool[ACP_SUPPLEMENT_REQUEST.RawInput] !== args)
    tool = { ...tool, [ACP_SUPPLEMENT_REQUEST.RawInput]: args }
  const frameStatus = toolCallStatus(pickString(tool, 'status'))
  const finished = acpToolFinished(tool, completion)
  const resultFrameLanded = isFinishedToolCallStatus(frameStatus)
  const lifecycle: ToolCallLifecycleFacts = {
    frameStatus,
    providerOutcome: null,
    retainedOutcome: outcome,
    rowFinal: finished,
    resultFrameLanded,
  }
  // The DERIVED word, for the payload ladder: a retained row whose turn ended
  // shapes its body as a finished call's, which is what the old in-frame override
  // expressed by rewriting the status before any reader saw it.
  const status = deriveToolCallStatus(lifecycle, resultFrameLanded)

  const content = flattenAcpContent(tool.content)
  const text = collectAcpToolTextFromContent(content, tool.rawOutput, { rawObjects: uncategorized })
  const images = acpImagesFromContent(content, pickObject(tool, ACP_SUPPLEMENT_REQUEST.RawInput))
  const structuredJson = tool.rawOutput !== undefined ? prettifyJson(tool.rawOutput) : undefined
  return {
    tool,
    extra,
    wireKind,
    args,
    ...(argsText !== undefined ? { argsText } : {}),
    scalarInput,
    status,
    retained: outcome,
    lifecycle,
    finished,
    text,
    content,
    ...(structuredJson !== undefined ? { structuredJson } : {}),
    images,
    terminals: acpTerminalResults(tool, extra),
    place: place ?? {},
  }
}

/**
 * Whether the call states an input that is NOT an object.
 *
 * `null` and an absent key both answer no: they state no arguments, which every
 * typed request already handles.
 */
function hasScalarToolInput(tool: Record<string, unknown>): boolean {
  const raw = tool[ACP_SUPPLEMENT_REQUEST.RawInput]
  return raw !== undefined && raw !== null && !isObject(raw)
}

/** Resolve historical results that omit fields from the matching request. */
export function resolveACPToolCall(tool: Record<string, unknown>, request?: Record<string, unknown>): Record<string, unknown> {
  if (!request || !tool.toolCallId || request.toolCallId !== tool.toolCallId)
    return tool
  const fields = Object.fromEntries(Object.entries(tool).filter(([, value]) => value !== undefined && value !== null))
  const resolved = { ...request, ...fields }
  const previousInput = pickObject(request, ACP_SUPPLEMENT_REQUEST.RawInput)
  const currentInput = pickObject(tool, ACP_SUPPLEMENT_REQUEST.RawInput)
  if (previousInput && currentInput)
    resolved[ACP_SUPPLEMENT_REQUEST.RawInput] = { ...previousInput, ...currentInput }
  return resolved
}

/**
 * Whether one FRAME states that its call ended, with the turn's own outcome folded in.
 *
 * A provider ADAPTER must read `facts.finished` and never call this itself. The facts
 * carry the completion; a bare call here does not, so a row the turn retained after the
 * provider stopped sending reads as still running -- and its payload then draws the
 * request with the answer dropped. This stays exported for the row and span readers,
 * which hold a frame and no facts.
 */
export function acpToolFinished(tool: Record<string, unknown>, completion?: MessageCompletion): boolean {
  return retainedRowIsFinal(completion)
    || isFinishedToolCallStatus(toolCallStatus(pickString(tool, 'status')))
}

/**
 * The facts of one call RE-READ under a kind a provider's own table states.
 *
 * A provider that renames a tool's arguments or reclassifies its kind builds a new
 * frame and asks the shared table for that kind's payload. Everything `acpToolFacts`
 * derives FROM the kind or FROM the frame has to be derived again, and a hand-written
 * clone of the facts derived none of it:
 *
 *   - `args` skipped {@link acpToolInput}, so the `locations` path recovery never ran
 *     for a file tool whose own arguments state no path.
 *   - `text` stayed the text collected at the ORIGINAL kind, and a provider that
 *     recovered the call's real output from its own record kept the stale empty
 *     string beside the frame that holds the output.
 *   - `images` stayed the pictures of the original `rawInput`.
 *
 * `tool` carries the frame the provider built, with the new arguments already under
 * the request key; `kind` is what the frame now claims to be.
 */
export function acpRemapFacts(facts: ACPToolFacts, remap: { tool: Record<string, unknown>, kind: ToolKind }): ACPToolFacts {
  const uncategorized = remap.kind === 'other' || remap.kind === 'unspecified' || hasScalarToolInput(remap.tool)
  const framed: Record<string, unknown> = { ...remap.tool, kind: remap.kind }
  const { args, text: argsText } = acpToolInput(framed, remap.kind)
  // The same rule the first pass applies: the recovered object goes back on the
  // frame, and a non-object input stays exactly as the tool sent it.
  const tool = isObject(framed[ACP_SUPPLEMENT_REQUEST.RawInput])
    ? { ...framed, [ACP_SUPPLEMENT_REQUEST.RawInput]: args }
    : framed
  const content = flattenAcpContent(tool.content)
  const structuredJson = tool.rawOutput !== undefined ? prettifyJson(tool.rawOutput) : undefined
  return {
    ...facts,
    tool,
    args,
    ...(argsText !== undefined ? { argsText } : {}),
    scalarInput: hasScalarToolInput(tool),
    text: collectAcpToolTextFromContent(content, tool.rawOutput, { rawObjects: uncategorized }),
    content,
    ...(structuredJson !== undefined ? { structuredJson } : {}),
    images: acpImagesFromContent(content, pickObject(tool, ACP_SUPPLEMENT_REQUEST.RawInput)),
  }
}

/**
 * A call's arguments in the two forms its readers need.
 *
 * ACP declares `rawInput` as arbitrary JSON, and the two readers want different
 * things from it. A TYPED reader indexes it as an object (`args.filePath`), so a
 * scalar has no field to land in. The GENERIC card draws the whole argument as text,
 * whatever its JSON type. Separating the halves is what lets a scalar reach the
 * reader: it used to be replaced by `{}` and disappear, so a tool that sent one bare
 * string drew a card with no arguments at all.
 */
interface NormalizedToolInput {
  /** The object a typed reader indexes. EMPTY when the tool sent no object. */
  args: Record<string, unknown>
  /** The words the generic card draws. Absent when the call states no arguments. */
  text?: string
}

/**
 * The tool INPUT of one call, in both forms, with the file path recovered from
 * `locations`.
 *
 * A file tool that states no path in its own arguments often lists the file it touched
 * under `locations` instead. Exactly ONE distinct path there is the call's target.
 * Several paths are an ambiguity that this refuses to resolve, because picking one
 * would show a file that the call may not have touched.
 *
 * `prettifyArgsJson` is the ONE formatter, for the object form and the scalar form
 * alike: a bare string reformats to itself, so the generic card states the argument
 * the tool sent rather than a quoted JSON spelling of it.
 */
function acpToolInput(tool: Record<string, unknown>, kind: ToolKind): NormalizedToolInput {
  // A non-object input has no field a typed reader can index, so it travels as TEXT
  // alone and `args` stays empty.
  //
  // `null` and an absent key are NOT that case, and the difference matters: they
  // state no arguments, and an EMPTY object still takes the `locations` recovery
  // below. A file tool that lists its target there and states no arguments of its own
  // is common, and a reader that treated an absent input as a scalar lost every one
  // of those paths.
  if (hasScalarToolInput(tool)) {
    // An empty prettification states no argument at all, so the key stays absent.
    const text = prettifyArgsJson(tool[ACP_SUPPLEMENT_REQUEST.RawInput])
    return { args: {}, ...(text ? { text } : {}) }
  }
  const args = argsWithRecoveredPath(pickObject(tool, ACP_SUPPLEMENT_REQUEST.RawInput) ?? {}, tool, kind)
  const text = prettifyArgsJson(args)
  return { args, ...(text ? { text } : {}) }
}

/** The object input, plus the `locations` path when the call's own arguments omit one. */
function argsWithRecoveredPath(input: Record<string, unknown>, tool: Record<string, unknown>, kind: ToolKind): Record<string, unknown> {
  if (!FILE_TARGET_KINDS.has(kind) || pickFirstString(input, TOOL_FILE_PATH_KEYS))
    return input
  const paths = Array.isArray(tool.locations)
    ? [...new Set(tool.locations.filter(isObject).map(location => pickString(location, 'path')).filter(Boolean))]
    : []
  return paths.length === 1 ? { ...input, filePath: paths[0] } : input
}

export function parsedACPToolCall(parsed: unknown): Record<string, unknown> | null {
  return isObject(parsed) && (parsed.sessionUpdate === ACP_SESSION_UPDATE.TOOL_CALL || parsed.sessionUpdate === ACP_SESSION_UPDATE.TOOL_CALL_UPDATE)
    ? parsed
    : null
}

/** Supplemental snapshots cannot change the message identity or its completion state. */
export function resolveACPMessage(parsed: ParsedMessageContent): Record<string, unknown> | undefined {
  const original = unwrapACPResult(parsed.parentObject)
  const supplemental = original ? acpToolSupplement(original, parsed.supplementalContent) : undefined
  if (!original || !supplemental)
    return original
  const protocol = acpSupplementProtocol(supplemental)
  const resolved = { ...protocol, ...original }
  // The base of the `rawInput` merge, read BEFORE the request keys land: the frame's
  // own arguments, or the protocol payload's when the frame states none. The worker
  // merges from exactly that, and reading the untouched frame here instead dropped
  // every protocol-supplied argument -- so a read row drew with no file path while
  // the worker's own extractors had one.
  const baseInput = pickObject(resolved, ACP_SUPPLEMENT_REQUEST.RawInput)
  // `hasOwn`, never `in`: `in` walks the prototype chain, so a protocol key named
  // `toString` or `constructor` reads as "the frame already carries it" for EVERY
  // plain object and the whole resolve answers unchanged. The worker's map lookup has
  // no prototype chain, so the two `changed` tests were not the same predicate.
  let changed = Object.keys(protocol ?? {}).some(key => !Object.hasOwn(original, key))
  // These fields come from later ACP request updates. Native records stay in supplemental content.
  for (const key of Object.values(ACP_SUPPLEMENT_REQUEST)) {
    if (Object.hasOwn(supplemental, key)) {
      resolved[key] = supplemental[key]
      changed = true
    }
  }
  const supplementalInput = pickObject(supplemental, ACP_SUPPLEMENT_REQUEST.RawInput)
  if (baseInput && supplementalInput)
    resolved[ACP_SUPPLEMENT_REQUEST.RawInput] = { ...baseInput, ...supplementalInput }
  // Nothing new: answer the frame ITSELF, as the worker does. A caller that resolves
  // every row on every pass then allocates nothing for the rows that carry no join.
  return changed ? resolved : original
}
