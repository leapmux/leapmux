import type { ClassifiedEntry } from './chatEntryCache'
import type { PendingRowsDeps } from './chatPendingRows'
import type { ChatRowItem } from './chatRowItem'
import type { SpanLine } from './widgets/SpanLines'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import { createPendingRows, isPendingRowId, messageIdOfRow, pendingRowId, pendingRowSpanLines } from './chatPendingRows'
import { nearestServerRowIndexBySeq } from './chatScrollAnchor'
import { rowStartSeqs } from './chatScrollRailGeometry'
import { resolvedSpanRole } from './providers/registry'
import { prepareMessage } from './rowPreparation'
import './providers'

/** A Claude tool_use row that opens span `spanId`. */
function requestMessage(id: string, seq: bigint, spanId: string): AgentChatMessage {
  return makeMessage({
    id,
    seq,
    spanId,
    spanColor: 3,
    spanLines: '[]',
    content: rawContent({
      type: 'assistant',
      message: {
        role: 'assistant',
        content: [
          {
            type: 'tool_use',
            id: spanId,
            name: 'Bash',
            input: { command: 'make' },
          },
        ],
      },
    }),
  })
}

/** A Claude tool_result row that closes span `spanId`. */
function resultMessage(id: string, seq: bigint, spanId: string): AgentChatMessage {
  return makeMessage({
    id,
    seq,
    spanId,
    content: rawContent({
      type: 'user',
      message: {
        role: 'user',
        content: [
          { type: 'tool_result', tool_use_id: spanId, content: 'done' },
        ],
      },
    }),
  })
}

/** A plain assistant text row, which opens no call. */
function textMessage(id: string, seq: bigint): AgentChatMessage {
  return makeMessage({
    id,
    seq,
    content: rawContent({
      type: 'assistant',
      message: {
        role: 'assistant',
        content: [
          { type: 'text', text: 'hi' },
        ],
      },
    }),
  })
}

function entryOf(message: AgentChatMessage, parsedSpanLines: (SpanLine | null)[] = []): ClassifiedEntry {
  const prepared = prepareMessage(message)
  return {
    ...prepared,
    rowId: message.id,
    pending: false,
    parsedSpanLines,
    freshness: {
      revisionKey: message.id,
      isChildTranscript: false,
      settingsLabelRevision: '',
      role: resolvedSpanRole(prepared.resolved, message.agentProvider),
    },
    spanLinesRef: message.spanLines,
    settingsLabelDependencies: [],
  }
}

function itemOf(entry: ClassifiedEntry): ChatRowItem {
  return {
    id: entry.rowId,
    seq: entry.message.seq,
    hasSpanLines: entry.parsedSpanLines.length > 0,
    heightKey: `key:${entry.rowId}`,
    kind: entry.category.kind,
  }
}

interface Harness {
  ids: () => string[]
  isOpen: (messageId: string) => boolean
  entries: () => readonly ClassifiedEntry[]
  items: () => ChatRowItem[]
  setEntries: (entries: ClassifiedEntry[]) => void
  setTail: (tail: boolean) => void
  setMeasured: (ids: string[]) => void
  setResult: (message: AgentChatMessage | undefined) => void
}

/** Run `body` over a pending-rows unit whose sources the test drives. */
function withPendingRows(initial: ClassifiedEntry[], overrides: Partial<PendingRowsDeps>, body: (harness: Harness) => void): void {
  createRoot((dispose) => {
    try {
      const [entries, setEntries] = createSignal<ClassifiedEntry[]>(initial)
      const [tail, setTail] = createSignal(true)
      const [measured, setMeasured] = createSignal<ReadonlySet<string>>(new Set())
      const [result, setResult] = createSignal<AgentChatMessage | undefined>(undefined)
      const rows = createPendingRows({
        entries,
        items: () => entries().map(itemOf),
        tailLoaded: tail,
        resultOf: () => result(),
        showsResult: item => measured().has(item.id),
        ...overrides,
      })
      body({
        ids: () => rows.entries().map(entry => entry.rowId),
        isOpen: rows.isOpen,
        entries: rows.entries,
        items: rows.items,
        setEntries,
        setTail,
        setMeasured: ids => setMeasured(new Set(ids)),
        setResult,
      })
    }
    finally {
      dispose()
    }
  })
}

