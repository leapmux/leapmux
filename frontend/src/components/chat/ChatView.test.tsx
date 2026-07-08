import type { JSX } from 'solid-js'
import type { ChatVirtualizerRange } from './useChatVirtualizer'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import { render, waitFor } from '@solidjs/testing-library'
import { batch, createEffect, createSignal, For } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AgentChatMessageSchema, AgentProvider, MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { messageListRailActive, rowSkeletonClosing } from './ChatView.css'

type HiddenPremeasureOnMeasure = (id: string, height: number, heightKey: string | undefined, measureDurationMs: number, settled: boolean) => boolean

const virtualizerState = vi.hoisted(() => ({
  range: { start: 0, end: 1 } as ChatVirtualizerRange,
  setRange: undefined as undefined | ((range: ChatVirtualizerRange) => void),
  attachedIds: [] as string[],
  measuredIds: new Set<string>(),
  currentHeightKeys: new Map<string, string | undefined>(),
  setDeferred: undefined as undefined | ((deferred: boolean) => void),
}))

const hiddenPremeasureState = vi.hoisted(() => ({
  candidates: [] as Array<{ entry: unknown, item: { id: string, heightKey?: string } }>,
  onMeasure: undefined as HiddenPremeasureOnMeasure | undefined,
  contentWidthPx: undefined as number | undefined,
}))

const viewportSizeObserverState = vi.hoisted(() => ({
  width: 640,
  height: 733,
  onWidth: undefined as undefined | ((width: number) => void),
  onHeight: undefined as undefined | ((height: number) => void),
}))

const prefsState = vi.hoisted(() => ({
  setDiffView: undefined as undefined | ((view: 'unified' | 'split') => void),
  setExpandThoughts: undefined as undefined | ((value: boolean) => void),
}))

vi.mock('~/context/PreferencesContext', async () => {
  const { createSignal } = await import('solid-js')
  const [diffView, setDiffView] = createSignal<'unified' | 'split'>('unified')
  const [expandThoughts, setExpandThoughts] = createSignal(true)
  prefsState.setDiffView = setDiffView
  prefsState.setExpandThoughts = setExpandThoughts
  return {
    usePreferences: () => ({
      diffView,
      expandAgentThoughts: expandThoughts,
      showHiddenMessages: () => false,
    }),
  }
})

vi.mock('./MessageBubble', () => ({
  MessageBubble: (props: { message: AgentChatMessage, premeasureMode?: boolean }) => (
    <div
      data-testid="mock-message-bubble"
      data-message-id={props.message.id}
      data-premeasure={props.premeasureMode ? 'true' : 'false'}
    />
  ),
}))

vi.mock('./chatHiddenPremeasure', () => ({
  ChatHiddenPremeasure: (props: {
    candidates: Array<{ entry: unknown, item: { id: string, heightKey?: string } }>
    contentWidthPx: number
    renderBubble: (entry: unknown) => JSX.Element
    onMeasure: HiddenPremeasureOnMeasure
  }) => {
    createEffect(() => {
      hiddenPremeasureState.candidates = props.candidates
      hiddenPremeasureState.contentWidthPx = props.contentWidthPx
      virtualizerState.currentHeightKeys = new Map(props.candidates.map(candidate => [candidate.item.id, candidate.item.heightKey]))
      hiddenPremeasureState.onMeasure = props.onMeasure
    })
    return (
      <div data-testid="mock-hidden-premeasure">
        <For each={props.candidates}>
          {candidate => props.renderBubble(candidate.entry)}
        </For>
      </div>
    )
  },
}))

vi.mock('./chatViewportGeometry', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./chatViewportGeometry')>()
  return {
    ...actual,
    createViewportSizeObserver: (opts: { onWidth: (w: number) => void, onHeight: (h: number) => void }) => ({
      observe: () => {
        viewportSizeObserverState.onWidth = opts.onWidth
        viewportSizeObserverState.onHeight = opts.onHeight
        opts.onWidth(viewportSizeObserverState.width)
        opts.onHeight(viewportSizeObserverState.height)
      },
      disconnect: vi.fn(),
    }),
  }
})

vi.mock('./useChatScroll', () => ({
  useChatScroll: () => ({
    attachListRef: vi.fn(),
    handlers: {
      onScroll: vi.fn(),
      onWheel: vi.fn(),
      onKeyDown: vi.fn(),
      onTouchStart: vi.fn(),
      onTouchMove: vi.fn(),
      onTouchEnd: vi.fn(),
      onTouchCancel: vi.fn(),
      onPointerDown: vi.fn(),
      onPointerMove: vi.fn(),
      onPointerUp: vi.fn(),
      onPointerCancel: vi.fn(),
    },
    atBottom: () => true,
    stalledOlder: () => false,
    stalledNewer: () => false,
    scrollToBottom: vi.fn(),
    restickIfAtBottom: vi.fn(),
    isAtBottomFresh: () => false,
    jumpToBottom: vi.fn(),
    getScrollState: () => undefined,
    forceScrollToBottom: vi.fn(),
    pageScroll: vi.fn(),
  }),
}))

