import type { LucideIcon } from 'lucide-solid'
import type { Accessor, JSX } from 'solid-js'
import type { MessageUiKey } from '../../messageUiKeys'
import type { ToolSpanRowPosition } from '../../model/row'
import type { ToolCallVariant } from '../../model/toolCall'
import type { ToolKind } from '../../model/toolKind'
import type { ToolResultByKind } from '../../model/tools'
import type { ToolResultRenderContext } from '../../renderContext'
import { typedResult } from '../../model/toolCall'

/** A call whose result, when present, is the kind's own payload. Failed and unparsed results are stripped BEFORE any hook runs. */
export type ParsedCall<K extends ToolKind> = Omit<ToolCallVariant<K>, 'result'> & { result?: ToolResultByKind[K] }
/** A call that carries the kind's own result. `result()` and `resultMeta()` take this alone. */
export type ResolvedCall<K extends ToolKind> = Omit<ToolCallVariant<K>, 'result'> & { result: ToolResultByKind[K] }

/**
 * The call as a REQUEST-side hook reads it: what the call asked, and its identity.
 *
 * No member states the result or how the call ended. The request row draws these
 * hooks, and a request row never reads its result: it shows, and reports its height,
 * before the result exists. So a hook typed over this cannot change the request row
 * when the result lands. A row that draws both sides (a result row with no request
 * row beside it) passes its whole call, which this type accepts.
 *
 * The members are LISTED, not excluded: a member that a later change adds to the call
 * reaches no request-side hook until someone lists it here and decides that the
 * request states it.
 */
export type RequestSideCall<K extends ToolKind> = Pick<ToolCallVariant<K>, 'id' | 'name' | 'title' | 'label' | 'icon' | 'metadata' | 'kind' | 'request'>

/** The call with its failed and unparsed results stripped, so a kind hook reads its own payload alone. */
export function parsedCall<K extends ToolKind>(call: ToolCallVariant<K>): ParsedCall<K> {
  const { result, ...rest } = call
  const parsed = typedResult({ kind: call.kind, result })
  // A stripped or empty slot re-applies as ABSENT rather than an explicitly
  // undefined key, which is the spelling the hooks' optional reads expect.
  return parsed === undefined
    ? rest
    : { ...rest, result: parsed }
}

/**
 * The parsed call when its result slot holds the kind's own payload.
 *
 * Undefined when the slot is empty or holds a brand every hook strips, which is the
 * answer a request row and an unfinished call give. Built rather than narrowed: the
 * slot's type is a union no `!== undefined` check can carry out of the object, and the
 * spread states the pair -- every field but `result` from the call, `result` from the
 * stripped slot -- the way {@link parsedCall} does.
 */
export function resolvedCall<K extends ToolKind>(call: ToolCallVariant<K>): ResolvedCall<K> | undefined {
  const result = typedResult(call)
  return result === undefined
    ? undefined
    : { ...call, result }
}

/**
 * What one mounted row hands each drawing hook.
 *
 * It carries {@link ToolSpanRowPosition} rather than a flat `role` plus a boolean, so
 * the rule the row model states -- a row is never its own sibling -- reaches the shared
 * renderers that actually branch on it. Restating the fields here let
 * `{role: 'request', hasRequestRow: true}` back in one hop above every reader.
 *
 * It states nothing about the result row: a request row never learns whether its
 * result exists.
 */
export type ToolRowView = ToolSpanRowPosition & {
  context: ToolResultRenderContext | undefined
  expanded: Accessor<boolean>
  setExpanded: (value: boolean) => void
  /** How many images of this MESSAGE are numbered before the ones a result draws inline. Only the generic renderer reads it. */
  imageIndexOffset: number
  /** A summary that clips in the DOM says so, and the header then offers Expand. */
  onSummaryOverflow: (overflowing: boolean) => void
}

/** What the toolbar may offer for one side of one call. Pure data plus a lazy getter. */
export interface ToolKindMeta {
  collapsible: boolean
  /** The row draws a diff, so the toolbar offers the split/unified toggle. */
  hasDiff: boolean
  /** Lazy. Null when nothing is copyable. The caller builds it at most once, once it is final. */
  copyableContent: () => string | null
  /**
   * False while `copyableContent` states a stand-in for text that waits on work in
   * a worker, such as an edit's diff. `copyableContent` asks for that work itself.
   * Both getters are reactive: a memo that reads either one while the work waits
   * re-runs when it lands. The stand-in is null exactly when the final text is.
   * The caller keeps a stand-in only until this answers true, and then builds the
   * text again. Absent means final.
   */
  copyableFinal?: () => boolean
  copyLabel?: string
  expandLabel?: string
  /** The scroll-rail snippet. Defaults to `copyableContent`. */
  previewText?: () => string | null
}

