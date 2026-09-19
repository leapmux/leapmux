import type { McpContentItem } from './mcpToolCall'
import type { ToolKind } from './toolKind'
import type { ToolMetadataItem } from './toolMetadata'
import type { ToolRowStatus } from './toolRowStatus'
import type { ToolRequests, ToolResults } from './tools'
import type { ImageResultSource } from '~/lib/imageBlocks'
import { isGenericResult } from './tools/generic'

/**
 * The glyph one CALL asks for, when the tool it names is narrower than its kind.
 *
 * A closed set, so the renderer's map over it is exhaustive and a hint no icon
 * answers cannot reach the screen. The IR states what the tool DOES rather than
 * which component draws it: the icon set is a rendering decision, and swapping
 * one glyph for another must not edit an extractor.
 *
 * Absent means the kind's own icon, which is the common case -- a Bash call on
 * Claude and a shell call anywhere else then draw the same glyph.
 */
export type ToolIconHint
  /** Reads back a checklist. */
  = | 'checklist'
  /** Ends something that is running; not the wait its kind draws. */
    | 'stop'
  /** Enters plan mode. */
    | 'plan-enter'
  /** Leaves plan mode. */
    | 'plan-exit'
  /** Fires on a request rather than on a clock. */
    | 'webhook'
  /** Moves between worktrees, which is a branch. */
    | 'branch'
  /** Answers in JSON. */
    | 'json'

/** A result that IS prose. A kind whose answer is words declares this as its result. Not a fallback. */
export interface ProseResult { text: string, format: 'plain' | 'markdown' }

/** The ONE fallback: the call completed, and this build could not read the payload into the kind's shape. A test counts it. */
export interface UnparsedResult { readonly unparsed: true, text: string }

/** A call that ended WITHOUT its payload (failed, cancelled, declined) and stated only text. The outcome word is `status`. */
export interface FailedResult { readonly failure: true, text: string }

export type ToolResultOf<K extends ToolKind> = ToolResults[K] | FailedResult | UnparsedResult

export type GenericToolKind = '' | 'other' | 'mcp'

/**
 * The pictures one KIND may carry beside its typed result.
 *
 * The generic trio carries none of its own -- invariant I6. Its result declares
 * `content`, the block list every picture of such a call rides in, and a second copy
 * on the call drew the same picture twice.
 */
export type ToolCallImages<K extends ToolKind> = K extends GenericToolKind ? readonly [] : readonly ImageResultSource[]

/**
 * What a call produced BESIDE its typed result.
 *
 * Declared on the FINISHED lifecycle variants alone, and that is what makes the
 * request/result distinction structural: a row that has not answered yet has nowhere
 * to put a truncation notice, a picture or a rich content block, so no provider can
 * state one there and no renderer has to gate it out again.
 */
export interface ToolResultSide<K extends ToolKind> {
  /** Pictures beside the typed result. EMPTY for the generic trio, whose pictures ride in `result.content`. */
  images: ToolCallImages<K>
  /** Rich content beside a recognized result. Copilot alone sends it. */
  extraContent?: readonly McpContentItem[]
  /** The provider kept only part of what the call produced, and no body states it. */
  truncated?: boolean
}

/**
 * The result side of a call that produced nothing.
 *
 * `images` stays PRESENT and empty rather than absent. Every reader of a call reads
 * it unconditionally, and an absent member would make `call.images` undefined across
 * the whole union for no gain: an empty list already states "no pictures", and a
 * producer cannot put one here.
 */
export interface NoResultSide {
  images: readonly []
  extraContent?: undefined
  truncated?: undefined
}

/**
 * A call with NO result: the provider states no status, the call is queued, or it is
 * still running. Invariant I1.
 *
 * `ToolMessage` draws the live output the worker broadcasts only while the row is in
 * flight AND states no result, so a reader that filled a result early replaced the
 * streaming tail with an empty card.
 */
export interface UnfinishedCall extends NoResultSide {
  status: '' | 'pending' | 'in_progress'
  result?: undefined
}