vi.mock('./useChatVirtualizer', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./useChatVirtualizer')>()
  const { createSignal } = await import('solid-js')
  const [version, setVersion] = createSignal(0)
  const [deferred, setDeferred] = createSignal(false)
  virtualizerState.setRange = (range: ChatVirtualizerRange) => {
    virtualizerState.range = range
    setVersion(v => v + 1)
  }
  virtualizerState.setDeferred = setDeferred
  return {
    ...actual,
    useChatVirtualizer: () => ({
      mountedIds: new Set<string>(),
      fastScrollActive: deferred,
      range: () => {
        version()
        return virtualizerState.range
      },
      geometryVersion: version,
      totalHeight: () => 10_000,
      offsetOfId: (id: string) => Number(id.slice(1)) * 100,
      indexOfId: (id: string) => Number(id.slice(1)),
      offsetOfIndex: (index: number) => index * 100,
      heightOfIndex: () => 100,
      heightOfId: () => 100,
      hasMeasuredHeight: (id: string) => {
        version()
        return virtualizerState.measuredIds.has(id)
      },
      hasPendingPremeasuredHeight: () => false,
      heightDebugOfId: () => ({}),
      attachRow: (id: string) => {
        virtualizerState.attachedIds.push(id)
      },
      detachRow: vi.fn(),
      primeHeight: vi.fn((id: string, _height: number, heightKey?: string) => {
        if (!virtualizerState.currentHeightKeys.has(id) || virtualizerState.currentHeightKeys.get(id) !== heightKey)
          return false
        virtualizerState.measuredIds.add(id)
        return true
      }),
      primeHeights: vi.fn(() => 0),
      snapshotHeights: () => [],
    }),
  }
})

const { ChatView, SKELETON_SHOW_DELAY_MS } = await import('./ChatView')
const { PRE_MEASURE_WIDTH_PX } = await import('./chatViewportGeometry')

afterEach(() => {
  vi.useRealTimers()
  viewportSizeObserverState.width = 640
  viewportSizeObserverState.height = 733
  viewportSizeObserverState.onWidth = undefined
  viewportSizeObserverState.onHeight = undefined
  prefsState.setDiffView?.('unified') // the pref signals are module-scoped; reset between tests
  prefsState.setExpandThoughts?.(true)
})

function message(id: string, seq: number): AgentChatMessage {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.AGENT,
    content: new TextEncoder().encode(`message ${id}`),
    seq: BigInt(seq),
    createdAt: '2026-06-28T00:00:00.000Z',
    agentProvider: AgentProvider.CODEX,
  })
}

function visibleBubbleIds(container: HTMLElement): string[] {
  return [...container.querySelectorAll('[data-testid="mock-message-bubble"][data-premeasure="false"]')]
    .map(el => el.getAttribute('data-message-id') ?? '')
}

