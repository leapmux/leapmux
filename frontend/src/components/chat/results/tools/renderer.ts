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

/** The call with its failed and unparsed results stripped, so a kind hook reads its own payload alone. */
export function parsedCall<K extends ToolKind>(call: ToolCallVariant<K>): ParsedCall<K> {
  const { result, ...rest } = call
  const parsed = typedResult({ kind: call.kind, result })
  // A stripped or empty slot re-applies as ABSENT rather than an explicitly
  // undefined key, which is the spelling the hooks' optional reads expect.
  return parsed === undefined ? rest : { ...rest, result: parsed }
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
  return result === undefined ? undefined : { ...call, result }
}

/**
 * What one mounted row hands each drawing hook.
 *
 * It carries {@link ToolSpanRowPosition} rather than a flat `role` plus two booleans, so
 * the rule the row model states -- a row is never its own sibling -- reaches the shared
 * renderers that actually branch on it. Restating the three fields here let
 * `{role: 'request', hasRequestRow: true}` back in one hop above every reader.
 */
export type ToolRowView = ToolSpanRowPosition & {
  context: ToolResultRenderContext | undefined
  drawsResult: boolean
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
  /** Lazy. Null when nothing is copyable. The caller builds it at most once. */
  copyableContent: () => string | null
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
  summary?: (call: ParsedCall<K>, view: ToolRowView) => JSX.Element | null
  /** Request-side body: the expanded command, the requested diff, the question options, the agent prompt. */
  request?: (call: ParsedCall<K>, view: ToolRowView) => JSX.Element | null
  /** The result body. Composes shared `results/*` components. */
  result: (call: ResolvedCall<K>, view: ToolRowView) => JSX.Element | null
  /** What the toolbar offers a row that shows the request alone. Absent means nothing. */
  requestMeta?: (call: ParsedCall<K>, hasResult: boolean) => Partial<ToolKindMeta>
  /** What the toolbar offers a row that draws the result. */
  resultMeta: (call: ResolvedCall<K>) => ToolKindMeta
}