/**
 * A call that ran to the end. Invariant I2: it HAS a result.
 *
 * The result is the kind's own payload, or the text this build could not read into
 * that shape. Never a {@link FailedResult}: a completed call did not fail, and the
 * two brands draw the same pixels, so nothing but this states the difference.
 */
export interface CompletedCall<K extends ToolKind> extends ToolResultSide<K> {
  status: 'completed'
  result: ToolResults[K] | UnparsedResult
}

/**
 * A call that ran and FAILED.
 *
 * The result is the reason it gave, or the kind's own record of what it produced
 * before it failed -- nine kinds answer a typed result here, and `execute` is the one
 * that owns its own failure in every provider (see the I5 paragraph in
 * `test-support/toolVocabulary.ts`).
 *
 * Never an {@link UnparsedResult}. Invariant I4: that brand states "the call
 * completed and this build could not read the payload", which a failed call did not
 * do. A provider with only unreadable text for a failure states {@link failedResult}.
 */
export interface FailedCall<K extends ToolKind> extends ToolResultSide<K> {
  status: 'failed'
  result?: ToolResults[K] | FailedResult
}

/**
 * A call a reader REFUSED, so the tool never ran.
 *
 * The typed half is narrowed to the kinds whose result IS prose, and that states the
 * rule the words alone could not: a declined call produced no payload, because it
 * produced nothing at all. What it may carry is the refusal, and a refusal is words.
 * `switch_mode` is the case in the corpus -- the reader sends a plan back with
 * feedback -- and `Extract` answers `never` for every kind whose result is a file, a
 * match list or an exit code, so a declined `read` can state a {@link FailedResult}
 * and nothing else.
 */
export interface DeclinedCall<K extends ToolKind> extends ToolResultSide<K> {
  status: 'declined'
  result?: Extract<ToolResults[K], ProseResult> | FailedResult
}

/**
 * A call the turn CUT.
 *
 * It keeps whatever partial body it printed, in any of the three shapes: the kind's
 * own payload as far as it got, the reason, or the text no kind's shape reads. This
 * is the one status that admits all three, and invariant I4 admits the unparsed brand
 * here for exactly that reason.
 */
export interface CancelledCall<K extends ToolKind> extends ToolResultSide<K> {
  status: 'cancelled'
  result?: ToolResultOf<K>
}

/**
 * The status and the result of one call, as ONE correlated choice.
 *
 * A status and a result used to be two independent fields, so every pair compiled and
 * a runtime corpus walk was the only thing that reported the illegal ones. Here the
 * pair is the discriminant: `{ status: 'pending', result: … }` states a member that
 * does not exist, and `{ status: 'completed' }` omits a required one.
 */
export type ToolCallLifecycle<K extends ToolKind>
  = UnfinishedCall | CompletedCall<K> | FailedCall<K> | DeclinedCall<K> | CancelledCall<K>

/** The fields every kind shares, at every state of the call. */
export interface ToolCallCommon {
  /** The span id: the provider's own call id. '' for a synthetic one-row call. */
  id: string
  /** The provider's own tool name. The vocabulary tests walk it; a generic kind displays it. */
  name: string
  /** The provider's own header words, for a kind whose title the renderer cannot compose from the request. */
  title?: string
  /** The provider's display name for the tool, when the kind's word says less. */
  label?: string
  /** An icon for a tool narrower than its kind; absent means the kind's own. */
  icon?: ToolIconHint
  /**
   * Facts the REQUEST states, which the row draws at every state of the call.
   *
   * Request-owned on purpose: Claude's task type is the one producer, and a reader
   * must see it while the call is still running. A fact the RESULT states belongs in
   * that kind's own result shape, never here.
   */
  metadata?: ToolMetadataItem[]
  /**
   * Why this call drew as the generic row: the draft broke an invariant, and the
   * degrade is what still renders. Only {@link toolCall} supplies it, so a call that
   * carries none is either valid or a generic kind read legitimately -- never a
   * degrade nobody noticed.
   */
  degradation?: ToolCallDegradation
}