describe('chat view virtualized visible slice', () => {
  it('does not mount rows between stale pending premeasure ids and the current viewport range', async () => {
    virtualizerState.attachedIds = []
    virtualizerState.measuredIds = new Set()
    virtualizerState.currentHeightKeys = new Map()
    hiddenPremeasureState.candidates = []
    hiddenPremeasureState.onMeasure = undefined
    virtualizerState.setRange?.({ start: 0, end: 1 })
    const messages = Array.from({ length: 12 }, (_, index) => message(`m${index}`, index + 1))
    const { container } = render(() => (
      <ChatView
        messages={messages}
        streamingText=""
      />
    ))

    await Promise.resolve()
    expect(visibleBubbleIds(container)).toEqual(['m0'])

    virtualizerState.setRange?.({ start: 8, end: 10 })
    await Promise.resolve()

    expect(visibleBubbleIds(container)).toEqual(['m8', 'm9'])
    expect(visibleBubbleIds(container)).not.toContain('m1')
    expect(visibleBubbleIds(container)).not.toContain('m7')
  })

  it('keeps an unsettled premeasure row mounted after the first height commit', async () => {
    virtualizerState.attachedIds = []
    virtualizerState.measuredIds = new Set()
    virtualizerState.currentHeightKeys = new Map()
    hiddenPremeasureState.candidates = []
    hiddenPremeasureState.onMeasure = undefined
    virtualizerState.setRange?.({ start: 0, end: 1 })
    render(() => (
      <ChatView
        messages={[message('m0', 1)]}
        streamingText=""
      />
    ))

    await waitFor(() => {
      expect(hiddenPremeasureState.candidates.map(candidate => candidate.item.id)).toEqual(['m0'])
    })

    const heightKey = hiddenPremeasureState.candidates[0].item.heightKey
    const onMeasure = hiddenPremeasureState.onMeasure as unknown as HiddenPremeasureOnMeasure
    onMeasure('m0', 20, heightKey, 0, false)
    await waitFor(() => {
      expect(hiddenPremeasureState.candidates.map(candidate => candidate.item.id)).toEqual(['m0'])
    })

    onMeasure('m0', 40, heightKey, 0, true)

    await waitFor(() => {
      expect(hiddenPremeasureState.candidates.map(candidate => candidate.item.id)).toEqual([])
    })
  })

  it('keeps a settled premeasure row pending when its height key is stale', async () => {
    virtualizerState.attachedIds = []
    virtualizerState.measuredIds = new Set()
    virtualizerState.currentHeightKeys = new Map()
    hiddenPremeasureState.candidates = []
    hiddenPremeasureState.onMeasure = undefined
    virtualizerState.setRange?.({ start: 0, end: 1 })
    render(() => (
      <ChatView
        messages={[message('m0', 1)]}
        streamingText=""
      />
    ))

    await waitFor(() => {
      expect(hiddenPremeasureState.candidates.map(candidate => candidate.item.id)).toEqual(['m0'])
    })

    const heightKey = hiddenPremeasureState.candidates[0].item.heightKey
    const staleHeightKey = `${heightKey ?? 'missing'}:stale`
    const onMeasure = hiddenPremeasureState.onMeasure as unknown as HiddenPremeasureOnMeasure
    onMeasure('m0', 20, staleHeightKey, 0, true)

    expect(virtualizerState.measuredIds.has('m0')).toBe(false)
    await waitFor(() => {
      expect(hiddenPremeasureState.candidates.map(candidate => candidate.item.id)).toEqual(['m0'])
    })

    onMeasure('m0', 30, heightKey, 0, true)

    await waitFor(() => {
      expect(hiddenPremeasureState.candidates.map(candidate => candidate.item.id)).toEqual([])
    })
  })

  it('uses the same fallback width for queued hidden premeasure keys and layout', async () => {
    virtualizerState.attachedIds = []
    virtualizerState.measuredIds = new Set()
    virtualizerState.currentHeightKeys = new Map()
    hiddenPremeasureState.candidates = []
    hiddenPremeasureState.onMeasure = undefined
    hiddenPremeasureState.contentWidthPx = undefined
    virtualizerState.setRange?.({ start: 0, end: 1 })
    render(() => (
      <ChatView
        messages={[message('m0', 1)]}
        streamingText=""
      />
    ))

    await waitFor(() => {
      expect(hiddenPremeasureState.candidates.map(candidate => candidate.item.id)).toEqual(['m0'])
    })

    viewportSizeObserverState.onWidth?.(0)

    let fallbackWidthHeightKey: string | undefined
    await waitFor(() => {
      expect(hiddenPremeasureState.contentWidthPx).toBe(PRE_MEASURE_WIDTH_PX)
      fallbackWidthHeightKey = hiddenPremeasureState.candidates[0].item.heightKey
      expect(fallbackWidthHeightKey).toBeDefined()
    })

    viewportSizeObserverState.onWidth?.(PRE_MEASURE_WIDTH_PX)

    await waitFor(() => {
      expect(hiddenPremeasureState.contentWidthPx).toBe(PRE_MEASURE_WIDTH_PX)
      expect(hiddenPremeasureState.candidates[0].item.heightKey).toBe(fallbackWidthHeightKey)
    })
  })

  it('hides a newly appended live-tail row until it is measured (no overflow onto trailing UI)', async () => {
    // Regression: the live tail was shown at its ESTIMATED height immediately, so a
    // tall unmeasured tail overflowed its slot onto the in-flow thinking indicator,
    // and it revealed a frame ahead of an earlier appended sibling still skeletonised.
    // The tail is now hidden-until-measured like any other in-range row.
    virtualizerState.attachedIds = []
    virtualizerState.measuredIds = new Set(['m0'])
    virtualizerState.currentHeightKeys = new Map()
    hiddenPremeasureState.candidates = []
    hiddenPremeasureState.onMeasure = undefined
    virtualizerState.setRange?.({ start: 0, end: 1 })
    const [messages, setMessages] = createSignal([message('m0', 1)])
    const { container } = render(() => (
      <ChatView
        messages={messages()}
        streamingText=""
      />
    ))

    await waitFor(() => {
      expect(visibleBubbleIds(container)).toEqual(['m0'])
    })

    virtualizerState.setRange?.({ start: 0, end: 2 })
    setMessages([message('m0', 1), message('m1', 2)])

    // The appended tail m1 is premeasured AND hidden until its height commits -- it must
    // not paint its real content at the estimate. It shows NO skeleton immediately (a
    // fast re-measure is expected; the skeleton is deferred by SKELETON_SHOW_DELAY_MS).
    await waitFor(() => {
      expect(hiddenPremeasureState.candidates.map(candidate => candidate.item.id)).toEqual(['m1'])
    })
    const appendedRow = container.querySelector('[data-seq="2"]') as HTMLElement
    expect(appendedRow.style.visibility).toBe('hidden')
    expect(appendedRow.style.opacity).toBe('0')
    expect(container.querySelectorAll('[data-testid="row-skeleton"]')).toHaveLength(0)

    // Its height commits -> it fades in, still with no skeleton.
    virtualizerState.measuredIds.add('m1')
    virtualizerState.setRange?.({ start: 0, end: 2 }) // bump the stub's version signal
    expect(appendedRow.style.visibility).not.toBe('hidden')
    expect(appendedRow.style.opacity).toBe('1')
    expect(container.querySelectorAll('[data-testid="row-skeleton"]')).toHaveLength(0)
  })

  it('keeps streaming text in flow until its replacement row is measured', async () => {
    virtualizerState.attachedIds = []
    virtualizerState.measuredIds = new Set()
    virtualizerState.currentHeightKeys = new Map()
    hiddenPremeasureState.candidates = []
    hiddenPremeasureState.onMeasure = undefined
    virtualizerState.setRange?.({ start: 0, end: 1 })
    const [messages, setMessages] = createSignal<AgentChatMessage[]>([])
    const [streamingText, setStreamingText] = createSignal('Streaming answer')
    const { container } = render(() => (
      <ChatView
        messages={messages()}
        streamingText={streamingText()}
      />
    ))

    batch(() => {
      setMessages([message('m0', 1)])
      setStreamingText('')
    })

    const row = await waitFor(() => {
      const el = container.querySelector('[data-seq="1"]') as HTMLElement | null
      expect(el).not.toBeNull()
      return el!
    })
    await waitFor(() => expect(container).toHaveTextContent('Streaming answer'))
    expect(row.style.visibility).toBe('hidden')
    expect(row.style.opacity).toBe('0')

    await waitFor(() => {
      expect(hiddenPremeasureState.candidates.map(candidate => candidate.item.id)).toEqual(['m0'])
    })
    const heightKey = hiddenPremeasureState.candidates[0].item.heightKey
    const onMeasure = hiddenPremeasureState.onMeasure as unknown as HiddenPremeasureOnMeasure
    onMeasure('m0', 64, heightKey, 0, true)
    virtualizerState.setRange?.({ start: 0, end: 1 })

    await waitFor(() => expect(container).not.toHaveTextContent('Streaming answer'))
    expect(row.style.visibility).toBe('')
    expect(row.style.opacity).toBe('1')
  })

  it('renders newly appended interior AND live-tail rows invisible until measured', () => {
    virtualizerState.attachedIds = []
    virtualizerState.measuredIds = new Set(['m0'])
    virtualizerState.currentHeightKeys = new Map()
    hiddenPremeasureState.candidates = []
    hiddenPremeasureState.onMeasure = undefined
    virtualizerState.setRange?.({ start: 0, end: 1 })
    const [messages, setMessages] = createSignal([message('m0', 1)])
    const { container } = render(() => (
      <ChatView
        messages={messages()}
        streamingText=""
      />
    ))

    virtualizerState.setRange?.({ start: 0, end: 3 })
    setMessages([message('m0', 1), message('m1', 2), message('m2', 3)])

    // Both the interior unmeasured row m1 and the live tail m2 render hidden until
    // their heights commit (INVISIBILITY, not a 0-height collapse), so neither paints
    // real content at its estimate -- m2 no longer overflows onto the trailing tail UI.
    // Applied synchronously (a createComputed), before any async turn.
    const interior = container.querySelector('[data-seq="2"]') as HTMLElement | null
    const tail = container.querySelector('[data-seq="3"]') as HTMLElement | null
    expect(interior).not.toBeNull()
    expect(interior!.style.visibility).toBe('hidden')
    expect(tail).not.toBeNull()
    expect(tail!.style.visibility).toBe('hidden')
  })

  it('defers loading skeletons past the show-delay, then paints them (including the live tail)', () => {
    vi.useFakeTimers()
    virtualizerState.attachedIds = []
    virtualizerState.measuredIds = new Set(['m0'])
    virtualizerState.currentHeightKeys = new Map()
    virtualizerState.setDeferred?.(false)
    hiddenPremeasureState.candidates = []
    hiddenPremeasureState.onMeasure = undefined
    virtualizerState.setRange?.({ start: 0, end: 1 })
    const [messages, setMessages] = createSignal([message('m0', 1)])
    const { container } = render(() => (
      <ChatView
        messages={messages()}
        streamingText=""
      />
    ))

    virtualizerState.setRange?.({ start: 0, end: 3 })
    setMessages([message('m0', 1), message('m1', 2), message('m2', 3)])

    // m1 (interior) and m2 (live tail) are hidden immediately, but NO skeleton paints
    // yet -- a fast re-measure is expected, so the shimmer is deferred.
    expect(container.querySelectorAll('[data-testid="row-skeleton"]')).toHaveLength(0)

    // Only once the wait exceeds SKELETON_SHOW_DELAY_MS do the skeletons appear, one per
    // still-hidden row (m1 -> 100px, m2 -> 200px); m0 (measured) gets none.
    vi.advanceTimersByTime(SKELETON_SHOW_DELAY_MS)
    const skeletons = [...container.querySelectorAll('[data-testid="row-skeleton"]')] as HTMLElement[]
    expect(skeletons).toHaveLength(2)
    expect(skeletons.every(s => s.style.height === '100px')).toBe(true) // stub heightOfIndex
    expect(skeletons.map(s => s.parentElement!.style.transform).sort())
      .toEqual(['translateY(100px)', 'translateY(200px)'])

    // Once both heights commit, the real rows show and the shown overlays CROSSFADE out:
    // each lingers for one SKELETON_CROSSFADE_MS beat in the fading-out wrapper.
    virtualizerState.measuredIds.add('m1')
    virtualizerState.measuredIds.add('m2')
    virtualizerState.setRange?.({ start: 0, end: 3 }) // bump the stub's version signal
    expect((container.querySelector('[data-seq="2"]') as HTMLElement).style.visibility).not.toBe('hidden')
    expect((container.querySelector('[data-seq="3"]') as HTMLElement).style.visibility).not.toBe('hidden')
    const lingering = [...container.querySelectorAll('[data-testid="row-skeleton"]')] as HTMLElement[]
    expect(lingering).toHaveLength(2)
    expect(lingering.every(s => s.parentElement!.classList.contains(rowSkeletonClosing))).toBe(true)

    // After the crossfade beat the skeletons unmount for good.
    vi.advanceTimersByTime(SKELETON_SHOW_DELAY_MS)
    expect(container.querySelectorAll('[data-testid="row-skeleton"]')).toHaveLength(0)
  })

  it('does NOT re-key non-diff / non-thinking rows on a global diffView or expandThoughts toggle', () => {
    // The kind-scoped heightKey keeps diffView out of every row's key except tool_use /
    // tool_result, and expandAgentThoughts out of every kind except assistant_thinking (see
    // kindScopedLayoutKey). These agent-TEXT rows (assistant_text) depend on NEITHER, so a
    // global toggle must leave their heightKey byte-identical -- no re-measure, no viewport
    // blank. This is the fix for the whole-viewport dim: it FAILS against the old global
    // layoutEpochKey (which folded both prefs into every row). kindScopedLayoutKey's own
    // unit tests cover the other half -- that tool / thinking rows DO carry the term.
    virtualizerState.attachedIds = []
    virtualizerState.measuredIds = new Set() // all unmeasured -> all are premeasure candidates
    virtualizerState.currentHeightKeys = new Map()
    virtualizerState.setDeferred?.(false)
    hiddenPremeasureState.candidates = []
    hiddenPremeasureState.onMeasure = undefined
    prefsState.setDiffView?.('unified')
    prefsState.setExpandThoughts?.(true)
    virtualizerState.setRange?.({ start: 0, end: 3 })
    const messages = [message('m0', 1), message('m1', 2), message('m2', 3)]
    render(() => (
      <ChatView
        messages={messages}
        streamingText=""
      />
    ))

    // The premeasure mock captures each candidate row's heightKey (see its createEffect).
    const before = new Map(virtualizerState.currentHeightKeys)
    expect([...before.keys()].sort()).toEqual(['m0', 'm1', 'm2'])
    expect([...before.values()].every(key => typeof key === 'string' && key.length > 0)).toBe(true)

    prefsState.setDiffView?.('split') // toggle the GLOBAL diff-view preference
    for (const id of ['m0', 'm1', 'm2'])
      expect(virtualizerState.currentHeightKeys.get(id)).toBe(before.get(id)) // unchanged

    prefsState.setExpandThoughts?.(false) // toggle the GLOBAL expand-thoughts preference
    for (const id of ['m0', 'm1', 'm2'])
      expect(virtualizerState.currentHeightKeys.get(id)).toBe(before.get(id)) // still unchanged
  })

  it('reveals appended rows in document order, even when a later one measures first', async () => {
    // Issue: when several messages are appended at once, whichever measured first
    // used to pop in first -- so a later message could appear ahead of an earlier one
    // still showing a skeleton. Now a measured tail row is HELD until every earlier
    // still-loading sibling has revealed.
    virtualizerState.attachedIds = []
    virtualizerState.measuredIds = new Set(['m0'])
    virtualizerState.currentHeightKeys = new Map()
    virtualizerState.setDeferred?.(false)
    hiddenPremeasureState.candidates = []
    hiddenPremeasureState.onMeasure = undefined
    virtualizerState.setRange?.({ start: 0, end: 1 })
    const [messages, setMessages] = createSignal([message('m0', 1)])
    const { container } = render(() => (
      <ChatView
        messages={messages()}
        streamingText=""
      />
    ))

    // Append m1 (interior) and m2 (tail) together -- both hidden until measured.
    virtualizerState.setRange?.({ start: 0, end: 3 })
    setMessages([message('m0', 1), message('m1', 2), message('m2', 3)])
    const interior = () => container.querySelector('[data-seq="2"]') as HTMLElement
    const tail = () => container.querySelector('[data-seq="3"]') as HTMLElement
    expect(interior().style.visibility).toBe('hidden')
    expect(tail().style.visibility).toBe('hidden')

    // The TAIL (m2) measures FIRST, while the interior m1 is still loading. m2 must
    // stay hidden so it can't appear before m1 (ordering is enforced by visibility,
    // independent of the deferred loading skeleton).
    virtualizerState.measuredIds.add('m2')
    virtualizerState.setRange?.({ start: 0, end: 3 }) // bump
    expect(interior().style.visibility).toBe('hidden') // still loading
    expect(tail().style.visibility).toBe('hidden') // HELD behind m1

    // m1 measures -> both reveal together, in order.
    virtualizerState.measuredIds.add('m1')
    virtualizerState.setRange?.({ start: 0, end: 3 }) // bump
    expect(interior().style.visibility).not.toBe('hidden')
    expect(tail().style.visibility).not.toBe('hidden')
  })

  it('does not skeletonise a stream-covered tail even when an earlier appended row is loading', () => {
    // The order gate can pick up the stream-replacement tail (it is "ready" -- its
    // content is painted by the in-flow streaming bubble, not a skeleton). Holding it
    // behind an earlier still-loading row must NOT paint a skeleton over its slot, or
    // the row double-paints with the bubble.
    vi.useFakeTimers()
    virtualizerState.attachedIds = []
    virtualizerState.measuredIds = new Set(['m0'])
    virtualizerState.currentHeightKeys = new Map()
    virtualizerState.setDeferred?.(false)
    hiddenPremeasureState.candidates = []
    hiddenPremeasureState.onMeasure = undefined
    virtualizerState.setRange?.({ start: 0, end: 3 })
    const [messages, setMessages] = createSignal([message('m0', 1)])
    const [streamingText, setStreamingText] = createSignal('Streaming answer')
    const { container } = render(() => (
      <ChatView
        messages={messages()}
        streamingText={streamingText()}
      />
    ))

    // Streaming ends: the persisted assistant row m2 becomes the (stream-covered) tail,
    // with an earlier tool row m1 appended alongside it and still premeasuring.
    batch(() => {
      setMessages([message('m0', 1), message('m1', 2), message('m2', 3)])
      setStreamingText('')
    })

    expect(container).toHaveTextContent('Streaming answer')
    // Past the show-delay, exactly one skeleton appears -- the interior loading row m1's
    // overlay (offset 100px). The stream-covered tail m2 (offset 200px) gets NONE even
    // as its earlier sibling skeletonises; it is covered by the bubble.
    vi.advanceTimersByTime(SKELETON_SHOW_DELAY_MS)
    const skeletons = [...container.querySelectorAll('[data-testid="row-skeleton"]')] as HTMLElement[]
    expect(skeletons).toHaveLength(1)
    expect(skeletons[0].parentElement!.style.transform).toBe('translateY(100px)')
    // The tail row itself is hidden (covered by the in-flow streaming bubble).
    expect((container.querySelector('[data-seq="3"]') as HTMLElement).style.visibility).toBe('hidden')
  })

  it('premeasures a look-ahead band of rows just beyond the rendered range', async () => {
    virtualizerState.attachedIds = []
    virtualizerState.measuredIds = new Set(['m0'])
    virtualizerState.currentHeightKeys = new Map()
    hiddenPremeasureState.candidates = []
    hiddenPremeasureState.onMeasure = undefined
    virtualizerState.setRange?.({ start: 0, end: 1 }) // only m0 is in the rendered range
    const [messages, setMessages] = createSignal([message('m0', 1)])
    render(() => (
      <ChatView
        messages={messages()}
        streamingText=""
      />
    ))

    // m1 and m2 sit BEYOND the rendered range but within LOOKAHEAD_PREMEASURE_ROWS, so they
    // are premeasured ahead of scrolling into view (previously only in-range rows were).
    setMessages([message('m0', 1), message('m1', 2), message('m2', 3)])

    await waitFor(() => {
      expect(hiddenPremeasureState.candidates.map(candidate => candidate.item.id).sort()).toEqual(['m1', 'm2'])
    })
  })

  it('mounts a fling skeleton for a MEASURED row entering mid-fling, upgrading on settle', async () => {
    virtualizerState.attachedIds = []
    virtualizerState.measuredIds = new Set(['m0', 'm8', 'm9'])
    virtualizerState.currentHeightKeys = new Map()
    virtualizerState.setRange?.({ start: 0, end: 1 })
    virtualizerState.setDeferred?.(false)
    const messages = Array.from({ length: 12 }, (_, index) => message(`m${index}`, index + 1))
    const { container } = render(() => (
      <ChatView
        messages={messages}
        streamingText=""
      />
    ))
    await Promise.resolve()
    // m0 mounted BEFORE the fling: it must stay a real bubble when the fling
    // starts (no downgrade — that would tear its DOM down mid-scroll).
    expect(visibleBubbleIds(container)).toEqual(['m0'])

    virtualizerState.setDeferred?.(true) // momentum fling in flight
    virtualizerState.setRange?.({ start: 0, end: 10 })
    await Promise.resolve()

    // Measured rows that ENTERED mid-fling render IN-ROW skeletons instead of
    // bubbles; the unmeasured ones mount real-but-hidden bubbles (so
    // measurement proceeds) with OVERLAY loading skeletons painting their
    // reserved slots. In-row skeletons sit inside the data-seq row; overlays
    // sit in their own positioned wrapper.
    const skeletons = [...container.querySelectorAll('[data-testid="row-skeleton"]')] as HTMLElement[]
    const inRow = skeletons.filter(s => s.parentElement?.hasAttribute('data-seq'))
    const overlay = skeletons.filter(s => !s.parentElement?.hasAttribute('data-seq'))
    expect(inRow).toHaveLength(2) // m8, m9 (m0 was already real)
    expect(overlay).toHaveLength(7) // m1..m7 (unmeasured, premeasure-hidden)
    expect(inRow[0].style.height).toBe('100px') // stub heightOfIndex
    // The body is ONE masked Oat fill block; its role="status" is what Oat's
    // `[role=status].skeleton` selector REQUIRES for the styles to apply.
    const fills = [...inRow[0].querySelectorAll('.skeleton.line')] as HTMLElement[]
    expect(fills).toHaveLength(1)
    expect(fills[0].getAttribute('role')).toBe('status')
    expect(visibleBubbleIds(container)).toEqual(
      ['m0', 'm1', 'm2', 'm3', 'm4', 'm5', 'm6', 'm7'],
    )

    virtualizerState.setDeferred?.(false) // fling settled
    await Promise.resolve()

    // Every IN-ROW skeleton upgraded to a real bubble, with a fading-out
    // skeleton COPY on top for the crossfade beat (inside the row but wrapped,
    // so no longer a direct data-seq child).
    expect(visibleBubbleIds(container)).toEqual(
      ['m0', 'm1', 'm2', 'm3', 'm4', 'm5', 'm6', 'm7', 'm8', 'm9'],
    )
    const during = [...container.querySelectorAll('[data-testid="row-skeleton"]')] as HTMLElement[]
    expect(during.filter(s => s.parentElement?.hasAttribute('data-seq'))).toHaveLength(0)
    const crossfading = during.filter(s => s.closest('[data-seq]') !== null)
    expect(crossfading).toHaveLength(2) // m8, m9 fading out over their bubbles
    expect(crossfading[0].parentElement!.classList.contains(rowSkeletonClosing)).toBe(true)

    // After the crossfade beat, only the 7 loading overlays (unmeasured rows)
    // remain.
    await waitFor(() => {
      const remaining = [...container.querySelectorAll('[data-testid="row-skeleton"]')] as HTMLElement[]
      expect(remaining.filter(s => s.closest('[data-seq]') !== null)).toHaveLength(0)
      expect(remaining).toHaveLength(7)
    })
  })

  it('attaches wheel and touch listeners as passive on the scroll container', () => {
    virtualizerState.attachedIds = []
    virtualizerState.measuredIds = new Set()
    virtualizerState.currentHeightKeys = new Map()
    // Non-passive wheel/touch listeners force the compositor to wait on the main
    // thread before starting a scroll; nothing in these handlers calls
    // preventDefault, so they must register passive (see
    // attachPassiveScrollListeners in ChatView).
    const spy = vi.spyOn(HTMLElement.prototype, 'addEventListener')
    try {
      const { container } = render(() => (
        <ChatView
          messages={[message('m0', 1)]}
          streamingText=""
        />
      ))
      const scroller = container.querySelector('[data-chat-scroll-container]')
      expect(scroller).toBeTruthy()
      const optionsByType = new Map<string, unknown>()
      spy.mock.calls.forEach((call, i) => {
        if (spy.mock.instances[i] === scroller)
          optionsByType.set(call[0], call[2])
      })
      for (const type of ['wheel', 'touchstart', 'touchmove', 'touchend', 'touchcancel'])
        expect(optionsByType.get(type), `${type} listener`).toEqual({ passive: true })
    }
    finally {
      spy.mockRestore()
    }
  })
})

