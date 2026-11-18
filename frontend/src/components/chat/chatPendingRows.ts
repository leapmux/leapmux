import type { Accessor } from 'solid-js'
import type { ClassifiedEntry } from './chatEntryCache'
import type { ChatRowItem } from './chatRowItem'
import type { SpanLine } from './widgets/SpanLines'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { MessageSpanIdentity } from '~/lib/messageSpan'
import type { ToolProgressEntry } from '~/stores/chatToolProgress'
import { createMemo, createSignal } from 'solid-js'
import { messageSpanIdentity } from '~/lib/messageSpan'
import { stripLeadingBlankLines } from '~/lib/normalizeProgressOutput'
import { shallowEqualArrays, shallowEqualSets } from '~/lib/shallowEqual'
import { sameChatRowItems } from './chatRowItem'

// ---------------------------------------------------------------------------
// Pending rows
//
// A tool call is drawn as two rows: its REQUEST row and its RESULT row. The request
// body depends on the opening message. Its header can read the result after it arrives.
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
//  - An open pending row STAYS until admission can show its result, so the swap
//    happens in one update.
//  - It CLOSES when the result row is admitted, when the result draws no row of its own,
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
  items: Accessor<ChatRowItem[]>
  /** Whether the loaded window holds the live tail. */
  tailLoaded: () => boolean
  /** The result message of a span that the browser holds, or undefined. */
  resultOf: (identity: MessageSpanIdentity) => AgentChatMessage | undefined
  /** The preview's own layout inputs. The request header's revision is excluded. */
  previewHeightKey?: (entry: ClassifiedEntry) => string
  progressOf?: (identity: MessageSpanIdentity) => ToolProgressEntry | undefined
  /** Non-virtualized transcripts show each result as soon as it is listed. */
  showsResult?: (item: ChatRowItem) => boolean
}

export interface PendingPreview {
  outputTail: string
  outputTruncated: boolean
  revision: number
}

export interface PendingRows {
  /** The loaded rows, each pending row right after its request row. */
  entries: Accessor<readonly ClassifiedEntry[]>
  /** The items of those rows, index-aligned with `entries`. */
  items: Accessor<ChatRowItem[]>
  /** Pending row ID to the result row that replaces it on admission. */
  replacements: Accessor<ReadonlyMap<string, string>>
  retire: (rowIds: readonly string[]) => void
  preview: (requestId: string) => PendingPreview | undefined
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
  const [retired, setRetired] = createSignal<readonly string[]>([])
  const requestResults = createMemo(() => {
    const requests = new Map<ClassifiedEntry, AgentChatMessage | undefined>()
    if (!deps.tailLoaded())
      return requests
    for (const entry of deps.entries()) {
      if (entry.category.kind === 'tool_use' && entry.message.spanId !== '' && entry.freshness.role === 'request') {
        const result = deps.resultOf(messageSpanIdentity(entry.message))
        requests.set(entry, result?.id === entry.message.id
          ? undefined
          : result)
      }
    }
    return requests
  })
  const listedIds = createMemo(() => new Set(deps.entries().map(entry => entry.rowId)))
  const pendingIds = createMemo((open: ReadonlySet<string>): ReadonlySet<string> => {
    const ids = new Set<string>()
    const retiredRows = retired()
    for (const [entry, result] of requestResults()) {
      const id = entry.message.id
      if (result === undefined) {
        ids.add(id)
        continue
      }
      if (!open.has(id) || !listedIds().has(result.id) || retiredRows.includes(pendingRowId(id)))
        continue
      if (deps.showsResult !== undefined && deps.items().some(item => item.id === result.id && deps.showsResult?.(item)))
        continue
      ids.add(id)
    }
    return ids
  }, new Set<string>(), { equals: shallowEqualSets })
  const replacements = createMemo(() => {
    const pairs = new Map<string, string>()
    const pending = pendingIds()
    for (const [entry, result] of requestResults()) {
      if (result !== undefined && pending.has(entry.message.id))
        pairs.set(pendingRowId(entry.message.id), result.id)
    }
    return pairs
  })

  let nextPreviewRevision = 0
  const previews = createMemo((previous: ReadonlyMap<string, PendingPreview>): ReadonlyMap<string, PendingPreview> => {
    const shown = new Map<string, PendingPreview>()
    const pending = pendingIds()
    for (const [entry, result] of requestResults()) {
      const id = entry.message.id
      if (!pending.has(id))
        continue
      const before = previous.get(id)
      const progress = deps.progressOf?.(messageSpanIdentity(entry.message))
      const outputTail = progress === undefined && result !== undefined
        ? before?.outputTail ?? ''
        : stripLeadingBlankLines(progress?.outputTail ?? '')
      const outputTruncated = progress === undefined && result !== undefined
        ? before?.outputTruncated ?? false
        : progress?.outputTruncated === true
      if (before?.outputTail === outputTail && before.outputTruncated === outputTruncated)
        shown.set(id, before)
      else if (outputTail !== '' || outputTruncated || before !== undefined)
        shown.set(id, { outputTail, outputTruncated, revision: ++nextPreviewRevision })
    }
    return shown
  }, new Map())

  const listed = createMemo((): { entries: readonly ClassifiedEntry[], items: ChatRowItem[] } => {
    const entries = deps.entries()
    const items = deps.items()
    const pending = pendingIds()
    if (pending.size === 0) {
      return {
        entries,
        items,
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
      const previewRevision = previews().get(entry.message.id)?.revision
      const heightKey = deps.previewHeightKey?.(entry) ?? item.heightKey ?? ''
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
        heightKey: previewRevision === undefined
          ? `pending|${heightKey}`
          : `pending|${heightKey}|progress:${previewRevision}`,
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
    entries: createMemo(() => listed().entries, [], { equals: shallowEqualArrays }),
    items: createMemo(() => listed().items, [], { equals: sameChatRowItems }),
    replacements,
    retire: rowIds => setRetired(rowIds),
    preview: requestId => previews().get(requestId),
    isOpen: messageId => pendingIds().has(messageId),
  }
}