/**
 * What a degraded call gave up: the invariant it broke, and the kind it was
 * reading before the degrade answered the uncategorized row.
 *
 * An OBSERVABLE diagnostic, not a control input: the row stays renderable either
 * way, and tests, the dev overlay and the warning census read this to tell a
 * deliberate `other` from a frame this build read wrongly.
 */
export interface ToolCallDegradation {
  fault: ToolCallFault
  originalKind: ToolKind
}

export type ToolCallOf<K extends ToolKind> = ToolCallCommon & { kind: K, request: ToolRequests[K] } & ToolCallLifecycle<K>
export type ToolCallIR = { [K in ToolKind]: ToolCallOf<K> }[ToolKind]

/**
 * The kind-specific half of one call: what a per-kind extractor produces. The
 * envelope adds the rest.
 *
 * `name` is the one envelope field a payload may restate. A protocol that carries
 * no tool name gives the envelope none, and only the extractor that read the frame
 * knows the registry id the vocabulary tests walk.
 *
 * The STATUS is the envelope's, so a payload cannot correlate its result with it: the
 * pair meets for the first time inside {@link toolCall}, which is where the lifecycle
 * rules are checked. A payload therefore states any of the kind's result shapes, and
 * a draft that pairs the wrong one with the envelope's status degrades there.
 */
export type ToolCallPayload<K extends ToolKind>
  = { kind: K, request: ToolRequests[K], result?: ToolResultOf<K> }
    // `title` alone admits an EXPLICIT undefined, and no other dressing does: the ACP
    // wrapper joins a payload over its own frame-title default, and a provider whose
    // kind composes its own header (a todo count; a command row whose title repeats
    // the command) must CLEAR that default -- a key that is merely omitted lets the
    // frame's words stand. Every other slot rides only when stated.
    & Partial<Pick<ToolCallCommon, 'name' | 'label' | 'icon' | 'metadata'>> & { title?: string | undefined }
    & {
      images?: readonly ImageResultSource[]
      extraContent?: readonly McpContentItem[]
      truncated?: boolean
      /**
       * The outcome the PAYLOAD read, when the envelope's own status cannot state it.
       *
       * A frame says `completed` and its body says the call failed; a plan the reader
       * refused arrives as an error. The extractor that read the body is the only one
       * that knows, so it states the word here and {@link toolCall} applies it. It is
       * not a field of the call: `status` is the ONE outcome word a row carries.
       */
      statusOverride?: Exclude<ToolRowStatus, ''>
    }
/**
 * One payload of ONE of the kinds in `K`, as a union rather than an intersection.
 *
 * The sibling of {@link ToolCallOfKinds}, and it exists for the same reason:
 * `ToolCallPayload<ToolKind>` is a SINGLE object whose `request` is every kind's
 * request at once, and no real payload satisfies it -- so a builder that returned it
 * for a kind it computed at runtime could not hand the value back without an
 * `as unknown as`, which erased the request and the result along with the kind.
 * Distributing keeps the pair correlated: `ToolCallPayloadOf<'edit' | 'write'>` is one
 * payload or the other, never a mixture.
 */
export type ToolCallPayloadOf<K extends ToolKind> = { [P in K]: ToolCallPayload<P> }[K]

export type ToolCallPayloadIR = ToolCallPayloadOf<ToolKind>
export type ToolCallEnvelope = Pick<ToolCallCommon, 'id' | 'name'> & { status: ToolRowStatus }

/**
 * One call of ONE of the kinds in `K`, as a union rather than an intersection.
 *
 * `ToolCallOf<ToolKind>` is a single object whose `request` is every kind's request at
 * once, which no real call satisfies; this distributes, so `ToolCallOfKinds<ToolKind>`
 * IS {@link ToolCallIR} and `ToolCallOfKinds<'read'>` is still `ToolCallOf<'read'>`.
 * {@link toolCall} returns it, which is what lets a provider that builds a payload of
 * a kind it computed at runtime keep the result typed instead of casting it.
 */