export interface ToolKindRenderer<K extends ToolKind> {
  icon: LucideIcon
  /** One human noun: `Read`, `Execute`. */
  label: string
  /** The tool NAME leads the label (the generic trio). */
  nameLeads?: true
  /**
   * Whether THIS result draws its own outcome, so the shared outcome header stays away.
   *
   * A test over the resolved call, not a flag. Each kind that answers it draws the
   * outcome out of its RESULT -- one header per agent, one per command, one for the
   * task's own state word -- and none of those exists when the result holds no agent,
   * no command, or no state word. A flat `true` suppressed the shared header over
   * those bodies as well, so a failed launch whose agent state never arrived drew its
   * title and then nothing.
   */
  statesOwnOutcome?: (call: ResolvedCall<K>) => boolean
  /** The expand key a row that does NOT draw the result uses. Agent alone: AGENT_PROMPT. */
  requestExpandUiKey?: MessageUiKey
  /** The words the shared outcome header states for this call, when the kind words an outcome of its own. */
  outcomeTitle?: (call: ParsedCall<K>) => string
  // Property functions, not methods: `ts/method-signature-style` rejects the method
  // form, and its parameters are contravariant -- so `ToolKindRenderer<'read'>` is NOT
  // assignable to `ToolKindRenderer<ToolKind>`. `dispatchToolCall` in `./index` closes
  // the gap, over a table that is total and keyed by the same `kind` the call holds,
  // and it is the one place a renderer and a call meet.
  /**
   * The words the row heads itself with.
   *
   * ONE precedence, which twenty-three of the thirty kinds compose exactly:
   *
   * 1. What the REQUEST asked about, worded by the kind -- the file, the pattern, the
   *    query. This is what a reader recognizes the row by.
   * 2. `call.title`, the words the provider's own frame carried.
   * 3. The kind's {@link label}, so no row draws a bare icon and no words.
   *
   * `fileChangesTitle(...) ?? call.title ?? 'Edit'` is the shape. Every step must stay
   * reachable: a last resort spelled INSIDE step 1 -- `description || 'Task'` -- makes
   * step 2 dead, and the frame's own title then never reaches the header.
   *
   * The generic trio exchanges the first two steps, because {@link nameLeads} makes
   * the tool's own name the heading. It is the one exception.
   */
  title: (call: ParsedCall<K>, context: ToolResultRenderContext | undefined) => JSX.Element | string
  /** One-liners above the body border: the command line, extra search paths. */
  summary?: (call: RequestSideCall<K>, view: ToolRowView) => JSX.Element | null
  /**
   * The request-side body that the row which draws the request ALWAYS draws, whether
   * the call runs or has ended: the question options, the agent prompt that Expand
   * shows. It reads no result, so it draws the same before and after the result lands.
   */
  request?: (call: RequestSideCall<K>, view: ToolRowView) => JSX.Element | null
  /**
   * The preview of a call that still runs: what it asked, until its result answers. The
   * PENDING row draws it -- a row of its own that follows the request row while the call
   * runs, and that leaves when the result row joins the list. The requested diff, the
   * to-do list the call carries, the agent prompt, the generic arguments.
   *
   * Absent for a kind whose running call shows nothing beyond its request row.
   */
  pending?: (call: RequestSideCall<K>, view: ToolRowView) => JSX.Element | null
  /** Whether the pending preview clips content its request can expand. */
  pendingCollapsible?: (call: RequestSideCall<K>) => boolean
  /** The result body. Composes shared `results/*` components. */
  result: (call: ResolvedCall<K>, view: ToolRowView) => JSX.Element | null
  /**
   * What the row that draws the result shows above it when the result is NOT the kind's
   * own payload: words the model could not read as that payload, a failure, or no result.
   * `answered` is false for a call that carries no result at all: the turn stopped it, or
   * it never answered. A file change that ended with no typed result and did not fail
   * states the changes it asked for, under the heading of requested changes.
   *
   * A kind that previews a running call (`pending`) must declare it, even as a hook that
   * draws nothing, because no pending row is left to show the preview once the call ended
   * (results/tools/index.test.tsx enforces this). Absent for a kind with no preview that
   * adds nothing.
   */
  withoutTypedResult?: (call: ParsedCall<K>, view: ToolRowView, answered: boolean) => JSX.Element | null
  /** What the toolbar adds for {@link withoutTypedResult}. Absent means nothing. */
  withoutTypedResultMeta?: (call: ParsedCall<K>) => Partial<ToolKindMeta>
  /** What the toolbar offers a row that shows the request alone. Absent means nothing. */
  requestMeta?: (call: RequestSideCall<K>) => Partial<ToolKindMeta>
  /** What the toolbar offers a row that draws the result. */
  resultMeta: (call: ResolvedCall<K>) => ToolKindMeta
}

/**
 * The `withoutTypedResult` hook of a kind whose pending row states what a call asked.
 *
 * A call that ended with NO result at all -- the turn stopped it, or it never answered --
 * states that preview on the row that ended it, as its pending row did while it ran: the
 * pending row has left, and a request row of a span never draws it (only a call with no
 * span draws it inline, see ToolResultRenderContext.loneCall). A result of any form
 * answers for the call instead.
 */
export function previewWithoutResult<K extends ToolKind>(pending: (call: RequestSideCall<K>, view: ToolRowView) => JSX.Element | null): NonNullable<ToolKindRenderer<K>['withoutTypedResult']> {
  return (call, view, answered) => answered
    ? null
    : pending(call, view)
}
