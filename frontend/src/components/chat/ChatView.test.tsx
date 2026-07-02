import type { JSX } from 'solid-js'
import type { ChatVirtualizerRange } from './useChatVirtualizer'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import { render, waitFor } from '@solidjs/testing-library'
import { batch, createEffect, createSignal, For } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AgentChatMessageSchema, AgentProvider, MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { rowSkeletonClosing } from './ChatView.css'

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

vi.mock('~/context/PreferencesContext', () => ({
  usePreferences: () => ({
    diffView: () => 'unified',
    expandAgentThoughts: () => true,
    showHiddenMessages: () => false,
  }),
}))

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

const { ChatView } = await import('./ChatView')
const { PRE_MEASURE_WIDTH_PX } = await import('./chatViewportGeometry')

afterEach(() => {
  viewportSizeObserverState.width = 640
  viewportSizeObserverState.height = 733
  viewportSizeObserverState.onWidth = undefined
  viewportSizeObserverState.onHeight = undefined
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

  it('does not collapse a newly appended live-tail row while premeasuring it', async () => {
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

    await waitFor(() => {
      expect(hiddenPremeasureState.candidates.map(candidate => candidate.item.id)).toEqual(['m1'])
    })

    const appendedRow = container.querySelector('[data-seq="2"]') as HTMLElement | null
    expect(appendedRow).not.toBeNull()
    expect(appendedRow!.style.visibility).not.toBe('hidden')
    expect(appendedRow!.style.opacity).toBe('1')
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

  it('renders a newly visible interior row invisible until measured, but not the live tail', () => {
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

    // Interior unmeasured rows are protected by INVISIBILITY (not a 0-height collapse):
    // m1 (interior, unmeasured) renders hidden until its height commits; m2 (live tail)
    // stays visible. This is applied synchronously (a createComputed), before any async turn.
    const interior = container.querySelector('[data-seq="2"]') as HTMLElement | null
    const tail = container.querySelector('[data-seq="3"]') as HTMLElement | null
    expect(interior).not.toBeNull()
    expect(interior!.style.visibility).toBe('hidden')
    expect(tail).not.toBeNull()
    expect(tail!.style.visibility).not.toBe('hidden')
  })

  it('paints a loading skeleton over a premeasure-hidden row\'s reserved slot, but not the live tail', async () => {
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

    // m1 (interior, unmeasured, premeasure-hidden) gets its reserved slot
    // painted by the skeleton overlay; m0 (measured) and m2 (live tail, never
    // hidden) do not.
    const skeletons = [...container.querySelectorAll('[data-testid="row-skeleton"]')] as HTMLElement[]
    expect(skeletons).toHaveLength(1)
    expect(skeletons[0].style.height).toBe('100px') // stub heightOfIndex (the reserved estimate)
    expect(skeletons[0].parentElement!.style.transform).toBe('translateY(100px)') // stub offset of m1

    // Once m1's height commits, the real row shows and the overlay CROSSFADES
    // out: it lingers for one SKELETON_CROSSFADE_MS beat in the fading-out
    // wrapper instead of popping away.
    virtualizerState.measuredIds.add('m1')
    virtualizerState.setRange?.({ start: 0, end: 3 }) // bump the stub's version signal
    const interior = container.querySelector('[data-seq="2"]') as HTMLElement
    expect(interior.style.visibility).not.toBe('hidden')
    const lingering = [...container.querySelectorAll('[data-testid="row-skeleton"]')] as HTMLElement[]
    expect(lingering).toHaveLength(1)
    expect(lingering[0].parentElement!.classList.contains(rowSkeletonClosing)).toBe(true)

    // After the crossfade beat the skeleton unmounts for good.
    await waitFor(() => {
      expect(container.querySelectorAll('[data-testid="row-skeleton"]')).toHaveLength(0)
    })
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