export type ToolCallOfKinds<K extends ToolKind> = { [P in K]: ToolCallOf<P> }[K]

/** The four kinds whose request names the files the call operates on. */
export const FILE_CHANGE_KINDS = ['edit', 'write', 'delete', 'move'] as const
export type FileChangeKind = (typeof FILE_CHANGE_KINDS)[number]

const FILE_CHANGE_KIND_SET: ReadonlySet<string> = new Set(FILE_CHANGE_KINDS)

export function isFileChangeKind(kind: ToolKind): kind is FileChangeKind {
  return FILE_CHANGE_KIND_SET.has(kind)
}

/**
 * Why one draft is not a call. The reasons the LIFECYCLE and the KIND rules state,
 * as a closed set, so a test can name the case it exercises.
 */
export type ToolCallFault
  = | 'result-before-the-call-finished'
    | 'pictures-before-the-call-finished'
    | 'completed-with-no-result'
    | 'completed-with-a-failure-result'
    | 'failed-with-an-unparsed-result'
    | 'declined-with-a-typed-payload'
    | 'a-generic-kind-carries-its-own-images'
    | 'a-file-change-states-no-file'

/**
 * One draft after the envelope and the payload have joined and every default has
 * landed: the normalized shape both the invariants and the degrade read.
 *
 * The CHECK and the DEGRADE share it, which is why it is a named type: the degrade
 * used to re-derive the status and re-apply the defaults from the raw payload, so
 * the two could disagree about what the frame said. Deriving the draft once and
 * handing it to both is the rule.
 */
export interface NormalizedToolCallDraft<K extends ToolKind> {
  id: string
  name: string
  kind: K
  status: ToolRowStatus
  request: ToolRequests[K]
  result?: unknown
  images: readonly ImageResultSource[]
  extraContent?: readonly McpContentItem[]
  truncated?: boolean
  title?: string | undefined
  label?: string | undefined
  icon?: ToolIconHint | undefined
  metadata?: ToolMetadataItem[] | undefined
}

/** The built call, or the reason and the normalized draft behind the refusal. */
export type ToolCallBuild<K extends ToolKind>
  = { ok: true, call: ToolCallOfKinds<K> }
    | { ok: false, fault: ToolCallFault, draft: NormalizedToolCallDraft<K> }

/**
 * Join the provider's envelope with a kind's payload, and check the invariants.
 *
 * `images` defaults to none, and a payload's `statusOverride` wins over the
 * envelope's status. Both land here rather than in each provider, so every one of
 * them applies the rule the same way.
 *
 * This is the only function that produces a {@link ToolCallIR}, and it produces one
 * only for a draft that holds every invariant. A provider that wants the reason calls
 * it; {@link toolCall} degrades instead and is what the extractors use. The refused
 * member carries the NORMALIZED draft, so the degrade reads exactly what the check
 * read and repeats none of the joining.
 */
export function buildToolCall<K extends ToolKind>(envelope: ToolCallEnvelope, payload: ToolCallPayloadOf<K>): ToolCallBuild<K> {
  const { statusOverride, name, images, extraContent, truncated, ...rest } = payload
  // `name` and `images` are pulled OUT of the spread and re-applied. Both are
  // required on the call, and `exactOptionalPropertyTypes` used to be off, so a
  // payload that states `name: undefined` -- the natural spelling of "this frame
  // carries no tool name", which a wire value can still hand a builder -- erased the
  // envelope's own, and one that states `images: undefined` destroyed the default and
  // made `imagesForIR` throw on a spread of undefined, killing the whole row list.
  // Doing it here makes the mistake impossible for every provider instead of asking
  // nine payload builders to remember the strip. `extraContent` and `truncated` are
  // re-applied only when the payload states them, so a stated undefined lands as an
  // ABSENT key, which is the one spelling `toolCallFault` reads either way.
  const draft: NormalizedToolCallDraft<K> = {
    ...envelope,
    ...rest,
    name: name ?? envelope.name,
    images: images ?? [],
    status: statusOverride ?? envelope.status,
    ...(extraContent !== undefined ? { extraContent } : {}),
    ...(truncated !== undefined ? { truncated } : {}),
  }
  const fault = toolCallFault(draft)
  if (fault !== null)
    return { ok: false, fault, draft }
  // The invariants the check just walked ARE the lifecycle union's own rules, one for
  // one, and no narrowing carries a runtime answer back into the type system. This is
  // the single assertion the IR needs, and it stands on the check above it.
  return { ok: true, call: draft as ToolCallOfKinds<K> }
}