describe('chat view native scrollbar hiding', () => {
  // The seq-space rail replaces the native scrollbar, but ONLY once it is active AND can
  // draw a thumb, or when there is no scrollable local-only content that needs the native
  // scrollbar fallback. Otherwise scrollable content would be left with no scrollbar at all.
  const railBase = {
    loaded: true,
    minSeq: 1n,
    maxSeq: 10n,
    marks: [],
    windowFirstSeq: 1n,
    windowLastSeq: 5n,
  }
  const scroller = (container: HTMLElement) => container.querySelector('[data-chat-scroll-container]') as HTMLElement

  it('hides the native scrollbar when the rail is loaded AND has a server row to anchor', () => {
    const { container } = render(() => (
      <ChatView messages={[message('m0', 1)]} streamingText="" rail={railBase} />
    ))
    expect(scroller(container).className).toContain(messageListRailActive)
  })

  it('hides the native scrollbar for an empty seeded rail because there is no native overflow to preserve', () => {
    const { container } = render(() => (
      <ChatView messages={[]} streamingText="" rail={{ ...railBase, windowFirstSeq: undefined, windowLastSeq: undefined }} />
    ))
    expect(scroller(container).className).toContain(messageListRailActive)
  })

  it('keeps the native scrollbar while the rail is unseeded (marks RPC failed / slow)', () => {
    const { container } = render(() => (
      <ChatView messages={[message('m0', 1)]} streamingText="" rail={{ ...railBase, loaded: false }} />
    ))
    expect(scroller(container).className).not.toContain(messageListRailActive)
  })

  it('keeps the native scrollbar for an all-optimistic-local window (no server seq to anchor)', () => {
    // A window of only optimistic locals (seq 0n) has no server row for the rail to anchor a thumb
    // to, so the rail hides itself and the native scrollbar must stay or overflowing local content
    // would have no scrollbar at all. windowFirst/LastSeq are undefined to match (no server seq).
    const { container } = render(() => (
      <ChatView messages={[message('m0', 0)]} streamingText="" rail={{ ...railBase, windowFirstSeq: undefined, windowLastSeq: undefined }} />
    ))
    expect(scroller(container).className).not.toContain(messageListRailActive)
  })

  it('keeps the native scrollbar when unsafe seq geometry prevents the rail from rendering', () => {
    const unsafe = BigInt(Number.MAX_SAFE_INTEGER)
    const unsafeMessage = create(AgentChatMessageSchema, {
      id: 'unsafe',
      source: MessageSource.AGENT,
      content: new TextEncoder().encode('unsafe seq'),
      seq: unsafe,
      createdAt: '2026-06-28T00:00:00.000Z',
      agentProvider: AgentProvider.CODEX,
    })

    const { container } = render(() => (
      <ChatView
        messages={[unsafeMessage]}
        streamingText=""
        rail={{
          ...railBase,
          minSeq: unsafe,
          maxSeq: unsafe,
          windowFirstSeq: unsafe,
          windowLastSeq: unsafe,
        }}
      />
    ))

    expect(scroller(container).className).not.toContain(messageListRailActive)
  })
})