describe('messageIdOfRow', () => {
  // A pending row draws its request's message, and reads that message's UI state.
  it('answers the message that a row draws', () => {
    expect(messageIdOfRow('msg-1')).toBe('msg-1')
    expect(messageIdOfRow(pendingRowId('msg-1'))).toBe('msg-1')
  })
})

describe('isPendingRowId', () => {
  it('recognizes the id of a pending row, and no message id', () => {
    expect(isPendingRowId(pendingRowId('message-1'))).toBe(true)
    expect(isPendingRowId('message-1')).toBe(false)
  })
})

describe('pendingRowSpanLines', () => {
  it('draws the line of the call\'s own span in the column after the request row\'s rails', () => {
    expect(pendingRowSpanLines([], 'tool-1', 4)).toEqual([
      { span_id: 'tool-1', color: 4, type: 'active' },
    ])
  })

  // A connector to a parent span continues as the parent's plain line below the request
  // row. A passthrough crosses only the request row, so it ends.
  it('continues each column of the request row', () => {
    const lines: (SpanLine | null)[] = [
      { span_id: 'parent', color: 1, type: 'connector' },
      null,
      {
        span_id: 'open',
        color: 2,
        type: 'active_passthrough',
        passthrough_color: 1,
      },
      { type: 'passthrough', passthrough_color: 1 },
      { span_id: 'still', color: 5, type: 'active' },
      { span_id: 'closed', color: 6, type: 'connector_end' },
    ]
    expect(pendingRowSpanLines(lines, 'tool-1', 7)).toEqual([
      { span_id: 'parent', color: 1, type: 'active' },
      null,
      { span_id: 'open', color: 2, type: 'active' },
      null,
      { span_id: 'still', color: 5, type: 'active' },
      null,
      { span_id: 'tool-1', color: 7, type: 'active' },
    ])
  })
})