/**
 * One draft, before the invariants are known to hold.
 *
 * A built {@link ToolCallIR} satisfies it, which is what lets the provider corpus
 * walk a call back through {@link toolCallFault} rather than restating the rules.
 */
export interface ToolCallDraft {
  kind: ToolKind
  status: ToolRowStatus
  request: ToolRequests[ToolKind]
  result?: unknown
  images: readonly ImageResultSource[]
  extraContent?: readonly McpContentItem[]
  truncated?: boolean
}

/**
 * The first invariant one draft breaks, or null when it holds them all.
 *
 * THE catalogue of the rules. The lifecycle union states most of them in the type
 * system, and this states the same ones over a value that came from the wire, where
 * no type was ever checked. The historical numbering is kept so a reader who meets an
 * old `I3:` message in a test log finds the rule here:
 *
 * - I1 `result !== undefined` implies a finished status -- `result-before-the-call-finished`,
 *   and its result-side half `pictures-before-the-call-finished`.
 * - I2 `status === 'completed'` implies a result -- `completed-with-no-result`.
 * - I3 a FailedResult implies a failed, cancelled or declined status --
 *   `completed-with-a-failure-result`. The two brands draw the same pixels, so
 *   nothing but this states the difference between a call that completed and a call
 *   that says it did not.
 * - I4 an UnparsedResult implies a completed or cancelled status --
 *   `failed-with-an-unparsed-result`, and the declined half rides in
 *   `declined-with-a-typed-payload`.
 * - I6 a generic kind carries no pictures of its own -- `a-generic-kind-carries-its-own-images`.
 * - I7 a file-change kind states the file it changes -- `a-file-change-states-no-file`.
 *
 * I5 IS DELIBERATELY ABSENT, and the number stays open so a reader who meets an old
 * `I5:` message finds this paragraph. It stated that a completed `execute` whose one
 * command reports a non-zero exit code is illegal. It is legal, and two layers say so
 * on purpose. The TOOL CALL succeeded; the COMMAND it ran failed, and those are two
 * facts. ZCode's app-server reports a failed command as a successful tool call whose
 * content says `Exit code 3` (`zcode/extractors/execute.ts`), and
 * `results/commandResult.tsx` accommodates the pair at the renderer: `commandFailed`
 * reads the exit code and draws `Error (exit 3)`. A guard that refused the pair would
 * refuse the shape the codebase states.
 */
export function toolCallFault(draft: ToolCallDraft): ToolCallFault | null {
  const finished = draft.status === 'completed' || draft.status === 'failed'
    || draft.status === 'cancelled' || draft.status === 'declined'
  if (draft.result !== undefined && !finished)
    return 'result-before-the-call-finished'
  if (!finished && (draft.images.length > 0 || draft.extraContent !== undefined || draft.truncated !== undefined))
    return 'pictures-before-the-call-finished'
  if (draft.status === 'completed' && draft.result === undefined)
    return 'completed-with-no-result'
  if (draft.status === 'completed' && isFailedResult(draft.result))
    return 'completed-with-a-failure-result'
  if (draft.status === 'failed' && isUnparsedResult(draft.result))
    return 'failed-with-an-unparsed-result'
  // A declined call produced no payload, because it never ran. What it may carry is
  // the refusal, and a refusal is words -- so the kind's OWN result must be prose for
  // a typed one to be legal here. The kind is asked as well as the shape: a result
  // that merely looks like prose is not the `read` payload becoming legal.
  if (draft.status === 'declined' && draft.result !== undefined && !isFailedResult(draft.result)
    && !(isProseResultKind(draft.kind) && isProseResult(draft.result))) {
    return 'declined-with-a-typed-payload'
  }
  if (isGenericKind(draft.kind) && draft.images.length > 0)
    return 'a-generic-kind-carries-its-own-images'
  if (isFileChangeKind(draft.kind) && !statesAFile(draft.request))
    return 'a-file-change-states-no-file'
  return null
}

