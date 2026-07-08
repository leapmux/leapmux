import type { ChatVirtualizerRange, VirtualItem } from './useChatVirtualizer'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { CommandStreamSegment } from '~/stores/chatTypes'
import { create } from '@bufbuild/protobuf'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { batch, createEffect, createSignal, For } from 'solid-js'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { AgentChatMessageSchema, AgentProvider, AgentStatus, ContentCompression, MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { KEY_BROWSER_PREFS, localStorageSet } from '~/lib/browserStorage'
import { flushAnimationFrame, installControllableResizeObserver, triggerResizeObserverFor, triggerResizeObservers } from '~/test-support/resizeObserverStub'
import { ChatView, rowChromeHeightKey, SKELETON_SHOW_DELAY_MS } from './ChatView'
import { messageListRailActive, rowSkeletonClosing } from './ChatView.css'
import { computeOverscanPx, PRE_MEASURE_WIDTH_PX } from './chatViewportGeometry'
import { sameVirtualItems } from './useChatVirtualizer'

const A_TXT_RE = /a\.txt/
const B_TXT_RE = /b\.txt/

type HiddenPremeasureOnMeasure = (id: string, height: number, heightKey: string | undefined, measureDurationMs: number, settled: boolean) => boolean

// ChatView is exercised by two complementary unit suites in this file. The
// premeasure/skeleton/fling/rail suites are white-box: they drive a fully
// stubbed virtualizer, scroll, viewport observer, message bubble and hidden-
// premeasure so row geometry and visibility are deterministic. The
// rendering/pagination/classification suites are black-box: they render the
// REAL modules against a controllable ResizeObserver.
//
// `vi.mock` is hoisted per-file, so a single set of partial mocks serves both:
// each wrapper forwards to the real implementation (vi.importActual) unless
// `mockState.mockDeps` is set, in which case it returns the stub. Every suite
// flips the flag in its own beforeEach. This keeps ONE solid-js module instance
// (no vi.resetModules), so the real-module suites still render correctly.
const mockState = vi.hoisted(() => ({ mockDeps: false }))

const virtualizerState = vi.hoisted(() => ({
  range: { start: 0, end: 1 } as ChatVirtualizerRange,
  setRange: undefined as undefined | ((range: ChatVirtualizerRange) => void),
  attachedIds: [] as string[],
  measuredIds: new Set<string>(),
  currentHeightKeys: new Map<string, string | undefined>(),
  setDeferred: undefined as undefined | ((deferred: boolean) => void),
}))

const hiddenPremeasureState = vi.hoisted(() => ({
  candidates: [] as ReadonlyArray<{ entry: unknown, item: { id: string, heightKey?: string } }>,
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
  const actual = await vi.importActual<typeof import('~/context/PreferencesContext')>('~/context/PreferencesContext')
  const { createSignal } = await import('solid-js')
  const [diffView, setDiffView] = createSignal<'unified' | 'split'>('unified')
  const [expandThoughts, setExpandThoughts] = createSignal(true)
  prefsState.setDiffView = setDiffView
  prefsState.setExpandThoughts = setExpandThoughts
  return {
    ...actual,
    usePreferences: () => {
      if (!mockState.mockDeps)
        return actual.usePreferences()
      return {
        diffView,
        expandAgentThoughts: expandThoughts,
        showHiddenMessages: () => false,
      }
    },
  }
})

vi.mock('./MessageBubble', async () => {
  const actual = await vi.importActual<typeof import('./MessageBubble')>('./MessageBubble')
  return {
    ...actual,
    // A single-return ternary keeps reactivity intact (mockDeps is fixed for a
    // component's lifetime, set once per test) without tripping solid/components-return-once.
    MessageBubble: (props: Parameters<typeof actual.MessageBubble>[0]) => (
      mockState.mockDeps
        ? (
            <div
              data-testid="mock-message-bubble"
              data-message-id={props.message.id}
              data-premeasure={props.premeasureMode ? 'true' : 'false'}
            />
          )
        : actual.MessageBubble(props)
    ),
  }
})

vi.mock('./chatHiddenPremeasure', async () => {
  const actual = await vi.importActual<typeof import('./chatHiddenPremeasure')>('./chatHiddenPremeasure')
  // The stub captures each pass's candidates/onMeasure into hoisted state and renders
  // the bubbles so the visible-slice tests can drive measurement deterministically.
  const MockHiddenPremeasure = (props: Parameters<typeof actual.ChatHiddenPremeasure>[0]) => {
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
  }
  return {
    ...actual,
    ChatHiddenPremeasure: (props: Parameters<typeof actual.ChatHiddenPremeasure>[0]) => (
      mockState.mockDeps ? MockHiddenPremeasure(props) : actual.ChatHiddenPremeasure(props)
    ),
  }
})

vi.mock('./chatViewportGeometry', async () => {
  const actual = await vi.importActual<typeof import('./chatViewportGeometry')>('./chatViewportGeometry')
  return {
    ...actual,
    createViewportSizeObserver: (opts: Parameters<typeof actual.createViewportSizeObserver>[0]) => {
      if (!mockState.mockDeps)
        return actual.createViewportSizeObserver(opts)
      return {
        observe: () => {
          viewportSizeObserverState.onWidth = opts.onWidth
          viewportSizeObserverState.onHeight = opts.onHeight
          opts.onWidth(viewportSizeObserverState.width)
          opts.onHeight(viewportSizeObserverState.height)
        },
        disconnect: vi.fn(),
      }
    },
  }
})

vi.mock('./useChatScroll', async () => {
  const actual = await vi.importActual<typeof import('./useChatScroll')>('./useChatScroll')
  return {
    ...actual,
    useChatScroll: (...args: Parameters<typeof actual.useChatScroll>) => {
      if (!mockState.mockDeps)
        return actual.useChatScroll(...args)
      return {
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
      }
    },
  }
})

vi.mock('./useChatVirtualizer', async () => {
  const actual = await vi.importActual<typeof import('./useChatVirtualizer')>('./useChatVirtualizer')
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
    useChatVirtualizer: (...args: Parameters<typeof actual.useChatVirtualizer>) => {
      if (!mockState.mockDeps)
        return actual.useChatVirtualizer(...args)
      return {
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
      }
    },
  }
})

// jsdom does not provide ResizeObserver or Worker
beforeAll(() => {
  installControllableResizeObserver()
  globalThis.Worker ??= class {
    onmessage: ((e: MessageEvent) => void) | null = null
    onerror: ((e: ErrorEvent) => void) | null = null
    postMessage() {}
    terminate() {}
    addEventListener() {}
    removeEventListener() {}
    dispatchEvent() { return false }
  } as unknown as typeof Worker
})

// Real modules by default; the stubbed-deps suite flips this to true.
beforeEach(() => {
  mockState.mockDeps = false
})

async function reportChatViewportSize(view: { container: HTMLElement }, width = 800, height = 733): Promise<void> {
  const scrollContainer = view.container.querySelector('[data-chat-scroll-container]') as HTMLElement | null
  expect(scrollContainer).not.toBeNull()
  vi.spyOn(scrollContainer!, 'getBoundingClientRect').mockImplementation(() => ({
    width,
    height,
  }) as DOMRect)
  await triggerResizeObserverFor(scrollContainer!)
}

// `role` ('user' | 'assistant') only labels the call site for readability;
// ChatView classifies messages off `message.source` (left UNSPECIFIED here,
// matching the historical behavior of these tests), not a `role` field —
// AgentChatMessage has no `role`, so it is intentionally not placed on the
// returned proto.
function makeMessage(role: string, text: string, id: string = '1'): AgentChatMessage {
  const content = JSON.stringify({
    message: {
      content: [{ type: 'text', text }],
    },
  })
  return {
    $typeName: 'leapmux.v1.AgentChatMessage',
    id,
    content: new TextEncoder().encode(content),
    contentCompression: ContentCompression.NONE,
    seq: 1n,
    createdAt: '',
    // A real persisted message always carries a provider; classifyMessage no
    // longer falls back to Claude for an unset one (it would be unsupported_provider).
    agentProvider: AgentProvider.CLAUDE_CODE,
    // Partial literal cast to the proto type, matching the sibling
    // makeCodex* helpers below — the omitted proto fields default to
    // their zero values, preserving these tests' historical behavior.
  } as AgentChatMessage
}

function makeCodexCommandMessage(params: {
  id: string
  seq: bigint
  spanId: string
  status: string
  command?: string
  aggregatedOutput?: string
  processId?: string
  exitCode?: number
}): AgentChatMessage {
  return {
    $typeName: 'leapmux.v1.AgentChatMessage',
    id: params.id,
    source: MessageSource.AGENT,
    content: new TextEncoder().encode(JSON.stringify({
      item: {
        type: 'commandExecution',
        id: params.spanId,
        command: params.command ?? 'echo hi',
        cwd: '/tmp',
        processId: params.processId,
        status: params.status,
        aggregatedOutput: params.aggregatedOutput ?? '',
        exitCode: params.exitCode,
      },
      threadId: 'thread-1',
      turnId: 'turn-1',
    })),
    contentCompression: ContentCompression.NONE,
    seq: params.seq,
    createdAt: '',
    agentProvider: AgentProvider.CODEX,
    spanId: params.spanId,
    spanType: 'commandExecution',
  } as AgentChatMessage
}

function makeCodexFileChangeMessage(params: {
  id: string
  seq: bigint
  spanId: string
  status: string
  diff?: string
  changes?: Array<Record<string, unknown>>
}): AgentChatMessage {
  return {
    $typeName: 'leapmux.v1.AgentChatMessage',
    id: params.id,
    source: MessageSource.AGENT,
    content: new TextEncoder().encode(JSON.stringify({
      item: {
        type: 'fileChange',
        id: params.spanId,
        status: params.status,
        changes: params.changes ?? (params.status === 'completed'
          ? [{ path: 'a.txt', kind: 'update', diff: params.diff ?? '@@ -1 +1 @@\n-old\n+new' }]
          : []),
      },
      threadId: 'thread-1',
      turnId: 'turn-1',
    })),
    contentCompression: ContentCompression.NONE,
    seq: params.seq,
    createdAt: '',
    agentProvider: AgentProvider.CODEX,
    spanId: params.spanId,
    spanType: 'fileChange',
  } as AgentChatMessage
}

function makeCodexReasoningMessage(params: {
  id: string
  seq: bigint
  spanId: string
  summary?: string[]
  content?: string[]
}): AgentChatMessage {
  return {
    $typeName: 'leapmux.v1.AgentChatMessage',
    id: params.id,
    source: MessageSource.AGENT,
    content: new TextEncoder().encode(JSON.stringify({
      item: {
        type: 'reasoning',
        id: params.spanId,
        summary: params.summary ?? [],
        content: params.content ?? [],
      },
      threadId: 'thread-1',
      turnId: 'turn-1',
    })),
    contentCompression: ContentCompression.NONE,
    seq: params.seq,
    createdAt: '',
    agentProvider: AgentProvider.CODEX,
    spanId: params.spanId,
    spanType: 'reasoning',
  } as AgentChatMessage
}

function makeCodexTurnPlanMessage(params: {
  id: string
  seq: bigint
  explanation?: string | null
  plan: Array<{ step: string, status: string }>
}): AgentChatMessage {
  return {
    $typeName: 'leapmux.v1.AgentChatMessage',
    id: params.id,
    source: MessageSource.AGENT,
    content: new TextEncoder().encode(JSON.stringify({
      method: 'turn/plan/updated',
      params: {
        threadId: 'thread-1',
        turnId: 'turn-1',
        explanation: params.explanation ?? null,
        plan: params.plan,
      },
    })),
    contentCompression: ContentCompression.NONE,
    seq: params.seq,
    createdAt: '',
    agentProvider: AgentProvider.CODEX,
  } as AgentChatMessage
}

function makeCodexWebSearchMessage(params: {
  id: string
  seq: bigint
  spanId: string
  query?: string
  action?: Record<string, unknown>
  completed?: boolean
}): AgentChatMessage {
  return {
    $typeName: 'leapmux.v1.AgentChatMessage',
    id: params.id,
    source: MessageSource.AGENT,
    content: new TextEncoder().encode(JSON.stringify({
      item: {
        type: 'webSearch',
        id: params.spanId,
        query: params.query ?? '',
        action: params.action ?? { type: 'other' },
      },
      threadId: 'thread-1',
      turnId: 'turn-1',
    })),
    contentCompression: ContentCompression.NONE,
    seq: params.seq,
    createdAt: '',
    agentProvider: AgentProvider.CODEX,
    spanId: params.spanId,
    spanType: 'webSearch',
    spanLines: params.completed ? JSON.stringify([{ span_id: params.spanId, color: 1, type: 'connector_end' }]) : '[]',
  } as AgentChatMessage
}

function makeCodexHiddenLifecycleMessage(id: string = 'codex-hidden'): AgentChatMessage {
  return {
    $typeName: 'leapmux.v1.AgentChatMessage',
    id,
    source: MessageSource.LEAPMUX,
    content: new TextEncoder().encode(JSON.stringify({
      type: 'notification_thread',
      old_seqs: [],
      messages: [
        {
          method: 'thread/started',
          params: { threadId: 'thread-1' },
        },
      ],
    })),
    contentCompression: ContentCompression.NONE,
    seq: 1n,
    createdAt: '',
    agentProvider: AgentProvider.CODEX,
  } as AgentChatMessage
}

function makeClaudeEnterPlanModeResultMessage(id: string = 'claude-enter-plan-result'): AgentChatMessage {
  return {
    $typeName: 'leapmux.v1.AgentChatMessage',
    id,
    source: MessageSource.USER,
    content: new TextEncoder().encode(JSON.stringify({
      type: 'user',
      message: {
        role: 'user',
        content: [
          {
            type: 'tool_result',
            content: 'Entered plan mode. You should now focus on exploring the codebase and designing an implementation approach.',
            tool_use_id: 'toolu_01U3MQbUE7bmTs1SnJx4SPU3',
          },
        ],
      },
      tool_use_result: {
        message: 'Entered plan mode. You should now focus on exploring the codebase and designing an implementation approach.',
      },
    })),
    contentCompression: ContentCompression.NONE,
    seq: 1n,
    createdAt: '',
    agentProvider: AgentProvider.CLAUDE_CODE,
    spanId: 'toolu_01U3MQbUE7bmTs1SnJx4SPU3',
    spanType: 'EnterPlanMode',
  } as AgentChatMessage
}

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

describe('computeOverscanPx', () => {
  it('floors short panes and the pre-measurement frame at 800px', () => {
    expect(computeOverscanPx(0)).toBe(800) // pre-measurement frame
    expect(computeOverscanPx(-10)).toBe(800) // defensive
    expect(computeOverscanPx(400)).toBe(800) // 400*1.5=600 < floor
    expect(computeOverscanPx(533)).toBe(800) // ~533*1.5=800 (floor boundary)
  })

  it('scales with the viewport in the mid-range', () => {
    expect(computeOverscanPx(800)).toBe(1200) // 800*1.5
    expect(computeOverscanPx(1000)).toBe(1500) // 1000*1.5
  })

  it('caps tall panes at the 2400px ceiling', () => {
    expect(computeOverscanPx(1600)).toBe(2400) // 1600*1.5=2400 (ceiling boundary)
    expect(computeOverscanPx(2200)).toBe(2400) // 2200*1.5=3300 -> capped
    expect(computeOverscanPx(10000)).toBe(2400) // far past the cap
  })
})

describe('samevirtualitems', () => {
  const item = (id: string, hasSpanLines = false, heightKey?: string): VirtualItem => ({ id, hasSpanLines, heightKey })

  it('is true for the same reference and for a geometry-equivalent rebuild', () => {
    const a = [item('m1', false, '1'), item('m2', true, '2')]
    expect(sameVirtualItems(a, a)).toBe(true)
    // A fresh array (a streaming/command-stream delta re-walk) with identical
    // id/hasSpanLines/heightKey is geometry-equivalent -> keep the prior, no churn.
    const b = [item('m1', false, '1'), item('m2', true, '2')]
    expect(sameVirtualItems(a, b)).toBe(true)
  })

  it('is false when a content version, span-line flag, id, or length changes', () => {
    const base = [item('m1', false, '1'), item('m2', true, '2')]
    expect(sameVirtualItems(base, [item('m1', false, '1'), item('m2', true, '3')])).toBe(false) // heightKey bumped
    expect(sameVirtualItems(base, [item('m1', true, '1'), item('m2', true, '2')])).toBe(false) // hasSpanLines flipped
    expect(sameVirtualItems(base, [item('mX', false, '1'), item('m2', true, '2')])).toBe(false) // id changed (reorder/insert)
    expect(sameVirtualItems(base, [item('m1', false, '1')])).toBe(false) // a row appeared/left
  })
})

describe('rowChromeHeightKey', () => {
  it('changes when delivery error or pending-label chrome changes', () => {
    const base = rowChromeHeightKey(undefined, undefined)
    expect(rowChromeHeightKey('failed', undefined)).not.toBe(base)
    expect(rowChromeHeightKey(undefined, 'queued')).not.toBe(base)
    expect(rowChromeHeightKey('failed', 'queued')).not.toBe(rowChromeHeightKey('failed', undefined))
  })
})

describe('chatView', () => {
  it('renders empty state when no messages', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView messages={[]} streamingText="" />
      </PreferencesProvider>
    ))
    expect(screen.getByText('Send a message to start')).toBeInTheDocument()
  })

  it('renders the older-loading indicator as an overlay OUTSIDE the scroll container', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView messages={[]} streamingText="" pagination={{ hasOlderMessages: true, fetchingOlder: true }} />
      </PreferencesProvider>
    ))
    const indicator = screen.getByText('Loading older messages...')
    expect(indicator).toBeInTheDocument()
    // An IN-FLOW indicator inside the scroll container shifts the virtualized content
    // by its height when fetchingOlder toggles -- a shift the anchor re-pin can't see
    // (its offset map covers only the virtual rows) -- so a scrolled-up reader bounces
    // and gets wedged re-triggering loadOlder. It must be an overlay sibling of the
    // scroll container, never a descendant.
    const scrollContainer = document.querySelector('[data-chat-scroll-container="true"]')
    expect(scrollContainer).toBeInTheDocument()
    expect(scrollContainer!.contains(indicator)).toBe(false)
  })

  it('gates the older-loading indicator on the top-edge stall, not the raw fetchingOlder flag', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView messages={[]} streamingText="" pagination={{ hasOlderMessages: true, fetchingOlder: true }} />
      </PreferencesProvider>
    ))
    // jsdom computes no layout, so give the scroll container real geometry: content
    // 5000 over a 500 viewport (max scrollTop 4500). scrollTop is a get/set pair so a
    // scroll event reads whatever we last set.
    const sc = document.querySelector('[data-chat-scroll-container="true"]') as HTMLElement
    let top = 0
    Object.defineProperty(sc, 'scrollHeight', { value: 5000, configurable: true })
    Object.defineProperty(sc, 'clientHeight', { value: 500, configurable: true })
    Object.defineProperty(sc, 'scrollTop', {
      get: () => top,
      set: (v) => { top = v },
      configurable: true,
    })

    // Off the top edge: the older fetch is still in flight, but it is now a background
    // pre-fetch (not a stall), so the indicator must stay dark.
    top = 2000
    fireEvent.scroll(sc)
    expect(screen.queryByText('Loading older messages...')).not.toBeInTheDocument()

    // Clamped at the top edge with the same fetch in flight: now it is a genuine stall.
    top = 0
    fireEvent.scroll(sc)
    expect(screen.getByText('Loading older messages...')).toBeInTheDocument()
  })

  it('shows the newer-loading indicator and hides the scroll-to-bottom button while stalled at the bottom', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView messages={[]} streamingText="" pagination={{ hasNewerMessages: true, fetchingNewer: true }} />
      </PreferencesProvider>
    ))
    const indicator = screen.getByText('Loading newer messages...')
    expect(indicator).toBeInTheDocument()
    // The newer indicator takes the scroll-to-bottom button's bottom-center slot, so the
    // button is hidden for the duration of the stall (the only button in this render).
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
    // Same overlay contract as the older indicator: a sibling of the scroll container,
    // never a descendant (an in-flow indicator would shift the virtualized content).
    const scrollContainer = document.querySelector('[data-chat-scroll-container="true"]')
    expect(scrollContainer!.contains(indicator)).toBe(false)
  })

  it('shows the scroll-to-bottom button (no newer indicator) when newer messages exist but no fetch is stalling', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView messages={[]} streamingText="" pagination={{ hasNewerMessages: true }} />
      </PreferencesProvider>
    ))
    expect(screen.queryByText('Loading newer messages...')).not.toBeInTheDocument()
    expect(screen.getByRole('button')).toBeInTheDocument()
  })

  // The AgentStartupBanner sub-component is the visible surface of the
  // backend's phased STARTING broadcasts. These tests lock in the
  // fallback → phase-message → error contract so a regression in the
  // startup-panel plumbing is caught at the unit level.
  it('shows the default "Starting <provider>…" label when no startup_message is set', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView
          messages={[]}
          streamingText=""
          agentLifecycle={{ agentStatus: AgentStatus.STARTING, providerLabel: 'Claude Code' }}
        />
      </PreferencesProvider>
    ))
    expect(screen.getByTestId('agent-startup-overlay')).toBeInTheDocument()
    expect(screen.getByText('Starting Claude Code…')).toBeInTheDocument()
  })

  it('shows the backend startup_message when one is provided (overrides the default)', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView
          messages={[]}
          streamingText=""
          agentLifecycle={{ agentStatus: AgentStatus.STARTING, providerLabel: 'Claude Code', startupMessage: 'Checking Git status…' }}
        />
      </PreferencesProvider>
    ))
    expect(screen.getByText('Checking Git status…')).toBeInTheDocument()
    // Default label must not also render — the backend message wins.
    expect(screen.queryByText('Starting Claude Code…')).not.toBeInTheDocument()
  })

  it('falls back to the default label when startup_message is empty string', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView
          messages={[]}
          streamingText=""
          agentLifecycle={{ agentStatus: AgentStatus.STARTING, providerLabel: 'Claude Code', startupMessage: '' }}
        />
      </PreferencesProvider>
    ))
    expect(screen.getByText('Starting Claude Code…')).toBeInTheDocument()
  })

  it('renders the startup-error banner with the server error on STARTUP_FAILED', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView
          messages={[]}
          streamingText=""
          agentLifecycle={{ agentStatus: AgentStatus.STARTUP_FAILED, providerLabel: 'Claude Code', startupError: 'exec: claude: not found' }}
        />
      </PreferencesProvider>
    ))
    expect(screen.getByTestId('agent-startup-error')).toBeInTheDocument()
    expect(screen.getByText('Claude Code failed to start')).toBeInTheDocument()
    expect(screen.getByText('exec: claude: not found')).toBeInTheDocument()
  })

  it('hides the trailing startup banner while windowed away from the live tail', () => {
    // Visible history is present (non-empty branch renders), the agent is STARTING,
    // but the window is scrolled away from the tail. The startup banner is tail-
    // anchored like streaming/thinking, so it must NOT paint mid-history here.
    render(() => (
      <PreferencesProvider>
        <ChatView
          messages={[makeMessage('user', 'hello', 'm1')]}
          streamingText=""
          agentLifecycle={{ agentStatus: AgentStatus.STARTING, providerLabel: 'Claude Code' }}
          pagination={{ hasNewerMessages: true }}
        />
      </PreferencesProvider>
    ))
    expect(screen.queryByTestId('agent-startup-overlay')).not.toBeInTheDocument()
  })

  it('shows the trailing startup banner at the live tail with visible history', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView
          messages={[makeMessage('user', 'hello', 'm1')]}
          streamingText=""
          agentLifecycle={{ agentStatus: AgentStatus.STARTING, providerLabel: 'Claude Code' }}
          pagination={{ hasNewerMessages: false }}
        />
      </PreferencesProvider>
    ))
    expect(screen.getByTestId('agent-startup-overlay')).toBeInTheDocument()
  })

  it('renders empty state when all messages are hidden', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView messages={[makeCodexHiddenLifecycleMessage()]} streamingText="" />
      </PreferencesProvider>
    ))
    expect(screen.getByText('Send a message to start')).toBeInTheDocument()
  })

  it('does not show the empty state for an all-hidden window page when more history exists', () => {
    // A windowed page that is entirely hidden messages is NOT an empty chat —
    // there is older history to page in. Showing "Send a message to start" here
    // would be the mid-history blank-page bug.
    render(() => (
      <PreferencesProvider>
        <ChatView messages={[makeCodexHiddenLifecycleMessage()]} streamingText="" pagination={{ hasOlderMessages: true }} />
      </PreferencesProvider>
    ))
    expect(screen.queryByText('Send a message to start')).not.toBeInTheDocument()
  })

  it('hides EnterPlanMode tool_result messages in chat history', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView messages={[makeClaudeEnterPlanModeResultMessage()]} streamingText="" />
      </PreferencesProvider>
    ))
    expect(screen.getByText('Send a message to start')).toBeInTheDocument()
    expect(screen.queryByText('Entered plan mode')).not.toBeInTheDocument()
  })

  it('renders messages', () => {
    const messages = [
      makeMessage('user', 'Hello', '1'),
      makeMessage('assistant', 'Hi there', '2'),
    ]
    render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))
    expect(screen.getByText('Hello')).toBeInTheDocument()
    expect(screen.getByText('Hi there')).toBeInTheDocument()
  })

  it('hides a trailing optimistic local while windowed away from the live tail', () => {
    const messages = [
      makeMessage('assistant', 'Server reply', 'm1'),
      { ...makeMessage('user', 'My pending message', 'local-1'), seq: 0n },
    ]
    render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" pagination={{ hasNewerMessages: true }} />
      </PreferencesProvider>
    ))
    // The server message renders; the optimistic local (seq 0n) is hidden -- it
    // belongs at the live tail, not stranded after a scrolled-away window. It
    // stays in the store and reappears on jump-to-latest.
    expect(screen.getByText('Server reply')).toBeInTheDocument()
    expect(screen.queryByText('My pending message')).toBeNull()
  })

  it('renders a trailing optimistic local at the live tail', () => {
    const messages = [
      makeMessage('assistant', 'Server reply', 'm1'),
      { ...makeMessage('user', 'My pending message', 'local-1'), seq: 0n },
    ]
    render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" pagination={{ hasNewerMessages: false }} />
      </PreferencesProvider>
    ))
    expect(screen.getByText('Server reply')).toBeInTheDocument()
    expect(screen.getByText('My pending message')).toBeInTheDocument()
  })

  it('renders streaming text', async () => {
    render(() => (
      <PreferencesProvider>
        <ChatView messages={[]} streamingText="Thinking..." />
      </PreferencesProvider>
    ))
    // Streaming text rendering is throttled via requestAnimationFrame
    await waitFor(() => expect(screen.getByText('Thinking...')).toBeInTheDocument())
  })

  it('renders chat container', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView messages={[]} streamingText="" />
      </PreferencesProvider>
    ))
    expect(screen.getByTestId('chat-container')).toBeInTheDocument()
  })

  it('renders live command stream inside the matching codex command bubble', () => {
    const messages = [
      makeCodexCommandMessage({ id: 'cmd-start', seq: 1n, spanId: 'cmd-1', status: 'in_progress' }),
    ]
    const commandStream: CommandStreamSegment[] = [
      { kind: 'output', text: 'building...\n' },
      { kind: 'interaction', text: 'y\n' },
    ]

    render(() => (
      <PreferencesProvider>
        <ChatView
          messages={messages}
          streamingText=""
          lookups={{ getCommandStreamBySpanId: () => commandStream }}
        />
      </PreferencesProvider>
    ))

    expect(screen.getByText('building...')).toBeInTheDocument()
    expect(screen.getByText('> y')).toBeInTheDocument()
  })

  it('reveals an empty codex reasoning row once its command stream starts streaming', async () => {
    // An empty reasoning envelope (no summary/content) classifies as hidden
    // until its span streams — then it becomes a visible row. The entry cache
    // keys on seq, so without ALSO re-checking command-stream presence the row
    // would freeze on its first (hidden) classification and never appear. A
    // visible anchor message keeps the list rendered so only the reasoning row's
    // own classification decides whether it shows.
    const messages = [
      { ...makeMessage('assistant', 'anchor', 'anchor-1'), seq: 1n },
      makeCodexReasoningMessage({ id: 'reasoning-1', seq: 2n, spanId: 'reasoning-span-1' }),
    ]
    let setStream!: (s: CommandStreamSegment[]) => void

    const view = render(() => {
      const [stream, updateStream] = createSignal<CommandStreamSegment[]>([])
      setStream = updateStream
      return (
        <PreferencesProvider>
          <ChatView
            messages={messages}
            streamingText=""
            lookups={{
              getCommandStreamBySpanId: () => stream(),
              hasRenderableCommandStreamBySpanId: () => stream().length > 0,
            }}
          />
        </PreferencesProvider>
      )
    })

    // No command stream yet -> the empty reasoning row (seq 2) is hidden.
    expect(view.container.querySelector('[data-seq="2"]')).toBeNull()
    expect(view.container.querySelector('[data-seq="1"]')).not.toBeNull() // anchor renders

    // The span starts streaming -> the reasoning row must flip to visible.
    setStream([{ kind: 'reasoning_content', text: 'pondering...' }])
    await waitFor(() => expect(view.container.querySelector('[data-seq="2"]')).not.toBeNull())
  })

  it('keeps unmeasured interior and tail rows invisible while reserving their estimated space', async () => {
    const messages = [
      { ...makeMessage('assistant', 'Older tall output', 'msg-1'), seq: 1n },
      { ...makeMessage('assistant', 'Current message', 'msg-2'), seq: 2n },
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    const row = await waitFor(() => {
      const el = view.container.querySelector('[data-seq="1"]') as HTMLElement | null
      expect(el).not.toBeNull()
      return el!
    })
    const spacer = row.parentElement as HTMLElement | null
    expect(spacer).not.toBeNull()
    expect(row.style.visibility).toBe('')

    await reportChatViewportSize(view)

    await waitFor(() => expect(row.style.visibility).toBe('hidden'))
    expect(row.style.opacity).toBe('0')
    const tailRow = view.container.querySelector('[data-seq="2"]') as HTMLElement | null
    expect(tailRow).not.toBeNull()
    // The live tail is hidden-until-measured like any other in-range row, so its
    // unmeasured content can't overflow its estimated slot onto the trailing tail UI.
    expect(tailRow!.style.visibility).toBe('hidden')
    expect(tailRow!.style.opacity).toBe('0')
    // Both rows are unmeasured and reserve the seed estimate (96); hidden means invisible,
    // NOT collapsed-to-0, so both still reserve space -- the spacer is 96 + 20 gap + 96.
    expect(spacer!.style.height).toBe('212px')

    vi.spyOn(tailRow!, 'getBoundingClientRect').mockImplementation(() => ({ height: 32 }) as DOMRect)
    await triggerResizeObserverFor(tailRow!)
    // Tail measures 32 -> the median estimate (one 32px sample) is now 32, so the
    // still-unmeasured interior row reserves 32: spacer = 32 (interior est) + 20 gap
    // + 32 (tail).
    await waitFor(() => expect(spacer!.style.height).toBe('84px'))

    vi.spyOn(row, 'getBoundingClientRect').mockImplementation(() => ({ height: 480 }) as DOMRect)
    await triggerResizeObserverFor(row)

    await waitFor(() => expect(row.style.visibility).toBe(''))
    expect(row.style.opacity).toBe('1')
    // The tail measured first but was held until the earlier interior row revealed
    // (ordered append reveal); with both measured it is now visible too.
    expect(tailRow!.style.visibility).toBe('')
    expect(tailRow!.style.opacity).toBe('1')
    expect(spacer!.style.height).toBe('532px')
  })

  it('keeps streaming text visible until its replacement row is measured', async () => {
    const [messages, setMessages] = createSignal<AgentChatMessage[]>([])
    const [streamingText, setStreamingText] = createSignal('Streaming answer')
    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages()} streamingText={streamingText()} />
      </PreferencesProvider>
    ))

    await waitFor(() => expect(screen.getByText('Streaming answer')).toBeInTheDocument())

    await reportChatViewportSize(view)

    batch(() => {
      setMessages([{ ...makeMessage('assistant', 'Streaming answer', 'final-1'), seq: 1n }])
      setStreamingText('')
    })

    const row = await waitFor(() => {
      const el = view.container.querySelector('[data-seq="1"]') as HTMLElement | null
      expect(el).not.toBeNull()
      return el!
    })

    expect(row.style.visibility).toBe('hidden')
    expect(row.style.opacity).toBe('0')

    vi.spyOn(row, 'getBoundingClientRect').mockImplementation(() => ({ height: 48 }) as DOMRect)
    await triggerResizeObserverFor(row)

    await waitFor(() => expect(row.style.visibility).toBe(''))
    expect(row.style.opacity).toBe('1')
  })

  it('keeps streaming text visible when its replacement row appears before streaming clears', async () => {
    const [messages, setMessages] = createSignal<AgentChatMessage[]>([])
    const [streamingText, setStreamingText] = createSignal('Streaming answer')
    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages()} streamingText={streamingText()} />
      </PreferencesProvider>
    ))

    await waitFor(() => expect(screen.getByText('Streaming answer')).toBeInTheDocument())

    await reportChatViewportSize(view)

    setMessages([{ ...makeMessage('assistant', 'Streaming answer', 'final-1'), seq: 1n }])

    const row = await waitFor(() => {
      const el = view.container.querySelector('[data-seq="1"]') as HTMLElement | null
      expect(el).not.toBeNull()
      return el!
    })
    expect(row.style.visibility).toBe('hidden')
    expect(row.style.opacity).toBe('0')

    vi.spyOn(row, 'getBoundingClientRect').mockImplementation(() => ({ height: 48 }) as DOMRect)
    await triggerResizeObserverFor(row)
    expect(row.style.visibility).toBe('hidden')
    expect(row.style.opacity).toBe('0')

    setStreamingText('')

    await waitFor(() => expect(row.style.visibility).toBe(''))
    expect(row.style.opacity).toBe('1')
  })

  it('keeps streaming text visible until a post-hidden replacement row is measured', async () => {
    const prior = { ...makeMessage('assistant', 'Prior answer', 'prior-1'), seq: 1n }
    const hidden = { ...makeCodexHiddenLifecycleMessage('hidden-1'), seq: 2n }
    const final = { ...makeMessage('assistant', 'Streaming answer', 'final-1'), seq: 3n }
    const [messages, setMessages] = createSignal<AgentChatMessage[]>([prior])
    const [streamingText, setStreamingText] = createSignal('Streaming answer')
    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages()} streamingText={streamingText()} />
      </PreferencesProvider>
    ))

    await waitFor(() => expect(screen.getByText('Streaming answer')).toBeInTheDocument())

    await reportChatViewportSize(view)

    // Measure the prior row so the ordered-reveal gate doesn't hold the replacement
    // tail behind it -- in the running app an existing message is already measured, so
    // this isolates the stream->row handoff (the tail reveals on its OWN measurement).
    const priorRow = view.container.querySelector('[data-seq="1"]') as HTMLElement
    vi.spyOn(priorRow, 'getBoundingClientRect').mockImplementation(() => ({ height: 40 }) as DOMRect)
    await triggerResizeObserverFor(priorRow)

    batch(() => {
      setStreamingText('')
      setMessages([prior, hidden])
    })
    expect(view.container.querySelector('[data-seq="2"]')).toBeNull()

    setMessages([prior, hidden, final])

    const row = await waitFor(() => {
      const el = view.container.querySelector('[data-seq="3"]') as HTMLElement | null
      expect(el).not.toBeNull()
      return el!
    })
    expect(row.style.visibility).toBe('hidden')
    expect(row.style.opacity).toBe('0')

    vi.spyOn(row, 'getBoundingClientRect').mockImplementation(() => ({ height: 48 }) as DOMRect)
    await triggerResizeObserverFor(row)

    await waitFor(() => expect(row.style.visibility).toBe(''))
    expect(row.style.opacity).toBe('1')
  })

  it('preserves expanded codex reasoning state when the message updates and new messages are appended', async () => {
    localStorageSet(KEY_BROWSER_PREFS, { expandAgentThoughts: false })

    const initialMessages = [
      makeCodexReasoningMessage({
        id: 'reasoning-1',
        seq: 1n,
        spanId: 'reasoning-span-1',
        summary: ['Initial reasoning summary'],
      }),
    ]
    let setMessages!: (messages: AgentChatMessage[]) => void

    render(() => {
      const [messages, updateMessages] = createSignal(initialMessages)
      setMessages = updateMessages
      return (
        <PreferencesProvider>
          <ChatView messages={messages()} streamingText="" />
        </PreferencesProvider>
      )
    })

    expect(screen.queryByText('Initial reasoning summary')).not.toBeInTheDocument()
    fireEvent.click(screen.getByText('Thinking'))
    expect(screen.getByText('Initial reasoning summary')).toBeInTheDocument()

    setMessages([
      makeCodexReasoningMessage({
        id: 'reasoning-1',
        seq: 2n,
        spanId: 'reasoning-span-1',
        summary: ['Initial reasoning summary'],
      }),
      makeMessage('assistant', 'Follow-up message', 'assistant-2'),
    ])

    await waitFor(() => expect(screen.getByText('Initial reasoning summary')).toBeInTheDocument())
    expect(screen.getByText('Follow-up message')).toBeInTheDocument()
  })

  it('does not snap to bottom after older messages are prepended while browsing history', async () => {
    const initialMessages = [
      makeMessage('assistant', 'Newest 1', 'msg-100'),
      makeMessage('assistant', 'Newest 2', 'msg-101'),
    ].map((message, index) => ({ ...message, seq: BigInt(100 + index) }))
    let setMessages!: (messages: AgentChatMessage[]) => void

    const view = render(() => {
      const [messages, updateMessages] = createSignal(initialMessages)
      setMessages = updateMessages
      return (
        <PreferencesProvider>
          <ChatView messages={messages()} streamingText="" pagination={{ hasOlderMessages: true }} />
        </PreferencesProvider>
      )
    })

    const chatContainer = screen.getByTestId('chat-container')
    const messageList = chatContainer.firstElementChild?.firstElementChild as HTMLDivElement

    let scrollTop = 0
    let scrollHeight = 2000
    const clientHeight = 500
    Object.defineProperty(messageList, 'scrollTop', {
      configurable: true,
      get: () => scrollTop,
      set: (value: number) => {
        scrollTop = value
      },
    })
    Object.defineProperty(messageList, 'scrollHeight', {
      configurable: true,
      get: () => scrollHeight,
    })
    Object.defineProperty(messageList, 'clientHeight', {
      configurable: true,
      get: () => clientHeight,
    })

    setMessages([...initialMessages])
    await flushAnimationFrame()

    scrollTop = 50
    fireEvent.scroll(messageList)

    scrollHeight = 2600
    setMessages([
      { ...makeMessage('assistant', 'Older 1', 'msg-050'), seq: 50n },
      { ...makeMessage('assistant', 'Older 2', 'msg-051'), seq: 51n },
      ...initialMessages,
    ])

    // The virtualized list anchors the viewport to the message at the top of
    // the viewport (by seq + offset) rather than shifting scrollTop by a raw
    // scrollHeight delta. In jsdom rows measure 0px, so the anchor resolves
    // back to the same offset — the key guarantee is that the view is NOT
    // snapped to the bottom. (The pixel-accurate offset math is unit-tested in
    // the useChatVirtualizer.*.test.ts / useChatScroll.*.test.ts suites.)
    await waitFor(() => expect(view.container).toHaveTextContent('Older 1'))
    expect(scrollTop).toBeLessThan(scrollHeight - clientHeight)

    scrollHeight = 2700
    setMessages([
      { ...makeMessage('assistant', 'Older 1', 'msg-050'), seq: 50n },
      { ...makeMessage('assistant', 'Older 2', 'msg-051'), seq: 51n },
      ...initialMessages,
      { ...makeMessage('assistant', 'Newest 3', 'msg-102'), seq: 102n },
    ])

    await waitFor(() => expect(view.container).toHaveTextContent('Newest 3'))
    expect(scrollTop).toBeLessThan(scrollHeight - clientHeight)
  })

  it('re-sticks to the bottom when the thinking-token count grows while pinned at the tail', async () => {
    // The thinking indicator is a tail sibling no ResizeObserver here watches; its
    // height growth (a climbing token count wrapping the verb row) does NOT move the
    // auto-scroll signature, so a dedicated effect must re-stick on the token count.
    // (agentWorking is left false so the visible indicator's rAF animation loop --
    // which the synchronous test rAF stub would recurse infinitely -- stays unmounted;
    // the effect is keyed on the thinkingTokens prop regardless, and the indicator's
    // jsdom-absent layout growth is simulated by growing scrollHeight below.)
    let setTokens!: (n: number) => void
    const messages = [{ ...makeMessage('assistant', 'Working on it', 'msg-1'), seq: 1n }]

    const view = render(() => {
      const [tokens, updateTokens] = createSignal(0)
      setTokens = updateTokens
      return (
        <PreferencesProvider>
          <ChatView messages={messages} streamingText="" agentLifecycle={{ thinkingTokens: tokens() }} />
        </PreferencesProvider>
      )
    })

    const chatContainer = screen.getByTestId('chat-container')
    const messageList = chatContainer.firstElementChild?.firstElementChild as HTMLDivElement

    let scrollTop = 0
    let scrollHeight = 1000
    const clientHeight = 500
    Object.defineProperty(messageList, 'scrollTop', {
      configurable: true,
      get: () => scrollTop,
      // Clamp like a real browser: the stick writes scrollTop = scrollHeight, which
      // the browser pins to the maximum (scrollHeight - clientHeight).
      set: (value: number) => {
        scrollTop = Math.max(0, Math.min(value, scrollHeight - clientHeight))
      },
    })
    Object.defineProperty(messageList, 'scrollHeight', { configurable: true, get: () => scrollHeight })
    Object.defineProperty(messageList, 'clientHeight', { configurable: true, get: () => clientHeight })

    // Pin to the bottom and let the sticky-bottom record seed.
    scrollTop = scrollHeight - clientHeight // 500
    fireEvent.scroll(messageList)
    await flushAnimationFrame()

    // The indicator grows: scrollHeight climbs while the message list, streamingText,
    // and agentWorking all stay put -- only the token count changed.
    scrollHeight = 1300
    setTokens(842)
    await flushAnimationFrame()
    await Promise.resolve()

    // The dedicated thinkingTokens re-stick effect snapped the view to the new bottom.
    await waitFor(() => expect(scrollTop).toBe(scrollHeight - clientHeight)) // 800
    view.unmount()
  })

  it('does not snap to bottom when a new message arrives before older-message anchoring finishes', async () => {
    const initialMessages = [
      makeMessage('assistant', 'Newest 1', 'msg-100'),
      makeMessage('assistant', 'Newest 2', 'msg-101'),
    ].map((message, index) => ({ ...message, seq: BigInt(100 + index) }))
    let setMessages!: (messages: AgentChatMessage[]) => void

    const view = render(() => {
      const [messages, updateMessages] = createSignal(initialMessages)
      setMessages = updateMessages
      return (
        <PreferencesProvider>
          <ChatView messages={messages()} streamingText="" pagination={{ hasOlderMessages: true }} />
        </PreferencesProvider>
      )
    })

    const chatContainer = screen.getByTestId('chat-container')
    const messageList = chatContainer.firstElementChild?.firstElementChild as HTMLDivElement

    let scrollTop = 0
    let scrollHeight = 2000
    const clientHeight = 500
    Object.defineProperty(messageList, 'scrollTop', {
      configurable: true,
      get: () => scrollTop,
      set: (value: number) => {
        scrollTop = value
      },
    })
    Object.defineProperty(messageList, 'scrollHeight', {
      configurable: true,
      get: () => scrollHeight,
    })
    Object.defineProperty(messageList, 'clientHeight', {
      configurable: true,
      get: () => clientHeight,
    })

    setMessages([...initialMessages])
    await flushAnimationFrame()

    scrollTop = 50
    fireEvent.scroll(messageList)

    scrollHeight = 2700
    setMessages([
      { ...makeMessage('assistant', 'Older 1', 'msg-050'), seq: 50n },
      { ...makeMessage('assistant', 'Older 2', 'msg-051'), seq: 51n },
      ...initialMessages,
      { ...makeMessage('assistant', 'Newest 3', 'msg-102'), seq: 102n },
    ])

    await waitFor(() => expect(view.container).toHaveTextContent('Newest 3'))
    // Anchored to the viewport-top message, not snapped to the bottom.
    await waitFor(() => expect(scrollTop).toBeLessThan(scrollHeight - clientHeight))
  })

  it('does not snap to bottom after older loading finishes while the user is still browsing history', async () => {
    const initialMessages = [
      makeMessage('assistant', 'Newest 1', 'msg-100'),
      makeMessage('assistant', 'Newest 2', 'msg-101'),
    ].map((message, index) => ({ ...message, seq: BigInt(100 + index) }))
    let setMessages!: (messages: AgentChatMessage[]) => void
    let setFetchingOlder!: (value: boolean) => void

    const view = render(() => {
      const [messages, updateMessages] = createSignal(initialMessages)
      const [fetchingOlder, updateFetchingOlder] = createSignal(false)
      setMessages = updateMessages
      setFetchingOlder = updateFetchingOlder
      return (
        <PreferencesProvider>
          <ChatView
            messages={messages()}
            streamingText=""
            pagination={{ hasOlderMessages: true, fetchingOlder: fetchingOlder() }}
          />
        </PreferencesProvider>
      )
    })

    const chatContainer = screen.getByTestId('chat-container')
    const messageList = chatContainer.firstElementChild?.firstElementChild as HTMLDivElement

    let scrollTop = 0
    let scrollHeight = 2000
    const clientHeight = 500
    Object.defineProperty(messageList, 'scrollTop', {
      configurable: true,
      get: () => scrollTop,
      set: (value: number) => {
        scrollTop = value
      },
    })
    Object.defineProperty(messageList, 'scrollHeight', {
      configurable: true,
      get: () => scrollHeight,
    })
    Object.defineProperty(messageList, 'clientHeight', {
      configurable: true,
      get: () => clientHeight,
    })

    setMessages([...initialMessages])
    await flushAnimationFrame()

    scrollTop = 50
    fireEvent.scroll(messageList)
    setFetchingOlder(true)

    scrollHeight = 2600
    setMessages([
      { ...makeMessage('assistant', 'Older 1', 'msg-050'), seq: 50n },
      { ...makeMessage('assistant', 'Older 2', 'msg-051'), seq: 51n },
      ...initialMessages,
    ])

    // Anchored to the viewport-top message, not snapped to the bottom.
    await waitFor(() => expect(view.container).toHaveTextContent('Older 1'))
    expect(scrollTop).toBeLessThan(scrollHeight - clientHeight)

    setFetchingOlder(false)
    scrollHeight = 2700
    setMessages([
      { ...makeMessage('assistant', 'Older 1', 'msg-050'), seq: 50n },
      { ...makeMessage('assistant', 'Older 2', 'msg-051'), seq: 51n },
      ...initialMessages,
      { ...makeMessage('assistant', 'Newest 3', 'msg-102'), seq: 102n },
    ])

    await waitFor(() => expect(view.container).toHaveTextContent('Newest 3'))
    expect(scrollTop).toBeLessThan(scrollHeight - clientHeight)
  })

  it('suppresses passive older-message loading when restored to top after trim clamping', async () => {
    const messages = [
      makeMessage('assistant', 'Retained 1', 'msg-1'),
      makeMessage('assistant', 'Retained 2', 'msg-2'),
    ].map((message, index) => ({ ...message, seq: BigInt(index + 1) }))
    const onLoadOlderMessages = vi.fn()
    const onClearSavedViewportScroll = vi.fn()

    render(() => (
      <PreferencesProvider>
        <ChatView
          messages={messages}
          streamingText=""
          pagination={{ hasOlderMessages: true, onLoadOlderMessages }}
          savedViewportScroll={{ atBottom: false, hasMoreNewer: false }}
          onClearSavedViewportScroll={onClearSavedViewportScroll}
        />
      </PreferencesProvider>
    ))

    const chatContainer = screen.getByTestId('chat-container')
    const messageList = chatContainer.firstElementChild?.firstElementChild as HTMLDivElement

    let scrollTop = 0
    const scrollHeight = 300
    let clientHeight = 0
    Object.defineProperty(messageList, 'scrollTop', {
      configurable: true,
      get: () => scrollTop,
      set: (value: number) => {
        scrollTop = value
      },
    })
    Object.defineProperty(messageList, 'scrollHeight', {
      configurable: true,
      get: () => scrollHeight,
    })
    Object.defineProperty(messageList, 'clientHeight', {
      configurable: true,
      get: () => clientHeight,
    })

    clientHeight = 100
    await triggerResizeObservers()

    expect(scrollTop).toBe(0)
    expect(onClearSavedViewportScroll).toHaveBeenCalledTimes(1)

    fireEvent.scroll(messageList)
    expect(onLoadOlderMessages).not.toHaveBeenCalled()
  })

  it('loads older messages on explicit upward wheel intent while at top', () => {
    const messages = [
      makeMessage('assistant', 'Retained 1', 'msg-1'),
      makeMessage('assistant', 'Retained 2', 'msg-2'),
    ].map((message, index) => ({ ...message, seq: BigInt(index + 1) }))
    const onLoadOlderMessages = vi.fn()

    render(() => (
      <PreferencesProvider>
        <ChatView
          messages={messages}
          streamingText=""
          pagination={{ hasOlderMessages: true, onLoadOlderMessages }}
        />
      </PreferencesProvider>
    ))

    const chatContainer = screen.getByTestId('chat-container')
    const messageList = chatContainer.firstElementChild?.firstElementChild as HTMLDivElement
    Object.defineProperty(messageList, 'scrollTop', { configurable: true, get: () => 0 })
    Object.defineProperty(messageList, 'clientHeight', { configurable: true, get: () => 200 })

    fireEvent.wheel(messageList, { deltaY: -20 })
    expect(onLoadOlderMessages).toHaveBeenCalledTimes(1)
  })

  it('loads older messages on explicit upward keyboard intent while at top', () => {
    const messages = [
      makeMessage('assistant', 'Retained 1', 'msg-1'),
      makeMessage('assistant', 'Retained 2', 'msg-2'),
    ].map((message, index) => ({ ...message, seq: BigInt(index + 1) }))
    const onLoadOlderMessages = vi.fn()

    render(() => (
      <PreferencesProvider>
        <ChatView
          messages={messages}
          streamingText=""
          pagination={{ hasOlderMessages: true, onLoadOlderMessages }}
        />
      </PreferencesProvider>
    ))

    const chatContainer = screen.getByTestId('chat-container')
    const messageList = chatContainer.firstElementChild?.firstElementChild as HTMLDivElement
    Object.defineProperty(messageList, 'scrollTop', { configurable: true, get: () => 0 })
    Object.defineProperty(messageList, 'clientHeight', { configurable: true, get: () => 200 })

    // ArrowUp is the explicit upward-intent key (Home jumps to the first
    // message and PageUp pages, so neither directly triggers older-loading).
    fireEvent.keyDown(messageList, { key: 'ArrowUp' })
    expect(onLoadOlderMessages).toHaveBeenCalledTimes(1)
  })

  it('scrolls the active chat by one page programmatically', () => {
    const messages = [
      makeMessage('assistant', 'Retained 1', 'msg-1'),
      makeMessage('assistant', 'Retained 2', 'msg-2'),
    ].map((message, index) => ({ ...message, seq: BigInt(index + 1) }))
    let pageScroll!: (direction: -1 | 1) => void

    render(() => (
      <PreferencesProvider>
        <ChatView
          messages={messages}
          streamingText=""
          onScrollApiReady={(api) => { pageScroll = api.pageScroll }}
        />
      </PreferencesProvider>
    ))

    const chatContainer = screen.getByTestId('chat-container')
    const messageList = chatContainer.firstElementChild?.firstElementChild as HTMLDivElement
    messageList.scrollBy = vi.fn()
    Object.defineProperty(messageList, 'clientHeight', { configurable: true, get: () => 240 })

    pageScroll(1)
    // A page jump keeps PAGE_SCROLL_OVERLAP_PX (48) of context: 240 - 48 = 192.
    expect(messageList.scrollBy).toHaveBeenCalledWith({ top: 192, behavior: 'auto' })
  })

  it('scrolls only the targeted chat when multiple chat views are mounted', () => {
    const messages = [
      makeMessage('assistant', 'Retained 1', 'msg-1'),
      makeMessage('assistant', 'Retained 2', 'msg-2'),
    ].map((message, index) => ({ ...message, seq: BigInt(index + 1) }))
    let hiddenPageScroll!: (direction: -1 | 1) => void
    let visiblePageScroll!: (direction: -1 | 1) => void

    render(() => (
      <PreferencesProvider>
        <div>
          <div style={{ display: 'none' }}>
            <ChatView
              messages={messages}
              streamingText=""
              onScrollApiReady={(api) => { hiddenPageScroll = api.pageScroll }}
            />
          </div>
          <div>
            <ChatView
              messages={messages}
              streamingText=""
              onScrollApiReady={(api) => { visiblePageScroll = api.pageScroll }}
            />
          </div>
        </div>
      </PreferencesProvider>
    ))

    const chatContainers = screen.getAllByTestId('chat-container')
    const hiddenList = chatContainers[0].firstElementChild?.firstElementChild as HTMLDivElement
    const visibleList = chatContainers[1].firstElementChild?.firstElementChild as HTMLDivElement

    Object.defineProperty(hiddenList, 'clientHeight', { configurable: true, get: () => 120 })
    Object.defineProperty(visibleList, 'clientHeight', { configurable: true, get: () => 240 })
    hiddenList.scrollBy = vi.fn()
    visibleList.scrollBy = vi.fn()

    visiblePageScroll(-1)

    expect(hiddenList.scrollBy).not.toHaveBeenCalled()
    // 240 viewport - 48 overlap = 192 (negative: paging up).
    expect(visibleList.scrollBy).toHaveBeenCalledWith({ top: -192, behavior: 'auto' })

    hiddenPageScroll(1)

    // 120 viewport - 48 overlap = 72.
    expect(hiddenList.scrollBy).toHaveBeenCalledWith({ top: 72, behavior: 'auto' })
  })

  it('loads older messages on touch and pointer overscroll intent while at top', () => {
    const messages = [
      makeMessage('assistant', 'Retained 1', 'msg-1'),
      makeMessage('assistant', 'Retained 2', 'msg-2'),
    ].map((message, index) => ({ ...message, seq: BigInt(index + 1) }))
    const onLoadOlderMessages = vi.fn()

    render(() => (
      <PreferencesProvider>
        <ChatView
          messages={messages}
          streamingText=""
          pagination={{ hasOlderMessages: true, onLoadOlderMessages }}
        />
      </PreferencesProvider>
    ))

    const chatContainer = screen.getByTestId('chat-container')
    const messageList = chatContainer.firstElementChild?.firstElementChild as HTMLDivElement
    Object.defineProperty(messageList, 'scrollTop', { configurable: true, get: () => 0 })
    Object.defineProperty(messageList, 'clientHeight', { configurable: true, get: () => 200 })

    fireEvent.touchStart(messageList, { touches: [{ clientY: 100 }] })
    fireEvent.touchMove(messageList, { touches: [{ clientY: 120 }] })
    fireEvent.pointerDown(messageList, { pointerType: 'touch', clientY: 100 })
    fireEvent.pointerMove(messageList, { pointerType: 'touch', clientY: 120 })

    expect(onLoadOlderMessages).toHaveBeenCalledTimes(2)
  })

  it('keeps both codex commandExecution start and completed messages in history', () => {
    const messages = [
      makeCodexCommandMessage({ id: 'cmd-start', seq: 1n, spanId: 'cmd-1', status: 'in_progress' }),
      makeCodexCommandMessage({ id: 'cmd-done', seq: 2n, spanId: 'cmd-1', status: 'completed', aggregatedOutput: 'done\n' }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(screen.getAllByTestId('message-bubble')).toHaveLength(2)
    expect(screen.getByText('done')).toBeInTheDocument()
    expect(view.container).toHaveTextContent('echo hi')
  })

  it('renders long completed codex command output with the shared collapsed result UI', () => {
    const messages = [
      makeCodexCommandMessage({
        id: 'cmd-done',
        seq: 1n,
        spanId: 'cmd-1',
        status: 'completed',
        aggregatedOutput: 'line1\nline2\nline3\nline4\n',
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(screen.getByTestId('message-toolbar')).toBeInTheDocument()
    expect(view.container).toHaveTextContent('line1')
    expect(view.container).toHaveTextContent('line3')
    expect(screen.queryByText('line4')).not.toBeInTheDocument()
  })

  it('strips tool use header DOM from completed codex command output', () => {
    const messages = [
      makeCodexCommandMessage({
        id: 'cmd-done',
        seq: 1n,
        spanId: 'cmd-1',
        status: 'failed',
        aggregatedOutput: [
          'TestingLibraryElementError',
          '  <div',
          '    class="toolStyles_toolUseHeader__174i4tc1"',
          '  >',
          '    <span>0 files</span>',
          '  </div>',
          'real failure output',
        ].join('\n'),
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(view.container).toHaveTextContent('real failure output')
    expect(screen.queryByText('0 files')).not.toBeInTheDocument()
  })

  it('renders process ID and exit code in completed codex command failures without output', () => {
    const messages = [
      makeCodexCommandMessage({
        id: 'cmd-failed',
        seq: 1n,
        spanId: 'cmd-1',
        status: 'failed',
        aggregatedOutput: '',
        processId: '63628',
        exitCode: 1,
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(view.container).toHaveTextContent('Error (exit 1)')
  })

  it('renders live fileChange stream inside the matching codex fileChange bubble', () => {
    const messages = [
      makeCodexFileChangeMessage({ id: 'fc-start', seq: 1n, spanId: 'fc-1', status: 'in_progress' }),
    ]
    const fileStream: CommandStreamSegment[] = [
      { kind: 'output', text: 'updating a.txt\n' },
    ]

    render(() => (
      <PreferencesProvider>
        <ChatView
          messages={messages}
          streamingText=""
          lookups={{ getCommandStreamBySpanId: () => fileStream }}
        />
      </PreferencesProvider>
    ))

    expect(screen.getByText('updating a.txt')).toBeInTheDocument()
  })

  it('keeps both codex fileChange start and completed messages in history', () => {
    const messages = [
      makeCodexFileChangeMessage({ id: 'fc-start', seq: 1n, spanId: 'fc-1', status: 'in_progress' }),
      makeCodexFileChangeMessage({ id: 'fc-done', seq: 2n, spanId: 'fc-1', status: 'completed' }),
    ]

    render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(screen.getAllByTestId('message-bubble')).toHaveLength(2)
    expect(screen.getByText('0 files')).toBeInTheDocument()
    expect(screen.getByText('old')).toBeInTheDocument()
  })

  it('does not render a diff in the codex fileChange start message', () => {
    const messages = [
      makeCodexFileChangeMessage({
        id: 'fc-start',
        seq: 1n,
        spanId: 'fc-1',
        status: 'in_progress',
        changes: [{ path: 'a.txt', kind: 'update', diff: '@@ -1 +1 @@\n-old\n+new' }],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(view.container).toHaveTextContent('a.txt')
    expect(view.container).not.toHaveTextContent('-old')
    expect(view.container).not.toHaveTextContent('+new')
  })

  it('renders a simple codex fileChange with Edit-style diff content', () => {
    const messages = [
      makeCodexFileChangeMessage({ id: 'fc-done', seq: 1n, spanId: 'fc-1', status: 'completed' }),
    ]

    render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(screen.queryByText('completed')).not.toBeInTheDocument()
    expect(screen.getByText('old')).toBeInTheDocument()
    expect(screen.getByText('new')).toBeInTheDocument()
    expect(screen.queryByText(A_TXT_RE)).not.toBeInTheDocument()
    expect(screen.queryByTestId('git-diff-stats')).not.toBeInTheDocument()
  })

  it('renders a simple codex add fileChange start message like Write tool_use', () => {
    const messages = [
      makeCodexFileChangeMessage({
        id: 'fc-start',
        seq: 1n,
        spanId: 'fc-1',
        status: 'in_progress',
        changes: [{ path: '/repo/src/new-file.ts', kind: { type: 'add' }, diff: 'export const hello = "world"\n' }],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(view.container).toHaveTextContent('new-file.ts')
    expect(view.container).not.toHaveTextContent('export const hello = "world"')
  })

  it('renders a simple codex add fileChange completion like Write tool_use_result', () => {
    const messages = [
      makeCodexFileChangeMessage({
        id: 'fc-done',
        seq: 1n,
        spanId: 'fc-1',
        status: 'completed',
        changes: [{ path: '/repo/src/new-file.ts', kind: { type: 'add' }, diff: 'export const hello = "world"\n' }],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(view.container).toHaveTextContent('export const hello = "world"')
    expect(screen.getByTestId('message-toolbar')).toBeInTheDocument()
  })

  it('renders a simple codex delete fileChange start message as file deletion', () => {
    const messages = [
      makeCodexFileChangeMessage({
        id: 'fc-start',
        seq: 1n,
        spanId: 'fc-1',
        status: 'in_progress',
        changes: [{ path: '/repo/src/old-file.ts', kind: { type: 'delete' }, diff: 'export const old = true\n' }],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(view.container).toHaveTextContent('old-file.ts')
    expect(view.container).not.toHaveTextContent('export const old = true')
  })

  it('renders a simple codex delete fileChange completion as file deletion', () => {
    const messages = [
      makeCodexFileChangeMessage({
        id: 'fc-done',
        seq: 1n,
        spanId: 'fc-1',
        status: 'completed',
        changes: [{ path: '/repo/src/old-file.ts', kind: { type: 'delete' }, diff: 'export const old = true\n' }],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(view.container).toHaveTextContent('Deleted /repo/src/old-file.ts')
    expect(view.container).not.toHaveTextContent('1 file changed')
    expect(screen.getByTestId('message-toolbar')).toBeInTheDocument()
  })

  it('renders multi-file codex fileChange entries with per-file labels including adds', () => {
    const messages = [
      makeCodexFileChangeMessage({
        id: 'fc-done',
        seq: 1n,
        spanId: 'fc-1',
        status: 'completed',
        changes: [
          { path: '/repo/src/a.ts', kind: { type: 'update' }, diff: '@@ -1 +1 @@\n-oldValue\n+newValue\n' },
          { path: '/repo/src/new-file.tsx', kind: { type: 'add' }, diff: 'export const hello = "world"\n' },
        ],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(view.container).toHaveTextContent('a.ts +1 -1')
    expect(view.container).toHaveTextContent('new-file.tsx +1')
    expect(view.container).toHaveTextContent('+1')
    expect(view.container).toHaveTextContent('oldValue')
    expect(view.container).toHaveTextContent('newValue')
    expect(view.container).toHaveTextContent('export const hello = "world"')
  })

  it('shows shared toolbar actions for completed codex fileChange messages', () => {
    const messages = [
      makeCodexFileChangeMessage({ id: 'fc-done', seq: 1n, spanId: 'fc-1', status: 'completed' }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    const toolbar = screen.getByTestId('message-toolbar')
    expect(toolbar).toBeInTheDocument()
    expect(screen.getByTestId('message-copy-json')).toBeInTheDocument()
    expect(view.container.querySelectorAll('[data-testid="message-toolbar"] button').length).toBeGreaterThanOrEqual(2)
  })

  it('falls back to codex fileChange summary for mixed changes', () => {
    const messages = [
      makeCodexFileChangeMessage({
        id: 'fc-done',
        seq: 1n,
        spanId: 'fc-1',
        status: 'completed',
        changes: [
          { path: 'a.txt', kind: 'create', diff: '' },
          { path: 'b.txt', kind: 'delete', diff: '' },
        ],
      }),
    ]

    render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(screen.getByText('2 files changed')).toBeInTheDocument()
    expect(screen.queryByText(A_TXT_RE)).not.toBeInTheDocument()
    expect(screen.queryByText(B_TXT_RE)).not.toBeInTheDocument()
  })

  it('renders live reasoning stream inside the matching codex reasoning bubble', () => {
    const messages = [
      makeCodexReasoningMessage({ id: 'reason-start', seq: 1n, spanId: 'reason-1' }),
    ]
    const reasoningStream: CommandStreamSegment[] = [
      { kind: 'reasoning_summary', text: 'first summary' },
      { kind: 'reasoning_summary_break', text: '' },
      { kind: 'reasoning_summary', text: 'second summary' },
    ]

    render(() => (
      <PreferencesProvider>
        <ChatView
          messages={messages}
          streamingText=""
          lookups={{
            getCommandStreamBySpanId: () => reasoningStream,
            hasRenderableCommandStreamBySpanId: () => reasoningStream.length > 0,
          }}
        />
      </PreferencesProvider>
    ))

    expect(screen.getAllByTestId('message-bubble')).toHaveLength(1)
    expect(screen.getByText('Thinking')).toBeInTheDocument()
  })

  it('starts codex reasoning collapsed when expandAgentThoughts is disabled', () => {
    localStorageSet(KEY_BROWSER_PREFS, { expandAgentThoughts: false })

    const messages = [
      makeCodexReasoningMessage({ id: 'reason-done', seq: 1n, spanId: 'reason-1', summary: ['done'] }),
    ]

    render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(screen.getByText('Thinking')).toBeInTheDocument()
    expect(screen.queryByText('done')).not.toBeInTheDocument()

    fireEvent.click(screen.getByText('Thinking'))
    expect(screen.getByText('done')).toBeInTheDocument()
  })

  it('keeps completed codex reasoning visible while empty start reasoning remains hidden', () => {
    const messages = [
      makeCodexReasoningMessage({ id: 'reason-start', seq: 1n, spanId: 'reason-1' }),
      makeCodexReasoningMessage({ id: 'reason-done', seq: 2n, spanId: 'reason-1', summary: ['done'] }),
    ]

    render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(screen.getAllByTestId('message-bubble')).toHaveLength(1)
    expect(screen.getByText('Thinking')).toBeInTheDocument()
  })

  it('renders turn/plan/updated with the TodoWrite-style todo list UI', () => {
    const messages = [
      makeCodexTurnPlanMessage({
        id: 'plan-1',
        seq: 1n,
        plan: [
          { step: 'Inspect message filtering', status: 'inProgress' },
          { step: 'Implement renderer', status: 'pending' },
          { step: 'Run tests', status: 'completed' },
        ],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(view.container).toHaveTextContent('3 tasks')
    expect(view.container).toHaveTextContent('Inspect message filtering')
    expect(view.container).toHaveTextContent('Implement renderer')
    expect(view.container).toHaveTextContent('Run tests')
  })

  it('renders turn/plan/updated explanation in the title when present', () => {
    const messages = [
      makeCodexTurnPlanMessage({
        id: 'plan-1',
        seq: 1n,
        explanation: 'Need to keep start and complete messages visible',
        plan: [
          { step: 'Inspect message filtering', status: 'inProgress' },
          { step: 'Implement renderer', status: 'pending' },
        ],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(view.container).toHaveTextContent('2 tasks - Need to keep start and complete messages visible')
  })

  it('renders codex webSearch start and completed search messages separately', () => {
    const messages = [
      makeCodexWebSearchMessage({ id: 'ws-start', seq: 1n, spanId: 'ws-1' }),
      makeCodexWebSearchMessage({
        id: 'ws-done',
        seq: 2n,
        spanId: 'ws-1',
        query: 'codex app server',
        action: {
          type: 'search',
          query: 'codex app server',
          queries: [
            'codex app server',
            'site:github.com openai codex "turn/plan/updated"',
            'site:github.com "turn/plan/updated" codex app server',
          ],
        },
        completed: true,
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(screen.getAllByTestId('message-bubble')).toHaveLength(1)
    expect(view.container).not.toHaveTextContent('Searching the web')
    expect(view.container).toHaveTextContent('codex app server')
    expect(view.container).not.toHaveTextContent('site:github.com openai codex')
    expect(view.container).not.toHaveTextContent('site:github.com "turn/plan/updated" codex app server')

    fireEvent.click(screen.getByRole('button', { name: 'Expand' }))

    expect(view.container).toHaveTextContent('site:github.com openai codex "turn/plan/updated"')
    expect(view.container).toHaveTextContent('site:github.com "turn/plan/updated" codex app server')
  })

  it('renders codex webSearch openPage messages like a fetch/opened-page result', () => {
    const messages = [
      makeCodexWebSearchMessage({
        id: 'ws-open',
        seq: 1n,
        spanId: 'ws-1',
        query: 'https://github.com/openai/codex/blob/main/codex-rs/app-server/README.md',
        action: {
          type: 'openPage',
          url: 'https://github.com/openai/codex/blob/main/codex-rs/app-server/README.md',
        },
        completed: true,
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(view.container).toHaveTextContent('https://github.com/openai/codex/blob/main/codex-rs/app-server/README.md')
    expect(screen.getByTestId('message-toolbar')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Expand' })).not.toBeInTheDocument()
  })

  it('renders codex webSearch findInPage messages with pattern and URL', () => {
    const messages = [
      makeCodexWebSearchMessage({
        id: 'ws-find',
        seq: 1n,
        spanId: 'ws-1',
        query: 'README',
        action: {
          type: 'findInPage',
          url: 'https://example.com/readme',
          pattern: 'turn/plan/updated',
        },
        completed: true,
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(view.container).toHaveTextContent('turn/plan/updated')
    expect(view.container).toHaveTextContent('https://example.com/readme')
    expect(screen.getByTestId('message-toolbar')).toBeInTheDocument()
  })

  it('hides empty completed codex webSearch messages with no query or detail', () => {
    const messages = [
      makeCodexWebSearchMessage({
        id: 'ws-empty',
        seq: 1n,
        spanId: 'ws-1',
        query: '',
        action: { type: 'other' },
        completed: true,
      }),
    ]

    render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(screen.queryByText('Searched')).not.toBeInTheDocument()
    expect(screen.queryByText('Searching the web')).not.toBeInTheDocument()
    expect(screen.queryByTestId('message-bubble')).not.toBeInTheDocument()
  })
})

// The premeasure/skeleton/fling/rail suites drive a fully stubbed virtualizer,
// scroll, viewport observer, hidden-premeasure and message bubble (see the
// partial mocks above) for deterministic geometry, so they flip mockDeps on.
describe('chat view virtualized with stubbed deps', () => {
  beforeEach(() => {
    mockState.mockDeps = true
  })

  afterEach(() => {
    vi.useRealTimers()
    viewportSizeObserverState.width = 640
    viewportSizeObserverState.height = 733
    viewportSizeObserverState.onWidth = undefined
    viewportSizeObserverState.onHeight = undefined
    prefsState.setDiffView?.('unified') // the pref signals are module-scoped; reset between tests
    prefsState.setExpandThoughts?.(true)
  })

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
})
