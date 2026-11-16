import type { Accessor } from 'solid-js'
import type { ClassifiedEntry } from './chatEntryCache'
import type { ChatRowItem } from './chatRowItem'
import type { SpanLine } from './widgets/SpanLines'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { MessageSpanIdentity, ToolSpanRole } from '~/lib/messageSpan'
import { createMemo } from 'solid-js'
import { messageSpanIdentity } from '~/lib/messageSpan'
import { shallowEqualSets } from '~/lib/shallowEqual'
import { sameChatRowItems } from './chatRowItem'

// ---------------------------------------------------------------------------
// Pending rows
//
// A tool call is drawn as two rows: its REQUEST row and its RESULT row. A request row
// never reads its result, so it shows, and reports its height, before the result
// exists, and it never changes when the result lands.
//
// While the call runs, the reader still needs to see what it asked -- the requested
// diff, the to-do list it carries, the agent prompt -- and the output it prints. A
// PENDING row draws that: a list row of its own, with no message of its own, right after
// the request row. It stands in for the result row: when the result row is measured,
// the result row joins the list and the pending row leaves it, in one update. So the
// end of a call replaces one row by another, which the row admission handles as it
// handles any row.
//
// The pending row of a call exists from the moment its request row is listed on the
// live tail, whether or not it has anything to show yet. While it has nothing to show,
// it takes no height and no gap (see ChatRowItem.continuesPrevious), so the reader sees
// nothing. Output that arrives, or an Expand, then changes only the height of a row that
// is already in the list. A pending row that joined the list later would sit between
// rows that the list admitted, unmeasured, and split the admitted range until a hidden
// copy measured it.
//
// The rule latches:
//
//  - A pending row OPENS for a request row while the loaded window holds the live tail
//    and the browser holds no result message of the span: the call runs.
//  - An open pending row STAYS while no result is held, and while the result row is
//    loaded and not measured yet, so the swap happens in one update.
//  - It CLOSES when the result row is measured, when the result draws no row of its own,
//    or when the window stops holding the live tail. A closed pending row never opens
//    again for a span whose result the browser holds: a finished call draws no preview,
//    also when its result row loses its height to a new width or a first fill.
// ---------------------------------------------------------------------------

/** The suffix that turns a message id into the id of its pending row. */
const PENDING_ROW_SUFFIX = '#pending'

/** The id of the pending row of the tool call that the message with `messageId` opens. */
export function pendingRowId(messageId: string): string {
  return `${messageId}${PENDING_ROW_SUFFIX}`
}

/** Whether `rowId` is the id of a pending row (see pendingRowId). */
export function isPendingRowId(rowId: string): boolean {
  return rowId.endsWith(PENDING_ROW_SUFFIX)
}

/**
 * The id of the message that the row `rowId` draws: a pending row draws the message of
 * its request row, and reads that message's state, such as its expand state.
 */
export function messageIdOfRow(rowId: string): string {
  return isPendingRowId(rowId)
    ? rowId.slice(0, -PENDING_ROW_SUFFIX.length)
    : rowId
}

/**
 * The rail columns of a pending row: the columns of its request row, continued below
 * it, and the column of the call's own span after them.
 *
 * The request row draws its span as the left border of its body, at the column after
 * its rails. A pending row has no body box, so it draws that line as a rail column at the
 * same place, as a result row does for its span. The Worker opens a span in the column
 * after every span open at its request (`resolveColumn` in the Worker's `spantrack`
 * package), so this column is the one the result row draws below.
 *
 * Each column of the request row continues: a connector to a parent span becomes the
 * parent's plain line, and a passthrough, which crosses only the request row, ends.
 */
export function pendingRowSpanLines(requestLines: readonly (SpanLine | null)[], spanId: string, spanColor: number): (SpanLine | null)[] {
  return [
    ...requestLines.map(continuedSpanLine),
    { span_id: spanId, color: spanColor, type: 'active' },
  ]
}

/** One rail column of a request row, as it continues on the row below it. */
function continuedSpanLine(line: SpanLine | null): SpanLine | null {
  if (line === null)
    return null
  switch (line.type) {
    case 'active':
      return line
    case 'connector':
    case 'active_passthrough':
      return {
        ...(line.span_id !== undefined
          ? { span_id: line.span_id }
          : {}),
        ...(line.color !== undefined
          ? { color: line.color }
          : {}),
        type: 'active',
      }
    case 'passthrough':
    case 'connector_end':
      return null
  }
}

export interface PendingRowsDeps {
  /** The rows of the loaded messages, in order. */
  entries: Accessor<readonly ClassifiedEntry[]>
  /** The items of those rows, index-aligned with `entries`. */
  items: Accessor<readonly ChatRowItem[]>
  /** Whether the loaded window holds the live tail. */
  tailLoaded: () => boolean
  /**
   * The span role of the row of `entry` (MessageContextResolver.role). Only a request
   * row gets a pending row: a row that both opens and closes its call, such as a
   * finished ACP `tool_call` or the closing copy of a request that a turn's end
   * stores, is the result of its span.
   */
  role: (entry: ClassifiedEntry) => ToolSpanRole
  /** The result message of a span that the browser holds, or undefined. */
  resultOf: (identity: MessageSpanIdentity) => AgentChatMessage | undefined
  /** Whether the height cache answers a height for a row (RowHeightCache.isMeasured). */
  isMeasured: (item: ChatRowItem) => boolean
}