/**
 * The kinds whose result IS prose, as a VALUE the runtime check can read.
 *
 * `DeclinedCall` states the same set as a type, through `Extract<…, ProseResult>`,
 * and `PROSE_RESULT_KINDS_MATCH_THE_TYPES` in `./tools` fails to compile when the two
 * disagree. Without the list the runtime check was the looser of the pair: it read
 * the SHAPE alone, so a declined `read` carrying a prose-shaped object passed a check
 * the type refuses.
 */
export const PROSE_RESULT_KINDS = [
  'agents',
  'memory',
  'message',
  'report',
  'skill',
  'switch_mode',
  'think',
  'trigger',
  'wait',
] as const

export type ProseResultKind = (typeof PROSE_RESULT_KINDS)[number]

const PROSE_RESULT_KIND_SET: ReadonlySet<string> = new Set(PROSE_RESULT_KINDS)

export function isProseResultKind(kind: ToolKind): kind is ProseResultKind {
  return PROSE_RESULT_KIND_SET.has(kind)
}

/**
 * Whether a file-change request names the files it operates on. Invariant I7.
 *
 * The row composes its header from that list at EVERY state of the call, so a builder
 * that empties the list on a failure -- or that never filled it -- heads the row with
 * the operation word and nothing else, and the reader cannot tell which file the call
 * did not change. The request is the provider's own record of what the tool ASKED
 * for, and a failure never takes that away.
 *
 * A non-empty tuple (`readonly [FileEditDiff, ...FileEditDiff[]]`) would state the
 * first half of this in the type system, and it is impossible here. Each provider
 * builds its file-change request from a per-kind TABLE of total functions
 * (`defaultToolRequests.ts`, `ACP_TOOL_REQUEST_OVERRIDES`, and eight more), typed
 * `(args, facts) => ToolRequests[K]`. Those functions have no failure channel, so a
 * builder that read a frame with no file could only invent a change to satisfy the
 * tuple -- which states a file the tool never named. The empty-path half needs a
 * runtime check regardless, and `toolCall` degrades on both, so the whole rule lives
 * here rather than half in each of ten tables.
 */
function statesAFile(request: ToolRequests[ToolKind]): boolean {
  const changes = (request as { changes?: readonly { filePath?: string }[] }).changes
  return changes !== undefined && changes.length > 0 && changes.every(change => Boolean(change.filePath))
}

/**
 * The call, or the uncategorized row that states what the frame carried when the
 * draft broke an invariant.
 *
 * The degrade is not a repair. An invalid draft is a frame this build read wrongly,
 * and the generic row is the codebase's own word for that: it draws the tool name and
 * the arguments verbatim, which is everything the reader can still trust. The
 * alternative -- returning the draft anyway -- is what let a `pending` row answer with
 * a result and an `edit` row head itself with no file at all.
 *
 * A provider that can do better degrades EARLIER, at its own frame, where it still
 * knows what the wire said. That is the rule for a malformed file operation: build
 * the generic payload from the arguments instead of an `edit` with no changes.
 */
export function toolCall<K extends ToolKind>(envelope: ToolCallEnvelope, payload: ToolCallPayloadOf<K>): ToolCallOfKinds<K> | ToolCallOf<'other'> {
  const built = buildToolCall(envelope, payload)
  return built.ok ? built.call : degradedToolCall(built)
}

