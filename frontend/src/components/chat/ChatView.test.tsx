import type { JSX } from 'solid-js'
import type { ChatVirtualizerRange } from './useChatVirtualizer'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import { render, waitFor } from '@solidjs/testing-library'
import { batch, createEffect, createSignal, For } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AgentChatMessageSchema, AgentProvider, MessageSource } from '~/generated/leapmux/v1/agent_pb'

type HiddenPremeasureOnMeasure = (id: string, height: number, heightKey: string | undefined, measureDurationMs: number, settled: boolean) => boolean

const virtualizerState = vi.hoisted(() => ({
  range: { start: 0, end: 1 } as ChatVirtualizerRange,
  setRange: undefined as undefined | ((range: ChatVirtualizerRange) => void),
  attachedIds: [] as string[],
  measuredIds: new Set<string>(),
  currentHeightKeys: new Map<string, string | undefined>(),
  collapsedIds: new Set<string>(),
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
  virtualizerState.setRange = (range: ChatVirtualizerRange) => {
    virtualizerState.range = range
    setVersion(v => v + 1)
  }
  return {
    ...actual,
    useChatVirtualizer: () => ({
      mountedIds: new Set<string>(),
      range: () => {
        version()
        return virtualizerState.range
      },
      totalHeight: () => 10_000,
      offsetOfId: (id: string) => Number(id.slice(1)) * 100,
      hasMeasuredHeight: (id: string) => {
        version()
        return virtualizerState.measuredIds.has(id)
      },
      hasPendingPremeasuredHeight: () => false,
      setCollapsedUntilMeasuredIds: (ids: ReadonlySet<string>) => {
        virtualizerState.collapsedIds = new Set(ids)
      },
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
    virtualizerState.collapsedIds = new Set()
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
    virtualizerState.collapsedIds = new Set()
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
    virtualizerState.collapsedIds = new Set()
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
    virtualizerState.collapsedIds = new Set()
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
    virtualizerState.collapsedIds = new Set()
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
    expect(virtualizerState.collapsedIds.has('m1')).toBe(false)
  })

  it('keeps streaming text in flow until its replacement row is measured', async () => {
    virtualizerState.attachedIds = []
    virtualizerState.measuredIds = new Set()
    virtualizerState.currentHeightKeys = new Map()
    virtualizerState.collapsedIds = new Set()
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

  it('collapses a newly visible interior row before the next async turn', () => {
    virtualizerState.attachedIds = []
    virtualizerState.measuredIds = new Set(['m0'])
    virtualizerState.currentHeightKeys = new Map()
    virtualizerState.collapsedIds = new Set()
    hiddenPremeasureState.candidates = []
    hiddenPremeasureState.onMeasure = undefined
    virtualizerState.setRange?.({ start: 0, end: 1 })
    const [messages, setMessages] = createSignal([message('m0', 1)])
    render(() => (
      <ChatView
        messages={messages()}
        streamingText=""
      />
    ))

    virtualizerState.setRange?.({ start: 0, end: 3 })
    setMessages([message('m0', 1), message('m1', 2), message('m2', 3)])

    expect(virtualizerState.collapsedIds.has('m1')).toBe(true)
    expect(virtualizerState.collapsedIds.has('m2')).toBe(false)
  })
})