export interface PendingRows {
  /** The loaded rows, each pending row right after its request row. */
  entries: Accessor<readonly ClassifiedEntry[]>
  /** The items of those rows, index-aligned with `entries`. */
  items: Accessor<ChatRowItem[]>
  /** Whether the call that the message with `messageId` opens has a pending row in the list. */
  isOpen: (messageId: string) => boolean
}

/**
 * Add the pending rows to the loaded rows. Call within a reactive owner.
 *
 * Each pending entry keeps its identity while its request entry does, so the row list
 * keeps its element. A list with no pending row is the list of the messages itself.
 */
export function createPendingRows(deps: PendingRowsDeps): PendingRows {
  // A pending entry for each request entry, rebuilt with the request entry.
  const pendingEntries = new WeakMap<ClassifiedEntry, ClassifiedEntry>()
  const pendingEntryOf = (request: ClassifiedEntry): ClassifiedEntry => {
    let entry = pendingEntries.get(request)
    if (entry === undefined) {
      entry = {
        ...request,
        rowId: pendingRowId(request.message.id),
        pending: true,
        parsedSpanLines: pendingRowSpanLines(request.parsedSpanLines, request.message.spanId, request.message.spanColor),
      }
      pendingEntries.set(request, entry)
    }
    return entry
  }

  // The ids of the messages whose call has an open pending row. The memo reads its own
  // previous answer, which is the latch: see the rules at the top of this file.
  const pendingIds = createMemo((open: ReadonlySet<string>): ReadonlySet<string> => {
    if (!deps.tailLoaded())
      return new Set()
    const entries = deps.entries()
    let items: readonly ChatRowItem[] | undefined
    let indexById: Map<string, number> | undefined
    // Whether the result row of the message with `resultId` is in the list and not
    // measured yet. The lookups are built once, and only for a held result.
    const resultRowWaits = (resultId: string): boolean => {
      if (indexById === undefined) {
        items = deps.items()
        indexById = new Map()
        entries.forEach((entry, index) => indexById!.set(entry.message.id, index))
      }
      const index = indexById.get(resultId)
      const item = index === undefined
        ? undefined
        : items?.[index]
      return item !== undefined && !deps.isMeasured(item)
    }
    const ids = new Set<string>()
    for (const entry of entries) {
      if (entry.category.kind !== 'tool_use' || entry.message.spanId === '')
        continue
      const id = entry.message.id
      const result = deps.resultOf(messageSpanIdentity(entry.message))
      const held = result !== undefined && result.id !== id
      // A call whose result the browser holds keeps only a pending row that is already
      // open. Almost every call of the window has ended, so the cheaper test comes first,
      // and the span role is read only for a row that can have a pending row.
      if (held && !open.has(id))
        continue
      if (deps.role(entry) !== 'request')
        continue
      if (!held || resultRowWaits(result.id))
        ids.add(id)
    }
    return ids
  }, new Set<string>(), { equals: shallowEqualSets })

  const listed = createMemo((): { entries: readonly ClassifiedEntry[], items: ChatRowItem[] } => {
    const entries = deps.entries()
    const items = deps.items()
    const pending = pendingIds()
    if (pending.size === 0) {
      return {
        entries,
        items: [...items],
      }
    }
    const listedEntries: ClassifiedEntry[] = []
    const listedItems: ChatRowItem[] = []
    entries.forEach((entry, index) => {
      const item = items[index]
      if (item === undefined)
        return
      listedEntries.push(entry)
      listedItems.push(item)
      if (!pending.has(entry.message.id))
        return
      const pendingEntry = pendingEntryOf(entry)
      listedItems.push({
        id: pendingEntry.rowId,
        // The sequence of the request row, which the pending row continues. Every
        // consumer of a row's sequence reads the list with its pending rows: the scroll
        // rail, which splits the one unit between the two rows, and the scroll anchor,
        // which recovers by the nearest sequence once the pending row leaves. A jump to
        // the sequence still lands on the request row, which comes first.
        ...(item.seq !== undefined
          ? { seq: item.seq }
          : {}),
        hasSpanLines: pendingEntry.parsedSpanLines.length > 0,
        // The pending row draws the request's preview: its own key, so it never answers
        // with the height of the request row.
        heightKey: `pending|${item.heightKey ?? ''}`,
        ...(item.kind !== undefined
          ? { kind: item.kind }
          : {}),
        continuesPrevious: true,
      })
      listedEntries.push(pendingEntry)
    })
    return { entries: listedEntries, items: listedItems }
  })

  return {
    entries: () => listed().entries,
    items: createMemo(() => listed().items, [], { equals: sameChatRowItems }),
    isOpen: messageId => pendingIds().has(messageId),
  }
}