/**
 * The faults already reported, so one broken invariant warns ONCE per session
 * however many frames hit it.
 *
 * The set is capped by construction at the closed {@link ToolCallFault} union -- a
 * key per member and no more -- so a run away with malformed frames cannot flood the
 * console either. The CENSUS does not read this: tests count the
 * {@link ToolCallCommon.degradation} metadata, which every degraded call carries,
 * while the warning exists for the operator who is watching a live session.
 */
const reportedFaults = new Set<ToolCallFault>()

/** Report one degraded call, once per fault code. */
function warnDegraded(built: { fault: ToolCallFault, draft: { id: string, name: string, kind: ToolKind, status: ToolRowStatus } }): void {
  if (reportedFaults.has(built.fault))
    return
  reportedFaults.add(built.fault)
  console.warn('ToolCall degraded to the uncategorized row', {
    fault: built.fault,
    callId: built.draft.id,
    toolName: built.draft.name,
    originalKind: built.draft.kind,
    status: built.draft.status,
  })
}

/** Reset the degradation warning census. Test-only: a suite counts warnings per fault code. */
export function __resetToolCallWarningsForTest(): void {
  reportedFaults.clear()
}

/**
 * The uncategorized row for a draft that broke an invariant.
 *
 * It keeps the envelope, the arguments and the words, and drops what the fault says
 * cannot be true: the result of an unfinished call, and the pictures of a generic
 * kind. The status stays, because the reader must still see that the call ended.
 * Everything comes from the NORMALIZED draft the check refused, so the degrade
 * repeats none of the joining and cannot disagree with what was checked.
 */
function degradedToolCall<K extends ToolKind>(built: { fault: ToolCallFault, draft: NormalizedToolCallDraft<K> }): ToolCallOf<'other'> {
  warnDegraded(built)
  const { draft } = built
  const status = draft.status
  const finished = status === 'completed' || status === 'failed' || status === 'cancelled' || status === 'declined'
  // The generic trio's three results ARE the `other` kind's result, so a refused
  // draft of one keeps everything the tool produced. The generic-images fault is the
  // case: only the pictures broke the rule, and dropping the content blocks with them
  // would throw away the tool's whole answer.
  const kept = isGenericResult(draft.result) ? draft.result : undefined
  const text = faultText(draft, built.fault)
  // The draft's optional dressings ride along only when the payload stated one,
  // which the normalization already spelled as an absent key.
  const common = {
    id: draft.id,
    name: draft.name,
    status,
    kind: 'other' as const,
    request: { args: requestArgs(draft.request) },
    ...(draft.title !== undefined ? { title: draft.title } : {}),
    ...(draft.label !== undefined ? { label: draft.label } : {}),
    ...(draft.icon !== undefined ? { icon: draft.icon } : {}),
    ...(draft.metadata !== undefined ? { metadata: draft.metadata } : {}),
    degradation: { fault: built.fault, originalKind: draft.kind },
  }
  // An unfinished status keeps NO result: that is invariant I1, and the degrade
  // cannot restate the very rule it exists to enforce. `completed` needs one, so the
  // unparsed brand states the words the draft carried -- which is what that brand
  // means.
  if (!finished)
    return { ...common, status: status as UnfinishedCall['status'], images: [] }
  if (status === 'completed')
    return { ...common, status, result: kept ?? unparsedResult(text), images: [] }
  if (status === 'declined')
    return { ...common, status, result: failedResult(text), images: [] }
  // `failed` and `cancelled` both admit the kind's own result beside the failure
  // brand, so a kept generic body survives here too. `declined` above does not: the
  // tool never ran, so it produced no body to keep.
  return { ...common, status, result: kept ?? failedResult(text), images: [] }
}

/** The words a degraded row states: the draft's own result text, or the fault. */
function faultText(draft: { result?: unknown }, fault: ToolCallFault): string {
  const result = draft.result
  if (result !== undefined && typeof (result as { text?: unknown }).text === 'string')
    return (result as { text: string }).text
  return `This build could not read the call: ${fault.replaceAll('-', ' ')}.`
}