describe('createPendingRows', () => {
  const request = entryOf(requestMessage('req', 1n, 'span-1'))
  const text = entryOf(textMessage('text', 2n))

  it('places the pending row of a running call right after its request row', () => {
    withPendingRows([request, text], {}, ({ ids, entries, items }) => {
      expect(ids()).toEqual([
        'req',
        pendingRowId('req'),
        'text',
      ])
      const pending = entries()[1]!
      expect(pending.pending).toBe(true)
      expect(pending.message).toBe(request.message)
      expect(pending.parsedSpanLines).toEqual([
        { span_id: 'span-1', color: 3, type: 'active' },
      ])
      // The request's sequence, and no gap above it, so an empty pending row takes no space.
      expect(items()[1]).toEqual({
        id: pendingRowId('req'),
        seq: 1n,
        hasSpanLines: true,
        heightKey: 'pending|key:req',
        kind: 'tool_use',
        continuesPrevious: true,
      })
    })
  })

  // Every consumer of a row's sequence reads the list with its pending rows: the scroll
  // rail maps each row to its place in the conversation, a scroll anchor recovers by the
  // nearest sequence, and a jump lands on the row of a sequence. A row with no sequence
  // made the rail give up while any call ran.
  // A list item states no sequence for a row that the conversation does not number. The
  // pending row then states none either, rather than an undefined field.
  it('states no sequence on the pending row of a request item that states none', () => {
    withPendingRows([request, text], {
      items: () => [request, text].map((entry) => {
        const { seq: _seq, ...item } = itemOf(entry)
        return item
      }),
    }, ({ items }) => {
      expect(items()[1]).not.toHaveProperty('seq')
      expect(items()[1]).toMatchObject({
        id: pendingRowId('req'),
        continuesPrevious: true,
      })
    })
  })

  it('keeps every row of the list on the conversation\'s sequence map while a call runs', () => {
    withPendingRows([request, text], {}, ({ items }) => {
      expect(rowStartSeqs(items())).toEqual([1, 1.5, 2, 3])
      // A jump to the call's sequence lands on its request row, not on the pending row.
      expect(nearestServerRowIndexBySeq(items(), 1n)).toBe(0)
    })
  })

  // A window that does not hold the live tail draws no pending row: a call there almost
  // always ended, and its result lies in a newer page.
  it('draws no pending row while the window does not hold the live tail', () => {
    withPendingRows([request, text], {}, ({ ids, isOpen, setTail }) => {
      expect(isOpen('req')).toBe(true)
      setTail(false)
      expect(ids()).toEqual(['req', 'text'])
      expect(isOpen('req')).toBe(false)
      setTail(true)
      expect(ids()).toEqual([
        'req',
        pendingRowId('req'),
        'text',
      ])
    })
  })

  // A row that both opens and closes its call -- a finished ACP `tool_call`, or the
  // closing copy of a request that a turn's end stores -- is the result of its span.
  it('draws no pending row for a tool row whose span role is not request', () => {
    const finished: ClassifiedEntry = {
      ...request,
      freshness: { ...request.freshness, role: 'result' },
    }
    withPendingRows([finished, text], {}, ({ ids }) => {
      expect(ids()).toEqual(['req', 'text'])
    })
  })

  it('draws no pending row for a row that opens no call', () => {
    const noSpan = entryOf(requestMessage('nospan', 3n, ''))
    withPendingRows([text, noSpan], {}, ({ ids }) => {
      expect(ids()).toEqual(['text', 'nospan'])
    })
  })

  // The pending row stands in for the result row until the result row is measured, so
  // the two change places in one update and no row changes its height.
  it('keeps the pending row until the result row is measured', () => {
    const result = entryOf(resultMessage('res', 3n, 'span-1'))
    withPendingRows([request, text], {}, ({ ids, isOpen, setEntries, setResult, setMeasured }) => {
      setEntries([request, text, result])
      setResult(result.message)
      expect(ids()).toEqual([
        'req',
        pendingRowId('req'),
        'text',
        'res',
      ])
      expect(isOpen('req')).toBe(true)
      setMeasured(['res'])
      expect(ids()).toEqual(['req', 'text', 'res'])
      expect(isOpen('req')).toBe(false)
      expect(isOpen('text')).toBe(false)
    })
  })

  // A call that has ended when its rows load -- a first fill, an older page, a reload --
  // draws no preview, also while its result row waits for its first measure.
  it('opens no pending row for a call whose result the browser already holds', () => {
    const result = entryOf(resultMessage('res', 3n, 'span-1'))
    withPendingRows([request, text, result], {
      resultOf: () => result.message,
    }, ({ ids }) => {
      expect(ids()).toEqual(['req', 'text', 'res'])
    })
  })

  // A result row that loses its height -- a new width, a new font -- waits for a measure
  // again. The pending row that left must not come back as a preview of a finished call.
  it('never reopens a pending row that left', () => {
    const result = entryOf(resultMessage('res', 3n, 'span-1'))
    withPendingRows([request, text], {}, ({ ids, setEntries, setResult, setMeasured }) => {
      setEntries([request, text, result])
      setResult(result.message)
      expect(ids()).toEqual([
        'req',
        pendingRowId('req'),
        'text',
        'res',
      ])
      setMeasured(['res'])
      expect(ids()).toEqual(['req', 'text', 'res'])
      setMeasured([])
      expect(ids()).toEqual(['req', 'text', 'res'])
    })
  })

  // The window stops holding the live tail: the latch lets go, and a call that still runs
  // when the tail returns opens a new pending row.
  it('closes the pending row when the window stops holding the live tail', () => {
    withPendingRows([request, text], {}, ({ ids, setTail }) => {
      expect(ids()).toEqual([
        'req',
        pendingRowId('req'),
        'text',
      ])
      setTail(false)
      expect(ids()).toEqual(['req', 'text'])
    })
  })

  // A result that draws no row of its own -- a frame the provider hides -- has no measure
  // to wait for, so the pending row leaves at once.
  it('drops the pending row at once when the result draws no row', () => {
    const hidden = resultMessage('hidden-res', 3n, 'span-1')
    withPendingRows([request, text], {}, ({ ids, setResult }) => {
      setResult(hidden)
      expect(ids()).toEqual(['req', 'text'])
    })
  })

  // A span whose indexed result is the request message itself has no other row to wait
  // for: the call still runs.
  it('keeps the pending row when the span\'s result is the request message itself', () => {
    withPendingRows([request, text], {}, ({ ids, setResult }) => {
      setResult(request.message)
      expect(ids()).toEqual([
        'req',
        pendingRowId('req'),
        'text',
      ])
    })
  })

  it('keeps the identity of a pending entry while its request entry stays', () => {
    withPendingRows([request, text], {}, ({ entries, setEntries }) => {
      const first = entries()[1]
      setEntries([
        request,
        text,
        entryOf(textMessage('later', 4n)),
      ])
      expect(entries()[1]).toBe(first)
    })
  })

  it('hands back the entries of the messages themselves when no call runs', () => {
    const messages = [text]
    withPendingRows(messages, {}, ({ entries }) => {
      expect(entries()).toBe(messages)
    })
  })
})