/** The arguments a degraded row prints, which is every field of the request it came from. */
function requestArgs(request: ToolRequests[ToolKind]): Record<string, unknown> {
  const args = (request as { args?: unknown }).args
  return typeof args === 'object' && args !== null && !Array.isArray(args)
    ? args as Record<string, unknown>
    : { ...request as object }
}

export function unparsedResult(text: string): UnparsedResult {
  return { unparsed: true, text }
}

export function failedResult(text: string): FailedResult {
  return { failure: true, text }
}

export function proseResult(text: string, format: ProseResult['format'] = 'plain'): ProseResult {
  return { text, format }
}

export function isUnparsedResult(r: unknown): r is UnparsedResult {
  // The VALUE, not the key: the brand is declared `readonly unparsed: true`, and a
  // presence test narrowed `{ unparsed: false }` to the branded type, which then
  // made `typedResult` discard a perfectly good payload.
  //
  // `text` is checked too, because the predicate promises the WHOLE type. Every
  // caller reads `.text` with no guard of its own, so a brand-only test handed
  // `hasMoreLinesThan` and `<PlainTextResult>` an undefined string.
  return typeof r === 'object' && r !== null
    && (r as { unparsed?: unknown }).unparsed === true
    && typeof (r as { text?: unknown }).text === 'string'
}

export function isFailedResult(r: unknown): r is FailedResult {
  return typeof r === 'object' && r !== null
    && (r as { failure?: unknown }).failure === true
    && typeof (r as { text?: unknown }).text === 'string'
}

/**
 * Whether one result IS prose: the shape a declined call may carry.
 *
 * Neither brand is prose. Both declare `text` and no `format`, and `declined` admits
 * a {@link FailedResult} through its own branch, so a test that accepted any `text`
 * would have let the unparsed brand through the one status that must refuse it.
 */
export function isProseResult(r: unknown): r is ProseResult {
  if (typeof r !== 'object' || r === null || isUnparsedResult(r) || isFailedResult(r))
    return false
  const format = (r as { format?: unknown }).format
  return typeof (r as { text?: unknown }).text === 'string' && (format === 'plain' || format === 'markdown')
}

/**
 * The kind's own payload, or undefined for no result, a failure, or an unparsed result.
 *
 * Takes the kind and the result alone rather than a whole call, so a provider test
 * can ask the question of a PAYLOAD the envelope has not joined yet. Those tests used
 * to invent an envelope around the payload to reach this, and the lifecycle union
 * refuses the invented one: a `completed` call declares a required result, which a
 * payload's optional one does not satisfy. `kind` stays the inference anchor, because
 * an indexed access cannot infer `K` on its own.
 *
 * `result` admits an explicit undefined because the lifecycle's own unfinished member
 * spells its absent result that way (`result?: undefined`), and this reads payloads
 * and calls alike.
 */
export function typedResult<K extends ToolKind>(call: { kind: K, result?: ToolResultOf<K> | undefined }): ToolResults[K] | undefined {
  const r = call.result
  return r === undefined || isUnparsedResult(r) || isFailedResult(r) ? undefined : r
}

/**
 * Whether one KIND is the generic trio's. Answers a boolean about the kind alone.
 *
 * Use it where a boolean is all the caller wants -- whether to carry pictures on the
 * call rather than in the result's content blocks. To narrow a CALL, take
 * {@link isGenericCall} instead: a predicate over the discriminant it was read from
 * can never narrow the value it came from, so a caller that tried had to assert the
 * call back afterwards.
 */
export function isGenericKind(kind: ToolKind): kind is GenericToolKind {
  return kind === '' || kind === 'other' || kind === 'mcp'
}

/** Whether one CALL is the generic trio's, narrowing the call itself. */
export function isGenericCall(call: ToolCallIR): call is ToolCallOfKinds<GenericToolKind> {
  return isGenericKind(call.kind)
}
