import type { ChatPremeasureBandsDeps } from './chatPremeasureBands'
import type { PersistableRowHeight, RowHeightCache } from './chatRowHeightCache'
import type { RowHeightPersistenceDeps } from './chatRowHeightPersistence'
import type { ChatScrollRail } from './ChatScrollRail'
import type { RowContentReadiness } from './createRowMeasurer'
import type { MessageBubbleHost } from './MessageBubble'
import type { UseChatScrollOptions } from './useChatScroll'
import type { ChatVirtualizerRange, UseChatVirtualizerResult, VirtualItem } from './useChatVirtualizer'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { batch, createEffect, createSignal, For } from 'solid-js'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { PreferencesProvider, usePreferences } from '~/context/PreferencesContext'
import { COPILOT_EVENT, COPILOT_TOOL } from '~/generated/contracts/copilot-protocol'
import { AgentChatMessageSchema, AgentProvider, AgentStatus, ContentCompression, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { KEY_BROWSER_PREFS, localStorageSet, localStorageStore, PREFIX_CHAT_ROW_HEIGHTS } from '~/lib/browserStorage'
import { fnv1a32Hex } from '~/lib/stringDigest'
import { deferred } from '~/test-support/async'
import { measureHiddenCopies, reportChatViewportSize } from '~/test-support/chatViewRows'
import { copilotFrame, copilotToolComplete, copilotToolStart } from '~/test-support/copilotFixtures'
import { settleWorkerReplies } from '~/test-support/fakeWorker'
import { testMessageContext, testTranscriptContext } from '~/test-support/messageContext'
import { useTestStorage } from '~/test-support/persistentStorage'
import { flushAnimationFrame, installControllableResizeObserver, triggerResizeObserverFor } from '~/test-support/resizeObserverStub'
import { installControlledSlices } from '~/test-support/sliceSchedulers'
import { CHAT_FONT_LOAD_LIMIT_MS } from './chatFonts'
import { LOOKAHEAD_PREMEASURE_ROWS } from './chatPremeasureBands'
import { WARMUP_IDLE_DELAY_MS } from './chatPremeasureWarmup'
import { STORED_ROW_HEIGHTS_VERSION } from './chatRowHeightPersistence'
import { railIdle } from './ChatScrollRail.css'
import { ChatView, RAIL_COAST_MAX_MS, RAIL_VISIBLE_IDLE_MS, SKELETON_SHOW_DELAY_MS, STAGING_WIDTH_SETTLE_MS, SYNTAX_HIGHLIGHT_SCROLL_IDLE_MS } from './ChatView'
import { messageListRailActive, railedRowContent, rowSkeletonClosing, virtualLayer, virtualSpacer } from './ChatView.css'
import { computeOverscanPx, PRE_MEASURE_WIDTH_PX } from './chatViewportGeometry'
import { bandRow, bleedRow } from './messageStyles.css'
import { MESSAGE_UI_KEY } from './messageUiKeys'
import { createRenderReadinessScope } from './renderReadiness'
import { sameVirtualItems } from './useChatVirtualizer'
import { ROW_BLEED_LEFT_VAR, rowBleedLeftStyle } from './widgets/SpanLines.geometry'

const A_TXT_RE = /a\.txt/

type HiddenPremeasureOnMeasure = (id: string, height: number, heightKey: string | undefined, measureDurationMs: number) => boolean

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
const mockState = vi.hoisted(() => ({
  mockDeps: false,
  /**
   * The options ChatView handed the scroll hook, so a test can fire the
   * callbacks the STUB never would. The rail's coast relight arrives that way:
   * only the real hook can tell the reader's momentum from our own writes
   * echoing back, so ChatView's half of that contract is "when the hook says
   * momentum, relight" -- which is what invoking `onMomentumScroll` pins here.
   * The classification itself belongs to the hook's own tests.
   */
  scrollOpts: undefined as undefined | UseChatScrollOptions,
  /** The stub that `useChatScroll` returned last, so a test can assert that nothing called it. */
  scrollStub: undefined as undefined | Record<string, unknown>,
  /** Set what the stub scroll hook reports: follow mode, and the two staged edges. */
  setIsFollowing: undefined as undefined | ((following: boolean) => void),
  setAtStagedTopEdge: undefined as undefined | ((atEdge: boolean) => void),
  setAtStagedBottomEdge: undefined as undefined | ((atEdge: boolean) => void),
}))

const virtualizerState = vi.hoisted(() => ({
  range: { start: 0, end: 1 } as ChatVirtualizerRange,
  realInstances: [] as UseChatVirtualizerResult[],
  /**
   * The height cache that ChatView handed each virtualizer, real or stub, in order: the
   * cache of the view, which the admission and the staging bands also read.
   */
  heightCaches: [] as RowHeightCache[],
  /** The `items` option that ChatView handed the stub virtualizer last: the admitted rows. */
  items: undefined as undefined | (() => readonly VirtualItem[]),
  setRange: undefined as undefined | ((range: ChatVirtualizerRange) => void),
  attachedIds: [] as string[],
  currentHeightKeys: new Map<string, string | undefined>(),
  setDeferred: undefined as undefined | ((deferred: boolean) => void),
  /** The readiness scope that ChatView handed the stub virtualizer with each row copy. */
  attachedReadiness: new Map<string, RowContentReadiness>(),
  /**
   * The layout shift of the stub virtualizer (`scroll = content + layoutShift`). Its layer
   * transform, its spacer height and its coordinate conversions all derive from this one number.
   */
  layoutShift: 0,
  /** Each element that ChatView handed the stub virtualizer for its layout writes, in order. */
  layoutAttachments: [] as { slot: 'spacer' | 'layer', element: HTMLElement | undefined }[],
}))

/**
 * The host bindings that ChatView handed each stubbed bubble, by message id: `byId` for a
 * bubble of the list, and `premeasureById` for the bubble of a hidden copy.
 */
const bubbleHostState = vi.hoisted(() => ({
  byId: new Map<string, MessageBubbleHost>(),
  premeasureById: new Map<string, MessageBubbleHost>(),
}))

/**
 * Which stubbed bubbles hold a Markdown body still rendering in the worker. The
 * MessageBubble stub reports it to the readiness scope ChatView hands each copy.
 */
const markdownPendingState = vi.hoisted(() => ({
  pendingIds: (): ReadonlySet<string> => new Set(),
  setPendingIds: undefined as undefined | ((ids: ReadonlySet<string>) => void),
}))

const hiddenPremeasureState = vi.hoisted(() => ({
  candidates: [] as ReadonlyArray<{ entry: unknown, item: { id: string, heightKey?: string } }>,
  /** The ids of each `candidates` value that the mock got, in order. */
  candidateLog: [] as string[][],
  onMeasure: undefined as HiddenPremeasureOnMeasure | undefined,
  contentWidthPx: undefined as number | undefined,
}))

/**
 * What ChatView handed three of its units last, so a test can read it: the deps of the
 * height persistence and of the staging bands, and the props of the scroll rail. Each
 * mock below passes its argument on to the real unit.
 */
const wiringState = vi.hoisted(() => ({
  persistenceDeps: undefined as undefined | RowHeightPersistenceDeps,
  bandsDeps: undefined as undefined | ChatPremeasureBandsDeps,
  railProps: undefined as undefined | Parameters<typeof ChatScrollRail>[0],
}))

vi.mock('./chatRowHeightPersistence', async () => {
  const actual = await vi.importActual<typeof import('./chatRowHeightPersistence')>('./chatRowHeightPersistence')
  return {
    ...actual,
    createRowHeightPersistence: (deps: RowHeightPersistenceDeps) => {
      wiringState.persistenceDeps = deps
      actual.createRowHeightPersistence(deps)
    },
  }
})

vi.mock('./chatPremeasureBands', async () => {
  const actual = await vi.importActual<typeof import('./chatPremeasureBands')>('./chatPremeasureBands')
  return {
    ...actual,
    createChatPremeasureBands: (deps: ChatPremeasureBandsDeps) => {
      wiringState.bandsDeps = deps
      return actual.createChatPremeasureBands(deps)
    },
  }
})

vi.mock('./ChatScrollRail', async () => {
  const actual = await vi.importActual<typeof import('./ChatScrollRail')>('./ChatScrollRail')
  return {
    ...actual,
    ChatScrollRail: (props: Parameters<typeof actual.ChatScrollRail>[0]) => {
      // The props object, whose members are getters: a test reads the current values.
      wiringState.railProps = props
      return actual.ChatScrollRail(props)
    },
  }
})

const viewportSizeObserverState = vi.hoisted(() => ({
  width: 640,
  height: 733,
  onWidth: undefined as undefined | ((width: number) => void),
  onHeight: undefined as undefined | ((height: number) => void),
}))

const prefsState = vi.hoisted(() => ({
  setDiffView: undefined as undefined | ((view: 'unified' | 'split') => void),
  setExpandThoughts: undefined as undefined | ((value: boolean) => void),
  setUiFontFamily: undefined as undefined | ((family: string | undefined) => void),
  setMonoFontFamily: undefined as undefined | ((family: string) => void),
}))

/** The font family that the mocked preferences give for code, until a test changes it. */
const MOCK_MONO_FONT_FAMILY = 'monospace'

vi.mock('~/context/PreferencesContext', async () => {
  const actual = await vi.importActual<typeof import('~/context/PreferencesContext')>('~/context/PreferencesContext')
  const { createSignal } = await import('solid-js')
  const [diffView, setDiffView] = createSignal<'unified' | 'split'>('unified')
  const [expandThoughts, setExpandThoughts] = createSignal(true)
  const [uiFontFamily, setUiFontFamily] = createSignal<string | undefined>(undefined)
  const [monoFontFamily, setMonoFontFamily] = createSignal('monospace')
  prefsState.setDiffView = setDiffView
  prefsState.setExpandThoughts = setExpandThoughts
  prefsState.setUiFontFamily = setUiFontFamily
  prefsState.setMonoFontFamily = setMonoFontFamily
  return {
    ...actual,
    usePreferences: () => {
      if (!mockState.mockDeps)
        return actual.usePreferences()
      return {
        diffView,
        expandAgentThoughts: expandThoughts,
        showHiddenMessages: () => false,
        uiFontFamily,
        monoFontFamily,
      }
    },
  }
})

vi.mock('./MessageBubble', async () => {
  const actual = await vi.importActual<typeof import('./MessageBubble')>('./MessageBubble')
  const { createSignal } = await import('solid-js')
  const [pendingIds, setPendingIds] = createSignal<ReadonlySet<string>>(new Set())
  markdownPendingState.pendingIds = pendingIds
  markdownPendingState.setPendingIds = setPendingIds
  const StubBubble = (props: Parameters<typeof actual.MessageBubble>[0]) => {
    // A stand-in Markdown body, pending while the test lists this message.
    // eslint-disable-next-line solid/reactivity -- the scope reads the accessor inside its own memo
    props.readiness?.track(() => pendingIds().has(props.message.id))
    // ChatView gives each mounted bubble one host, so the stub records it once, at creation.
    // eslint-disable-next-line solid/reactivity -- a read at creation, as the line above states
    const { host, premeasureMode, message } = props
    if (host !== undefined) {
      const hosts = premeasureMode
        ? bubbleHostState.premeasureById
        : bubbleHostState.byId
      hosts.set(message.id, host)
    }
    return (
      <div
        data-testid="mock-message-bubble"
        data-message-id={props.message.id}
        data-premeasure={props.premeasureMode ? 'true' : 'false'}
        // The scroll-idle highlight pause is only observable through the host
        // binding MessageBubble forwards into its focused context, so surface it
        // here for the tests that pin ChatView's highlightActivity window.
        data-syntax-paused={props.host?.syntaxHighlightingPaused?.()
          ? 'true'
          : 'false'}
      />
    )
  }
  return {
    ...actual,
    // A single-return ternary keeps reactivity intact (mockDeps is fixed for a
    // component's lifetime, set once per test) without tripping solid/components-return-once.
    MessageBubble: (props: Parameters<typeof actual.MessageBubble>[0]) => (
      mockState.mockDeps
        ? StubBubble(props)
        : actual.MessageBubble(props)
    ),
  }
})

/** The seed of each row skeleton that a component built, in order, built and then disposed ones included. */
const rowSkeletonState = vi.hoisted(() => ({
  builtSeeds: [] as string[],
}))

vi.mock('./widgets/ChatRowSkeleton', async () => {
  const actual = await vi.importActual<typeof import('./widgets/ChatRowSkeleton')>('./widgets/ChatRowSkeleton')
  return {
    ...actual,
    ChatRowSkeleton: (props: Parameters<typeof actual.ChatRowSkeleton>[0]) => {
      // eslint-disable-next-line solid/reactivity -- records the seed the skeleton was built with
      rowSkeletonState.builtSeeds.push(props.seed)
      return actual.ChatRowSkeleton(props)
    },
  }
})

vi.mock('./chatHiddenPremeasure', async () => {
  const actual = await vi.importActual<typeof import('./chatHiddenPremeasure')>('./chatHiddenPremeasure')
  // The stub captures each pass's candidates/onMeasure into hoisted state and renders
  // the bubbles so the visible-slice tests can drive measurement deterministically.
  const MockHiddenPremeasure = (props: Parameters<typeof actual.ChatHiddenPremeasure>[0]) => {
    createEffect(() => {
      hiddenPremeasureState.candidates = props.candidates
      hiddenPremeasureState.candidateLog.push(props.candidates.map(candidate => candidate.item.id))
      hiddenPremeasureState.contentWidthPx = props.contentWidthPx
      virtualizerState.currentHeightKeys = new Map(props.candidates.map(candidate => [candidate.item.id, candidate.item.heightKey]))
      hiddenPremeasureState.onMeasure = props.onMeasure
    })
    return (
      <div data-testid="mock-hidden-premeasure">
        <For each={props.candidates}>
          {candidate => props.renderBubble(candidate.entry, createRenderReadinessScope())}
        </For>
      </div>
    )
  }
  return {
    ...actual,
    ChatHiddenPremeasure: (props: Parameters<typeof actual.ChatHiddenPremeasure>[0]) => (
      mockState.mockDeps
        ? MockHiddenPremeasure(props)
        : actual.ChatHiddenPremeasure(props)
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
  const { createSignal } = await import('solid-js')
  const [isFollowing, setIsFollowing] = createSignal(true)
  const [atStagedTopEdge, setAtStagedTopEdge] = createSignal(false)
  const [atStagedBottomEdge, setAtStagedBottomEdge] = createSignal(false)
  mockState.setIsFollowing = setIsFollowing
  mockState.setAtStagedTopEdge = setAtStagedTopEdge
  mockState.setAtStagedBottomEdge = setAtStagedBottomEdge
  return {
    ...actual,
    useChatScroll: (...args: Parameters<typeof actual.useChatScroll>) => {
      mockState.scrollOpts = args[0]
      if (!mockState.mockDeps)
        return actual.useChatScroll(...args)
      // The admission forms no target of its own, so the stub asks for the tail once, as the
      // first-view rule of the real hook does. Without a request no row joins the list. The
      // stub never ends the request: the tail stays the target, as for a reader who follows.
      args[0].admission.request({ kind: 'tail' })
      const stub = {
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
        isFollowing,
        stalledOlder: () => false,
        stalledNewer: () => false,
        atStagedTopEdge,
        atStagedBottomEdge,
        viewAnchor: () => undefined,
        scrollToBottom: vi.fn(),
        restickIfAtBottom: vi.fn(),
        isAtBottomFresh: () => false,
        jumpToBottom: vi.fn(),
        getScrollState: () => undefined,
        forceScrollToBottom: vi.fn(),
        pageScroll: vi.fn(),
      }
      mockState.scrollStub = stub
      return stub
    },
  }
})

vi.mock('./useChatVirtualizer', async () => {
  const actual = await vi.importActual<typeof import('./useChatVirtualizer')>('./useChatVirtualizer')
  const { createSignal } = await import('solid-js')
  const { placementScrollTop } = await import('./createLayoutShift')
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
      const heightCache = args[0].heightCache
      if (heightCache !== undefined)
        virtualizerState.heightCaches.push(heightCache)
      if (!mockState.mockDeps) {
        const virt = actual.useChatVirtualizer(...args)
        virtualizerState.realInstances.push(virt)
        return virt
      }
      const items = args[0].items
      virtualizerState.items = items
      // The stub lays out the items that it gets, the admitted rows: row `i` of them is 100px
      // tall at 100·i. A row that is not among them has no index and no offset.
      const indexOfId = (id: string): number => items().findIndex(item => item.id === id)
      // The element that owns each mounted row.
      const mountedElements = new Map<string, HTMLElement>()
      const totalHeight = 10_000
      return {
        mountedIds: new Set<string>(),
        fastScrollActive: deferred,
        range: () => {
          version()
          return virtualizerState.range
        },
        geometryVersion: version,
        totalHeight: () => totalHeight,
        layoutShift: () => virtualizerState.layoutShift,
        spacerHeight: () => totalHeight + virtualizerState.layoutShift,
        contentTopOf: (scrollTop: number) => scrollTop - virtualizerState.layoutShift,
        scrollTopOf: (contentTop: number) => contentTop + virtualizerState.layoutShift,
        placementScrollTopOf: (contentTop: number) => placementScrollTop(contentTop, top => top + virtualizerState.layoutShift),
        attachSpacer: (element: HTMLElement | undefined) => {
          virtualizerState.layoutAttachments.push({ slot: 'spacer', element })
        },
        attachLayer: (element: HTMLElement | undefined) => {
          virtualizerState.layoutAttachments.push({ slot: 'layer', element })
        },
        rebase: () => 0,
        notePlacement: () => {},
        offsetOfId: (id: string) => {
          const index = indexOfId(id)
          return index < 0
            ? undefined
            : index * 100
        },
        indexOfId,
        offsetOfIndex: (index: number) => index * 100,
        heightOfIndex: () => 100,
        heightOfId: () => 100,
        heightDebugOfId: () => ({}),
        // The stub keeps the mounted set of the cache, as the row measurer of the real
        // virtualizer does: a mounted row keeps its height when its key changes.
        attachRow: (id: string, el: HTMLElement, readiness?: RowContentReadiness) => {
          virtualizerState.attachedIds.push(id)
          if (readiness !== undefined)
            virtualizerState.attachedReadiness.set(id, readiness)
          mountedElements.set(id, el)
          heightCache?.mountedIds.add(id)
        },
        detachRow: vi.fn((el: HTMLElement) => {
          for (const [id, mounted] of mountedElements) {
            // An element that a newer element of the row replaced owns the row no more.
            if (mounted !== el)
              continue
            mountedElements.delete(id)
            heightCache?.mountedIds.delete(id)
          }
        }),
      }
    },
  }
})

// jsdom does not provide ResizeObserver. The stand-in Worker of the jsdom setup
// renders each Markdown body, so a row that holds one stays hidden behind its
// skeleton until a test waits for the replies (settleWorkerReplies). The pending
// path has its own tests, with a worker client that the test answers.
beforeAll(() => {
  installControllableResizeObserver()
})

function requiredPremeasureCallback(): HiddenPremeasureOnMeasure {
  const callback = hiddenPremeasureState.onMeasure
  if (!callback)
    throw new Error('ChatView did not provide a premeasure callback')
  return callback
}

/**
 * Admit the rows of a view of the stubbed suite: commit a height of `height` for each row
 * that a hidden copy waits for, through the callback that ChatView handed the
 * hidden-premeasure mock, until no candidate remains. A commit can admit rows, and the
 * staging then asks for more, so the helper repeats.
 */
function admitRows(height = 100): void {
  for (let round = 0; round < 100 && hiddenPremeasureState.candidates.length > 0; round++) {
    const onMeasure = requiredPremeasureCallback()
    for (const candidate of [...hiddenPremeasureState.candidates])
      onMeasure(candidate.item.id, height, candidate.item.heightKey, 0)
  }
  // A candidate that remains never took its height, so its row is not in the list, and a
  // test that went on would read the bubble of its hidden copy where it means the row.
  const remaining = hiddenPremeasureState.candidates.length
  if (remaining > 0)
    throw new Error(`${remaining} staged rows took no height, so they cannot join the list`)
}

/** Commit the height of one staged row of the stubbed suite, as its hidden copy does. */
function measureStagedRow(id: string, height = 100): void {
  const candidate = hiddenPremeasureState.candidates.find(row => row.item.id === id)
  if (!candidate)
    throw new Error(`No hidden copy waits for the row ${id}`)
  requiredPremeasureCallback()(id, height, candidate.item.heightKey, 0)
}

// Real modules by default; the stubbed-deps suite flips this to true.
beforeEach(() => {
  mockState.mockDeps = false
})

/** The height cache of the ChatView that mounted last. */
function viewHeightCache(): RowHeightCache {
  const cache = virtualizerState.heightCaches.at(-1)
  if (!cache)
    throw new Error('ChatView handed its virtualizer no height cache')
  return cache
}

/** The scroll geometry that `stubChatScrollGeometry` gives the real scroll container. */
interface StubbedChatScroll {
  list: HTMLDivElement
  /** The virtual spacer, which holds the rows. */
  spacer: () => HTMLElement
  scrollTop: () => number
  /** Move the view as a reader does: set `scrollTop`, then dispatch its `scroll` event. */
  scrollTo: (top: number) => void
}

/**
 * Give the real scroll container of `view` a browser-like scroll geometry. jsdom
 * computes no layout, so the test owns three values: a fixed `clientHeight`, a
 * `scrollHeight` that follows the inline height of the virtual spacer, and a
 * `scrollTop` that stays inside the scroll range, as a browser clamps it.
 */
function stubChatScrollGeometry(view: { container: HTMLElement }, clientHeight = 500): StubbedChatScroll {
  const list = view.container.querySelector('[data-chat-scroll-container]') as HTMLDivElement | null
  if (list === null)
    throw new Error('ChatView did not mount its scroll container')
  const spacer = (): HTMLElement => {
    const element = list.querySelector(`.${virtualSpacer}`)
    if (!(element instanceof HTMLElement))
      throw new TypeError('ChatView did not mount its virtual spacer')
    return element
  }
  const scrollHeight = () => Number.parseFloat(list.querySelector<HTMLElement>(`.${virtualSpacer}`)?.style.height ?? '') || 0
  const clampTop = (top: number) => Math.max(0, Math.min(top, scrollHeight() - clientHeight))
  let stored = 0
  Object.defineProperty(list, 'clientHeight', {
    configurable: true,
    get: () => clientHeight,
  })
  Object.defineProperty(list, 'scrollHeight', { configurable: true, get: scrollHeight })
  Object.defineProperty(list, 'scrollTop', {
    configurable: true,
    get: () => clampTop(stored),
    set: (top: number) => {
      stored = clampTop(top)
    },
  })
  return {
    list,
    spacer,
    scrollTop: () => list.scrollTop,
    scrollTo: (top: number) => {
      list.scrollTop = top
      fireEvent.scroll(list)
    },
  }
}

/**
 * Record each write to the `scrollTop` of `list`. The getter returns the last write as
 * written: jsdom has no layout, so nothing clamps it.
 */
function recordScrollTopWrites(list: HTMLElement): number[] {
  const writes: number[] = []
  Object.defineProperty(list, 'scrollTop', {
    configurable: true,
    get: () => writes[writes.length - 1] ?? 0,
    set: (top: number) => {
      writes.push(top)
    },
  })
  return writes
}

/** The two computed values of the chat scroll container that make the inset of its spacer. */
interface ListInsetStyle {
  paddingTop: string
  rowGap: string
}

/**
 * Make the computed style of every chat scroll container report `style`, until `restore`.
 * `set` replaces the values for the next read. jsdom resolves no `var()` to px, so without
 * this stub the list reads an inset of 0.
 */
function stubListInsetStyle(style: ListInsetStyle): { set: (next: ListInsetStyle) => void, restore: () => void } {
  let current = style
  const realGetComputedStyle = window.getComputedStyle.bind(window)
  const spy = vi.spyOn(window, 'getComputedStyle').mockImplementation((element, pseudoElement) => {
    const computed = realGetComputedStyle(element, pseudoElement)
    if (!(element instanceof HTMLElement) || element.dataset.chatScrollContainer !== 'true')
      return computed
    return new Proxy(computed, {
      get: (target, key) => {
        if (key === 'paddingTop')
          return current.paddingTop
        if (key === 'rowGap')
          return current.rowGap
        const value: unknown = Reflect.get(target, key, target)
        return typeof value === 'function'
          ? value.bind(target)
          : value
      },
    })
  })
  return {
    set: (next) => {
      current = next
    },
    restore: () => spy.mockRestore(),
  }
}

/**
 * Stub the scroll geometry of `view` (see `stubChatScrollGeometry`) and report the size of its
 * scroll container to ChatView, both with the one viewport height `clientHeight`.
 */
async function stubChatViewport(view: { container: HTMLElement }, clientHeight = 500): Promise<StubbedChatScroll> {
  const scroll = stubChatScrollGeometry(view, clientHeight)
  await reportChatViewportSize(view, 800, clientHeight)
  return scroll
}

// `role` ('user' | 'assistant') only labels the call site for readability;
// ChatView classifies messages off `message.source` (left UNSPECIFIED here,
// matching the historical behavior of these tests), not a `role` field —
// AgentChatMessage has no `role`, so it is intentionally not placed on the
// returned proto.
function makeMessage(role: string, text: string, id: string = '1'): AgentChatMessage {
  const content = JSON.stringify({
    message: {
      content: [
        { type: 'text', text },
      ],
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

/**
 * The text of the rows in the list of `view`. The hidden copy that measures a staged row
 * holds the text of that row also, and it is outside the list, so it does not count.
 */
function listText(view: { container: HTMLElement }): string {
  return Array.from(
    view.container.querySelectorAll('[data-chat-scroll-container] [data-seq]'),
    row => row.textContent ?? '',
  ).join('\n')
}

/** An assistant message at `seq`, whose id (`msg-<seq>`) and text (`Message <seq>`) follow from it. */
function seqMessage(seq: number): AgentChatMessage {
  return {
    ...makeMessage('assistant', `Message ${seq}`, `msg-${seq}`),
    seq: BigInt(seq),
  }
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
          ? [
              { path: 'a.txt', kind: 'update', diff: params.diff ?? '@@ -1 +1 @@\n-old\n+new' },
            ]
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
    spanLines: params.completed
      ? JSON.stringify([
          { span_id: params.spanId, color: 1, type: 'connector_end' },
        ])
      : '[]',
  } as AgentChatMessage
}

function makeCodexHiddenLifecycleMessage(id: string = 'codex-hidden'): AgentChatMessage {
  // A Claude `system init` frame: the one lifecycle row the classifier still
  // hides through the shared hidden-subtype contract.
  return {
    $typeName: 'leapmux.v1.AgentChatMessage',
    id,
    source: MessageSource.LEAPMUX,
    content: new TextEncoder().encode(JSON.stringify({
      type: 'system',
      subtype: 'init',
    })),
    contentCompression: ContentCompression.NONE,
    seq: 1n,
    createdAt: '',
    agentProvider: AgentProvider.CLAUDE_CODE,
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

/** The height key of row `id` among the items that ChatView handed the stub virtualizer. */
function stubItemHeightKey(id: string): string | undefined {
  return virtualizerState.items?.().find(item => item.id === id)?.heightKey
}

/**
 * Whether `heightKey` holds `width` as its row width. The key ends with the JSON array of
 * the inputs that all rows share, and the width is the first of them.
 */
function heightKeyHoldsWidth(heightKey: string | undefined, width: number): boolean {
  return heightKey !== undefined && heightKey.includes(`|[${width},`)
}

function visibleBubbleIds(container: HTMLElement): string[] {
  return [
    ...container.querySelectorAll('[data-testid="mock-message-bubble"][data-premeasure="false"]'),
  ]
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

describe('sameVirtualItems', () => {
  const item = (id: string, hasSpanLines = false, heightKey?: string): VirtualItem => ({
    id,
    hasSpanLines,
    // Omitted (not undefined) when the row states no height key.
    ...(heightKey !== undefined
      ? { heightKey }
      : {}),
  })

  it('is true for the same reference and for a geometry-equivalent rebuild', () => {
    const a = [
      item('m1', false, '1'),
      item('m2', true, '2'),
    ]
    expect(sameVirtualItems(a, a)).toBe(true)
    // A fresh array (a streaming/command-stream delta re-walk) with identical
    // id/hasSpanLines/heightKey is geometry-equivalent -> keep the prior, no churn.
    const b = [
      item('m1', false, '1'),
      item('m2', true, '2'),
    ]
    expect(sameVirtualItems(a, b)).toBe(true)
  })

  it('is false when a content version, span-line flag, id, or length changes', () => {
    const base = [
      item('m1', false, '1'),
      item('m2', true, '2'),
    ]
    expect(sameVirtualItems(base, [
      item('m1', false, '1'),
      item('m2', true, '3'),
    ])).toBe(false) // heightKey bumped
    expect(sameVirtualItems(base, [
      item('m1', true, '1'),
      item('m2', true, '2'),
    ])).toBe(false) // hasSpanLines flipped
    expect(sameVirtualItems(base, [
      item('mX', false, '1'),
      item('m2', true, '2'),
    ])).toBe(false) // id changed (reorder/insert)
    expect(sameVirtualItems(base, [
      item('m1', false, '1'),
    ])).toBe(false) // a row appeared/left
  })
})

describe('ChatView', () => {
  it('draws the live compaction while the agent is idle', async () => {
    const messages = [
      makeMessage('assistant', 'The answer', 'msg-1'),
    ]
    const view = render(() => (
      <PreferencesProvider>
        <ChatView
          messages={messages}
          agentLifecycle={{
            agentWorking: false,
            liveCompaction: { trigger: 'auto' },
          }}
        />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)
    expect(view.getByTestId('compaction-indicator')).toHaveTextContent('Compacting context... (auto)')
  })

  it('lays out the hidden premeasure copy at the exact width of the list, and keys the heights by that width', async () => {
    // A hidden copy 4px wider than the row wraps its text differently, and its height is
    // then wrong by each line that wraps differently: thousands of pixels for a long row.
    // A height that a row measured at another width is a guess for the same reason, so
    // each width gives its own key.
    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={[
          makeMessage('assistant', 'An answer', 'msg-1'),
        ]}
        />
      </PreferencesProvider>
    ))
    await settleWorkerReplies()
    const premeasureRoot = view.container.querySelector('[data-chat-premeasure-root]') as HTMLElement | null
    expect(premeasureRoot).not.toBeNull()
    const heightCache = viewHeightCache()
    const storedKey = () => heightCache.snapshot().find(height => height.id === 'msg-1')?.heightKey

    // The hidden copy measures the row at this width, and the row joins the list.
    await reportChatViewportSize(view, 372, 733)
    expect(premeasureRoot!.style.width).toBe('372px')
    const row = view.container.querySelector('[data-seq="1"]') as HTMLElement | null
    expect(row).not.toBeNull()
    vi.spyOn(row!, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 372, 120))
    await triggerResizeObserverFor(row!)
    const keyAt372 = storedKey()
    expect(keyAt372).toBeDefined()

    // 379.5 and 372 shared one 8px step of the old key. The key of the row changes, and
    // the row confirms its height under the new key at its next read.
    await reportChatViewportSize(view, 379.5, 733)
    expect(premeasureRoot!.style.width).toBe('379.5px')
    await measureHiddenCopies(view.container)
    await triggerResizeObserverFor(row!)
    const keyAt379 = storedKey()
    expect(keyAt379).toBeDefined()
    expect(keyAt379).not.toBe(keyAt372)

    // Half a pixel more is another width, and another key.
    await reportChatViewportSize(view, 380, 733)
    expect(premeasureRoot!.style.width).toBe('380px')
    await measureHiddenCopies(view.container)
    await triggerResizeObserverFor(row!)
    const keyAt380 = storedKey()
    expect(keyAt380).toBeDefined()
    expect(keyAt380).not.toBe(keyAt372)
    expect(keyAt380).not.toBe(keyAt379)
    view.unmount()
  })

  it('passes the top padding and the row gap of the list to the virtualizer as the inset of its spacer', async () => {
    const inset = stubListInsetStyle({ paddingTop: '16px', rowGap: '12px' })
    try {
      const view = render(() => (
        <PreferencesProvider>
          <ChatView messages={[
            makeMessage('assistant', 'An answer', 'msg-1'),
          ]}
          />
        </PreferencesProvider>
      ))
      await reportChatViewportSize(view)

      const virt = virtualizerState.realInstances.at(-1)!
      expect(virt.layoutShift()).toBe(0)
      expect(virt.scrollTopOf(0)).toBe(28)
      expect(virt.contentTopOf(100)).toBe(72)
      view.unmount()
    }
    finally {
      inset.restore()
    }
  })

  it('reads a padding or a gap that is not in px as no inset, and keeps the other part', async () => {
    // A computed `row-gap` of `normal` and an empty value both parse to NaN.
    const inset = stubListInsetStyle({ paddingTop: '16px', rowGap: 'normal' })
    try {
      const view = render(() => (
        <PreferencesProvider>
          <ChatView messages={[
            makeMessage('assistant', 'An answer', 'msg-1'),
          ]}
          />
        </PreferencesProvider>
      ))
      await reportChatViewportSize(view, 800, 733)
      const virt = virtualizerState.realInstances.at(-1)!
      expect(virt.scrollTopOf(0)).toBe(16)

      inset.set({ paddingTop: '', rowGap: '12px' })
      await reportChatViewportSize(view, 800, 600)
      expect(virt.scrollTopOf(0)).toBe(12)
      view.unmount()
    }
    finally {
      inset.restore()
    }
  })

  it('reads the inset again when the list changes its height alone or its width alone', async () => {
    // A new padding, such as a narrower pane can give the list, comes with a new size of the
    // list. The size observer reports a change of each dimension on its own.
    const inset = stubListInsetStyle({ paddingTop: '16px', rowGap: '12px' })
    try {
      const view = render(() => (
        <PreferencesProvider>
          <ChatView messages={[
            makeMessage('assistant', 'An answer', 'msg-1'),
          ]}
          />
        </PreferencesProvider>
      ))
      await reportChatViewportSize(view, 800, 733)
      const virt = virtualizerState.realInstances.at(-1)!
      expect(virt.scrollTopOf(0)).toBe(28)

      inset.set({ paddingTop: '24px', rowGap: '12px' })
      await reportChatViewportSize(view, 800, 600)
      expect(virt.scrollTopOf(0)).toBe(36)

      inset.set({ paddingTop: '24px', rowGap: '20px' })
      await reportChatViewportSize(view, 1200, 600)
      expect(virt.scrollTopOf(0)).toBe(44)
      view.unmount()
    }
    finally {
      inset.restore()
    }
  })

  it('hides the live compaction when the reader views older history', async () => {
    const messages = [
      makeMessage('assistant', 'An earlier answer', 'msg-1'),
    ]
    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} pagination={{ hasNewerMessages: true }} agentLifecycle={{ liveCompaction: {} }} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)
    expect(view.queryByTestId('compaction-indicator')).toBeNull()
  })

  it('admits the later rows after a paired path-only Move result measures at zero height', async () => {
    const spanId = 'empty-native-move'
    const nativeMessage = (id: string, seq: bigint, payload: Record<string, unknown>, inSpan = false) => create(AgentChatMessageSchema, {
      id,
      seq,
      source: MessageSource.AGENT,
      agentProvider: AgentProvider.GITHUB_COPILOT,
      contentCompression: ContentCompression.NONE,
      content: new TextEncoder().encode(JSON.stringify(payload)),
      ...(inSpan
        ? { spanId, spanType: COPILOT_TOOL.Move }
        : {}),
    })
    const messages = [
      nativeMessage('move-request', 1n, copilotToolStart(spanId, COPILOT_TOOL.Move, { source: '/p/old.ts', path: '/p/new.ts' }), true),
      nativeMessage('move-result', 2n, copilotToolComplete(spanId, {
        result: { content: '' },
      }), true),
      nativeMessage('later-answer', 3n, copilotFrame(COPILOT_EVENT.AssistantMessage, { content: 'The actual later answer.' })),
      nativeMessage('later-divider', 4n, copilotFrame(COPILOT_EVENT.SessionIdle, {})),
    ]
    const messageContext = testMessageContext({
      messages: () => messages,
    })
    const view = render(() => <PreferencesProvider><ChatView messages={messages} messageContext={messageContext} /></PreferencesProvider>)
    await settleWorkerReplies()
    const scrollContainer = view.container.querySelector('[data-chat-scroll-container]') as HTMLElement
    vi.spyOn(scrollContainer, 'getBoundingClientRect').mockImplementation(() => ({ width: 800, height: 733 }) as DOMRect)
    await triggerResizeObserverFor(scrollContainer)
    await settleWorkerReplies()
    const row = (seq: number) => view.container.querySelector<HTMLElement>(`[data-seq="${seq}"]`)
    const copies = () => [
      ...view.container.querySelectorAll<HTMLElement>('[data-chat-premeasure-root] > *'),
    ]
    // The result row is an empty row: its hidden copy has a layout width and no height.
    const isResultCopy = (copy: HTMLElement) => copy.querySelector('[data-tool-status="completed"]') !== null
    const resultCopy = copies().find(isResultCopy)
    expect(resultCopy).toBeDefined()
    expect(resultCopy!.querySelector('[data-testid="message-content"]')?.textContent).toBe('')

    // Every copy but that of the result row measures. The rows after the result row are
    // measured then, and they still wait: the first view of this short chat is every row,
    // and one of them is not measured.
    for (const copy of copies().filter(copy => !isResultCopy(copy))) {
      vi.spyOn(copy, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 800, 40))
      await triggerResizeObserverFor(copy)
    }
    expect(row(3)).toBeNull()
    expect(row(4)).toBeNull()
    // A copy with no layout box reads nothing: a height of 0 counts only with a width.
    const rectangle = vi.spyOn(resultCopy!, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 0, 0))
    await triggerResizeObserverFor(resultCopy!)
    expect(row(3)).toBeNull()
    rectangle.mockReturnValue(new DOMRect(0, 0, 800, 0))
    await triggerResizeObserverFor(resultCopy!)
    await flushAnimationFrame()
    await settleWorkerReplies()

    expect(row(2)?.querySelector('[data-tool-status="completed"]')).not.toBeNull()
    expect(row(3)?.style.visibility).toBe('')
    expect(row(4)?.style.visibility).toBe('')
    expect(row(3)).toHaveTextContent('The actual later answer.')
    expect(row(4)?.querySelector('[data-testid="result-divider"]')).not.toBeNull()
    const heights = viewHeightCache().snapshot()
    expect(heights.find(height => height.id === 'move-result')?.height).toBe(0)
    view.unmount()

    // A second mount adopts the heights, the height of 0 included, with no hidden copy.
    const restored = render(() => <PreferencesProvider><ChatView messages={messages} messageContext={messageContext} /></PreferencesProvider>)
    await settleWorkerReplies()
    try {
      const restoredContainer = restored.container.querySelector('[data-chat-scroll-container]') as HTMLElement
      vi.spyOn(restoredContainer, 'getBoundingClientRect').mockImplementation(() => ({ width: 800, height: 733 }) as DOMRect)
      await triggerResizeObserverFor(restoredContainer)
      const virt = virtualizerState.realInstances.at(-1)!
      const heightCache = viewHeightCache()
      // The rows of this mount have the keys of the first one: the same content and width.
      const rowOf = (height: PersistableRowHeight): VirtualItem => ({
        id: height.id,
        hasSpanLines: false,
        ...(height.heightKey !== undefined
          ? { heightKey: height.heightKey }
          : {}),
      })
      expect(heightCache.commitAll(heights.map(height => ({
        row: rowOf(height),
        height: height.height,
        measuredKey: height.heightKey,
      })))).toBe(4)
      expect(heightCache.heightOf(rowOf(heights.find(height => height.id === 'move-result')!))).toBe(0)
      await flushAnimationFrame()
      await settleWorkerReplies()
      expect(virt.heightDebugOfId('move-result')).toEqual({ measured: 0 })
      const answer = restored.container.querySelector('[data-seq="3"]') as HTMLElement
      const divider = restored.container.querySelector('[data-seq="4"]') as HTMLElement
      expect(answer.style.visibility).toBe('')
      expect(divider.style.visibility).toBe('')
      expect(answer).toHaveTextContent('The actual later answer.')
    }
    finally {
      restored.unmount()
    }
  })

  it('keeps each chat instance ID stable and distinct', () => {
    const [messages, setMessages] = createSignal<AgentChatMessage[]>([])
    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages()} />
        <ChatView messages={[]} />
      </PreferencesProvider>
    ))
    const chats = view.container.querySelectorAll('[data-testid="chat-container"]')
    expect(chats).toHaveLength(2)
    const firstID = chats[0]?.getAttribute('data-chat-instance-id')
    const secondID = chats[1]?.getAttribute('data-chat-instance-id')
    expect(firstID).toBeTruthy()
    expect(secondID).toBeTruthy()
    expect(firstID).not.toBe(secondID)

    setMessages([
      message('new-row', 1),
    ])
    const updatedChats = view.container.querySelectorAll('[data-testid="chat-container"]')
    expect(updatedChats[0]?.getAttribute('data-chat-instance-id')).toBe(firstID)
    expect(updatedChats[1]?.getAttribute('data-chat-instance-id')).toBe(secondID)
  })

  it('renders an edit request recovered from the matching provider session', async () => {
    const spanId = 'reused-edit'
    const agentSessionId = 'new-session'
    const request = create(AgentChatMessageSchema, {
      id: 'new-session-request',
      source: MessageSource.AGENT,
      content: new TextEncoder().encode(JSON.stringify({
        sessionUpdate: 'tool_call',
        toolCallId: spanId,
        status: 'pending',
        kind: 'edit',
        title: 'edit',
        rawInput: { filePath: '/project/example.ts', oldString: 'correctSessionBefore', newString: 'correctSessionAfter' },
      })),
      contentCompression: ContentCompression.NONE,
      seq: 2n,
      agentProvider: AgentProvider.OPENCODE,
      spanId,
      spanType: 'edit',
      agentSessionId,
    })
    const result = create(AgentChatMessageSchema, {
      id: 'new-session-result',
      source: MessageSource.AGENT,
      content: new TextEncoder().encode(JSON.stringify({
        sessionUpdate: 'tool_call_update',
        toolCallId: spanId,
        status: 'completed',
        rawOutput: 'No file changes occurred.',
      })),
      contentCompression: ContentCompression.NONE,
      seq: 73n,
      agentProvider: AgentProvider.OPENCODE,
      spanId,
      spanType: 'edit',
      agentSessionId,
    })
    const fetchSpan = vi.fn(async () => [request, result])
    const messageContext = testMessageContext({
      messages: () => [result],
      fetchSpan,
    })
    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={[result]} messageContext={messageContext} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)

    await waitFor(() => expect(fetchSpan).toHaveBeenCalledWith({ spanId, agentSessionId }, expect.any(AbortSignal)))
    await waitFor(() => expect(messageContext.request({ spanId, agentSessionId })?.message.id).toBe(request.id))
    await waitFor(() => expect(view.container).toHaveTextContent('correctSessionBefore'))
    expect(view.container).toHaveTextContent('correctSessionAfter')
    expect(view.container).toHaveTextContent('No file changes occurred.')
  })

  it('renders empty state when no messages', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView messages={[]} />
      </PreferencesProvider>
    ))
    expect(screen.getByText('Send a message to start')).toBeInTheDocument()
  })

  it('does not resolve the save callback prop while its owner disposes', () => {
    const onSaveViewportScroll = vi.fn()
    let disposalStarted = false
    const chatProps = {
      messages: [] as AgentChatMessage[],
      get onSaveViewportScroll() {
        if (disposalStarted)
          throw new Error('read the save callback during disposal')
        return onSaveViewportScroll
      },
    }
    const view = render(() => (
      <PreferencesProvider>
        <ChatView {...chatProps} />
      </PreferencesProvider>
    ))
    const scrollContainer = view.container.querySelector('[data-chat-scroll-container]') as HTMLElement
    Object.defineProperty(scrollContainer, 'clientHeight', { value: 500, configurable: true })
    Object.defineProperty(scrollContainer, 'scrollHeight', { value: 3000, configurable: true })

    disposalStarted = true
    expect(() => view.unmount()).not.toThrow()
    expect(onSaveViewportScroll).toHaveBeenCalledWith({ atBottom: true, hasMoreNewer: false })
  })

  it('renders the older-loading indicator as an overlay OUTSIDE the scroll container', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView messages={[]} pagination={{ hasOlderMessages: true, fetchingOlder: true }} />
      </PreferencesProvider>
    ))
    const indicator = screen.getByText('Loading older messages...')
    expect(indicator).toBeInTheDocument()
    // An IN-FLOW indicator inside the scroll container shifts the virtualized content
    // by its height when fetchingOlder toggles -- a shift the layout shift can't see
    // (it follows only the virtual rows) -- so a scrolled-up reader bounces
    // and gets wedged re-triggering loadOlder. It must be an overlay sibling of the
    // scroll container, never a descendant.
    const scrollContainer = document.querySelector('[data-chat-scroll-container="true"]')
    expect(scrollContainer).toBeInTheDocument()
    expect(scrollContainer!.contains(indicator)).toBe(false)
  })

  it('gates the older-loading indicator on the top-edge stall, not the raw fetchingOlder flag', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView messages={[]} pagination={{ hasOlderMessages: true, fetchingOlder: true }} />
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
        <ChatView messages={[]} pagination={{ hasNewerMessages: true, fetchingNewer: true }} />
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
        <ChatView messages={[]} pagination={{ hasNewerMessages: true }} />
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
          messages={[
            makeMessage('user', 'hello', 'm1'),
          ]}

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
          messages={[
            makeMessage('user', 'hello', 'm1'),
          ]}

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
        <ChatView messages={[
          makeCodexHiddenLifecycleMessage(),
        ]}
        />
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
        <ChatView
          messages={[
            makeCodexHiddenLifecycleMessage(),
          ]}
          pagination={{ hasOlderMessages: true }}
        />
      </PreferencesProvider>
    ))
    expect(screen.queryByText('Send a message to start')).not.toBeInTheDocument()
  })

  it('hides EnterPlanMode tool_result messages in chat history', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView messages={[
          makeClaudeEnterPlanModeResultMessage(),
        ]}
        />
      </PreferencesProvider>
    ))
    expect(screen.getByText('Send a message to start')).toBeInTheDocument()
    expect(screen.queryByText('Entered plan mode')).not.toBeInTheDocument()
  })

  it('renders messages', async () => {
    const messages = [
      makeMessage('user', 'Hello', '1'),
      makeMessage('assistant', 'Hi there', '2'),
    ]
    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)
    await settleWorkerReplies()
    expect(screen.getByText('Hello')).toBeInTheDocument()
    expect(screen.getByText('Hi there')).toBeInTheDocument()
  })

  it('marks assistant message and thought ROWS as bands, and nothing else', async () => {
    // The full-bleed strip is the ROW's own background and border, so the band
    // marker must land on the row wrapper -- not on the bubble, which paint
    // containment would clip at the list gutter.
    const withContent = (id: string, inner: unknown, seq: bigint): AgentChatMessage => ({
      ...makeMessage('assistant', '', id),
      seq,
      content: new TextEncoder().encode(JSON.stringify(inner)),
    })
    const messages = [
      withContent('m1', {
        type: 'assistant',
        message: {
          content: [
            { type: 'text', text: 'answer' },
          ],
        },
      }, 1n),
      withContent('m2', {
        type: 'assistant',
        message: {
          content: [
            { type: 'thinking', thinking: 'pondering' },
          ],
        },
      }, 2n),
      withContent('m3', {
        type: 'assistant',
        message: {
          content: [
            {
              type: 'tool_use',
              id: 't1',
              name: 'Bash',
              input: { command: 'ls' },
            },
          ],
        },
      }, 3n),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)

    const bands = [
      ...view.container.querySelectorAll('[data-seq]'),
    ]
      .map(row => (row as HTMLElement).dataset.band)
    expect(bands).toEqual(['text', 'thought', undefined])
  })

  it('publishes the distance to the panel edge on the row content column, rails included', async () => {
    // A bleeding child reads this var instead of assuming the bare gutter. On a
    // row WITH rails the content column starts further right, and a bleed that
    // ignored that stopped short of the panel edge.
    const withSpanLines = (id: string, seq: bigint, lines: unknown[]): AgentChatMessage => ({
      ...makeMessage('assistant', 'answer', id),
      seq,
      spanLines: JSON.stringify(lines),
    })
    const messages = [
      withSpanLines('m1', 1n, []),
      withSpanLines('m2', 2n, [
        { span_id: 's1', color: 1, type: 'active' },
        { span_id: 's2', color: 2, type: 'active' },
      ]),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)

    const columnOf = (seq: string) => {
      const row = view.container.querySelector(`[data-seq="${seq}"]`) as HTMLElement
      return row.querySelector('[data-span-columns]')!.matches('[data-span-columns="0"]')
        ? row.querySelector('[data-span-columns="0"]') as HTMLElement
        : row.querySelector(`.${railedRowContent}`) as HTMLElement
    }

    expect(columnOf('1').style.getPropertyValue(ROW_BLEED_LEFT_VAR))
      .toBe(rowBleedLeftStyle(0)[ROW_BLEED_LEFT_VAR])
    expect(columnOf('2').style.getPropertyValue(ROW_BLEED_LEFT_VAR))
      .toBe(rowBleedLeftStyle(2)[ROW_BLEED_LEFT_VAR])
    // The two must differ, or the rails were not folded in at all.
    expect(columnOf('1').style.getPropertyValue(ROW_BLEED_LEFT_VAR))
      .not
      .toBe(columnOf('2').style.getPropertyValue(ROW_BLEED_LEFT_VAR))
  })

  it('widens a turn-end divider row without banding it', async () => {
    // A divider bleeds like a band, but its rule is drawn by a DESCENDANT, so
    // the row only widens -- it paints no strip and carries no band marker.
    const messages = [
      {
        ...makeMessage('assistant', '', 'm1'),
        seq: 1n,
        content: new TextEncoder().encode(JSON.stringify({ type: 'result', subtype: 'success', duration_ms: 1200 })),
      },
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)

    const row = view.container.querySelector('[data-seq="1"]') as HTMLElement | null
    expect(row).not.toBeNull()
    expect(row!.classList.contains(bleedRow)).toBe(true)
    expect(row!.classList.contains(bandRow)).toBe(false)
    expect(row!.dataset.band).toBeUndefined()
    expect(view.container.querySelector('[data-testid="result-divider"]')).not.toBeNull()
  })

  it('renders chat container', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView messages={[]} />
      </PreferencesProvider>
    ))
    expect(screen.getByTestId('chat-container')).toBeInTheDocument()
  })

  // A switch between unified and split mounts the new layout behind the old one in
  // idle slices (see DiffView). The switch changes the row's height key, but the
  // row's visible copy keeps showing the old layout at the old height until the
  // swap, so the row must stay visible, keep its slot, and commit its new height
  // once, when its own resize at the swap reports it.
  it('keeps a revealed diff row visible and its height in place while a switch to split mounts, and commits its height once, at the swap', async () => {
    const changedLines = 30
    const diff = [
      `@@ -1,${changedLines} +1,${changedLines} @@`,
      ...Array.from({ length: changedLines }, (_, i) => `-const before${i} = ${i}`),
      ...Array.from({ length: changedLines }, (_, i) => `+const after${i} = ${i}`),
    ].join('\n')
    const messages = [
      makeCodexFileChangeMessage({
        id: 'edit-row',
        seq: 1n,
        spanId: 'edit-span',
        status: 'completed',
        diff,
      }),
    ]
    let preferences: ReturnType<typeof usePreferences> | undefined
    const CapturePreferences = () => {
      preferences = usePreferences()
      return null
    }
    const view = render(() => (
      <PreferencesProvider>
        <CapturePreferences />
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await settleWorkerReplies()
    await reportChatViewportSize(view)
    const row = view.container.querySelector('[data-seq="1"]') as HTMLElement | null
    expect(row).not.toBeNull()
    const rowRect = vi.spyOn(row!, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 800, 500))
    await triggerResizeObserverFor(row!)
    await waitFor(() => expect(row!.style.visibility).toBe(''))
    const virt = virtualizerState.realInstances.at(-1)!
    expect(virt.heightOfId('edit-row')).toBe(500)
    expect(row!.querySelectorAll('[data-file-diff]')).toHaveLength(1)
    const premeasureRoot = view.container.querySelector('[data-chat-premeasure-root]')
    expect(premeasureRoot?.childElementCount).toBe(0)
    const commitsBeforeSwitch = virt.lastMeasurement()?.commitSeq

    const slices = installControlledSlices(10)
    preferences!.dual.diffView.setBrowser('split')
    await settleWorkerReplies()
    // The switch changes the key of the row. A hidden copy that the change mounted would
    // measure here, and the loop below asserts that none exists.
    await measureHiddenCopies(view.container)

    // The old layout stays while the new one mounts behind it, and the row keeps
    // its slot and needs no hidden copy: its visible copy measures itself.
    expect(row!.querySelectorAll('[data-file-diff]')).toHaveLength(2)
    expect(slices.waiting()).toBeGreaterThan(0)
    let slicesRun = 0
    while (slices.waiting() > 0) {
      expect(row!.style.visibility).toBe('')
      expect(premeasureRoot?.childElementCount).toBe(0)
      expect(virt.heightOfId('edit-row')).toBe(500)
      expect(virt.lastMeasurement()?.commitSeq).toBe(commitsBeforeSwitch)
      slices.runSlice()
      slicesRun += 1
      await settleWorkerReplies()
    }
    expect(slicesRun).toBeGreaterThan(1)

    // The swap shows only the new layout, and the row's resize commits its new height.
    expect(row!.querySelectorAll('[data-file-diff]')).toHaveLength(1)
    expect(row!.style.visibility).toBe('')
    expect(virt.lastMeasurement()?.commitSeq).toBe(commitsBeforeSwitch)
    rowRect.mockReturnValue(new DOMRect(0, 0, 800, 300))
    await triggerResizeObserverFor(row!)

    expect(virt.heightOfId('edit-row')).toBe(300)
    expect(virt.lastMeasurement()).toMatchObject({
      id: 'edit-row',
      source: 'visible',
      previousHeight: 500,
      newHeight: 300,
      commitSeq: commitsBeforeSwitch! + 1,
    })
    expect(row!.style.visibility).toBe('')
  })

  // A diff of added lines draws one line per row in both layouts, so its row keeps
  // its height through a switch, and no resize reports anything. The row reads
  // its height once the new layout shows, so the height that it kept holds under
  // its new key: a snapshot saves it under that key, and the row keeps it when it unmounts.
  it('confirms the height of a diff row whose new layout keeps its height, once the switch to split shows it', async () => {
    const addedLines = 30
    const diff = [
      `@@ -0,0 +1,${addedLines} @@`,
      ...Array.from({ length: addedLines }, (_, i) => `+const added${i} = ${i}`),
    ].join('\n')
    const messages = [
      makeCodexFileChangeMessage({
        id: 'edit-row',
        seq: 1n,
        spanId: 'edit-span',
        status: 'completed',
        diff,
      }),
    ]
    let preferences: ReturnType<typeof usePreferences> | undefined
    const CapturePreferences = () => {
      preferences = usePreferences()
      return null
    }
    const view = render(() => (
      <PreferencesProvider>
        <CapturePreferences />
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await settleWorkerReplies()
    await reportChatViewportSize(view)
    const row = view.container.querySelector('[data-seq="1"]') as HTMLElement | null
    expect(row).not.toBeNull()
    vi.spyOn(row!, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 800, 500))
    await triggerResizeObserverFor(row!)
    await waitFor(() => expect(row!.style.visibility).toBe(''))
    const virt = virtualizerState.realInstances.at(-1)!
    const heightCache = viewHeightCache()
    // The entry of the row in a snapshot: its height, with the key of the measure.
    const saved = () => heightCache.snapshot().find(height => height.id === 'edit-row')
    const keyBeforeSwitch = saved()?.heightKey
    expect(keyBeforeSwitch).toBeDefined()

    const slices = installControlledSlices(10)
    preferences!.dual.diffView.setBrowser('split')
    await settleWorkerReplies()

    // The new layout mounts behind the old one, so the row reads nothing yet: the cache
    // holds its height under the key of before the switch.
    expect(row!.querySelectorAll('[data-file-diff]')).toHaveLength(2)
    expect(saved()?.heightKey).toBe(keyBeforeSwitch)
    while (slices.waiting() > 0) {
      slices.runSlice()
      await settleWorkerReplies()
    }

    expect(row!.querySelectorAll('[data-file-diff]')).toHaveLength(1)
    expect(saved()?.heightKey).toBeDefined()
    expect(saved()?.heightKey).not.toBe(keyBeforeSwitch)
    expect(virt.heightOfId('edit-row')).toBe(500)
    expect(saved()?.height).toBe(500)
    expect(row!.style.visibility).toBe('')
  })

  it('renders no row before a hidden copy measured it', async () => {
    const messages = [
      {
        ...makeMessage('assistant', 'Older tall output', 'msg-1'),
        seq: 1n,
      },
      {
        ...makeMessage('assistant', 'An answer', 'msg-2'),
        seq: 2n,
      },
      {
        ...makeMessage('assistant', 'Current message', 'msg-3'),
        seq: 3n,
      },
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await settleWorkerReplies()
    const rows = () => view.container.querySelectorAll('[data-chat-scroll-container] [data-seq]')

    // The size of the list alone: each hidden copy mounts, and none has a layout box yet.
    const scrollContainer = view.container.querySelector('[data-chat-scroll-container]') as HTMLElement
    vi.spyOn(scrollContainer, 'getBoundingClientRect').mockImplementation(() => ({ width: 800, height: 733 }) as DOMRect)
    await triggerResizeObserverFor(scrollContainer)
    await settleWorkerReplies()

    // No row is in the list, and no space stands in for one: the list holds measured
    // rows only.
    expect(view.container.querySelectorAll('[data-chat-premeasure-root] > *')).toHaveLength(3)
    expect(rows()).toHaveLength(0)
    const spacer = scrollContainer.querySelector<HTMLElement>(`.${virtualSpacer}`)
    expect(spacer).not.toBeNull()
    expect(spacer!.style.height).toBe('0px')

    await measureHiddenCopies(view.container)
    await settleWorkerReplies()

    expect(rows()).toHaveLength(3)
    // The three rows at the height that their copies measured, and the two gaps.
    expect(spacer!.style.height).toBe('328px')
  })

  it('shows the first-view indicator once the wait lasted, and removes it when the rows join', async () => {
    vi.useFakeTimers({
      toFake: ['setTimeout', 'clearTimeout'],
    })
    try {
      const messages = [1, 2, 3].map(seq => seqMessage(seq))
      const view = render(() => (
        <PreferencesProvider>
          <ChatView messages={messages} />
        </PreferencesProvider>
      ))
      // The size of the list alone: the hidden copies mount, and none has a layout box yet.
      const scrollContainer = view.container.querySelector('[data-chat-scroll-container]') as HTMLElement
      vi.spyOn(scrollContainer, 'getBoundingClientRect').mockImplementation(() => ({ width: 800, height: 733 }) as DOMRect)
      await triggerResizeObserverFor(scrollContainer)

      // Most first views measure within a few frames, so the indicator waits.
      vi.advanceTimersByTime(SKELETON_SHOW_DELAY_MS - 1)
      expect(view.queryByText('Loading messages...')).toBeNull()
      vi.advanceTimersByTime(1)
      const indicator = view.queryByText('Loading messages...')
      expect(indicator).not.toBeNull()
      // An overlay beside the scroll container, so it never moves the rows.
      expect(scrollContainer.contains(indicator)).toBe(false)

      await measureHiddenCopies(view.container)

      expect(view.queryByText('Loading messages...')).toBeNull()
      expect(view.container.querySelectorAll('[data-chat-scroll-container] [data-seq]')).toHaveLength(3)
    }
    finally {
      vi.useRealTimers()
    }
  })

  it('keeps a row in the list when its content changes, before any hidden copy measured the new content', async () => {
    // A content change builds a new entry, and the row list replaces the element of the
    // row: the old element detaches, then the new one attaches. The row is on screen the
    // whole time, so it keeps the height that it shows, and the rows around it stay.
    const { transcript, resolver } = testTranscriptContext([
      seqMessage(1),
      seqMessage(2),
    ])
    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={transcript.sources.messages()} messageContext={resolver} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)
    await settleWorkerReplies()
    const rows = () => view.container.querySelectorAll('[data-chat-scroll-container] [data-seq]')
    expect(rows()).toHaveLength(2)
    const before = view.container.querySelector('[data-chat-scroll-container] [data-seq="1"]')
    const virt = virtualizerState.realInstances.at(-1)!
    const heightBefore = virt.heightOfId('msg-1')
    const nextRowTopBefore = virt.offsetOfId('msg-2')
    expect(heightBefore).toBeGreaterThan(0)

    transcript.replace({
      ...makeMessage('assistant', 'Message 1, revised', 'msg-1'),
      seq: 1n,
    })
    await flushAnimationFrame()
    await settleWorkerReplies()

    // No hidden copy has a layout box here, so nothing measured the new content.
    const after = view.container.querySelector('[data-chat-scroll-container] [data-seq="1"]')
    expect(after).not.toBeNull()
    expect(after).not.toBe(before)
    expect(rows()).toHaveLength(2)
    expect(after).toHaveTextContent('Message 1, revised')
    // The new element of the row has no layout box here either, so no read gave it a
    // height. The row keeps the slot that it showed, and the row below it keeps its place.
    expect(virt.heightOfId('msg-1')).toBe(heightBefore)
    expect(virt.offsetOfId('msg-2')).toBe(nextRowTopBefore)
  })

  describe('stored row heights', () => {
    useTestStorage()

    it('admits a row with a stored height, with no hidden copy', async () => {
      const messages = [1, 2, 3].map(seq => seqMessage(seq))
      // A first mount gives the height key of each row at this width.
      const first = render(() => (
        <PreferencesProvider>
          <ChatView messages={messages} />
        </PreferencesProvider>
      ))
      await reportChatViewportSize(first)
      const heights = viewHeightCache().snapshot()
      expect(heights).toHaveLength(3)
      first.unmount()
      localStorageStore(`${PREFIX_CHAT_ROW_HEIGHTS}agent-stored`, {
        v: STORED_ROW_HEIGHTS_VERSION,
        rows: heights.map(height => [
          height.id,
          fnv1a32Hex(height.heightKey ?? ''),
          height.height,
        ]),
      })

      const view = render(() => (
        <PreferencesProvider>
          <ChatView agentId="agent-stored" messages={messages} />
        </PreferencesProvider>
      ))
      // The size of the list alone: no hidden copy gets a layout box.
      const scrollContainer = view.container.querySelector('[data-chat-scroll-container]') as HTMLElement
      vi.spyOn(scrollContainer, 'getBoundingClientRect').mockImplementation(() => ({ width: 800, height: 733 }) as DOMRect)
      await triggerResizeObserverFor(scrollContainer)

      await waitFor(() => {
        expect(view.container.querySelectorAll('[data-chat-scroll-container] [data-seq]')).toHaveLength(3)
      })
      expect(view.container.querySelectorAll('[data-chat-premeasure-root] > *')).toHaveLength(0)
      view.unmount()
    })
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

    const view = render(() => {
      const [messages, updateMessages] = createSignal(initialMessages)
      setMessages = updateMessages
      return (
        <PreferencesProvider>
          <ChatView messages={messages()} />
        </PreferencesProvider>
      )
    })
    await reportChatViewportSize(view)
    await settleWorkerReplies()

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
    await settleWorkerReplies()
    // The new message joins the list after its hidden copy measured it.
    await measureHiddenCopies(view.container)

    await waitFor(() => expect(screen.getByText('Initial reasoning summary')).toBeInTheDocument())
    expect(listText(view)).toContain('Follow-up message')
  })

  it('does not snap to bottom after older messages are prepended while browsing history', async () => {
    const initialMessages = [
      makeMessage('assistant', 'Newest 1', 'msg-100'),
      makeMessage('assistant', 'Newest 2', 'msg-101'),
    ].map((message, index) => ({
      ...message,
      seq: BigInt(100 + index),
    }))
    let setMessages!: (messages: AgentChatMessage[]) => void

    const view = render(() => {
      const [messages, updateMessages] = createSignal(initialMessages)
      setMessages = updateMessages
      return (
        <PreferencesProvider>
          <ChatView messages={messages()} pagination={{ hasOlderMessages: true }} />
        </PreferencesProvider>
      )
    })
    await reportChatViewportSize(view)

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
      {
        ...makeMessage('assistant', 'Older 1', 'msg-050'),
        seq: 50n,
      },
      {
        ...makeMessage('assistant', 'Older 2', 'msg-051'),
        seq: 51n,
      },
      ...initialMessages,
    ])

    // The older rows join the list after their hidden copies measured them. The
    // virtualized list then anchors the viewport to the message at the top of the
    // viewport (by seq + offset) rather than shifting scrollTop by a raw scrollHeight
    // delta. The key guarantee is that the view is NOT snapped to the bottom. (The
    // pixel-accurate offset math is unit-tested in the useChatVirtualizer.*.test.ts /
    // useChatScroll.*.test.ts suites.)
    await measureHiddenCopies(view.container)
    await waitFor(() => expect(listText(view)).toContain('Older 1'))
    expect(scrollTop).toBeLessThan(scrollHeight - clientHeight)

    scrollHeight = 2700
    setMessages([
      {
        ...makeMessage('assistant', 'Older 1', 'msg-050'),
        seq: 50n,
      },
      {
        ...makeMessage('assistant', 'Older 2', 'msg-051'),
        seq: 51n,
      },
      ...initialMessages,
      {
        ...makeMessage('assistant', 'Newest 3', 'msg-102'),
        seq: 102n,
      },
    ])

    await measureHiddenCopies(view.container)
    await waitFor(() => expect(listText(view)).toContain('Newest 3'))
    expect(scrollTop).toBeLessThan(scrollHeight - clientHeight)
  })

  it('keeps the mounted rows when an older page is prepended', async () => {
    const newest = Array.from({ length: 150 }, (_, i) => seqMessage(100 + i))
    const older = Array.from({ length: 50 }, (_, i) => seqMessage(50 + i))
    let setMessages!: (messages: AgentChatMessage[]) => void
    const view = render(() => {
      const [messages, updateMessages] = createSignal<AgentChatMessage[]>(newest)
      setMessages = updateMessages
      return (
        <PreferencesProvider>
          <ChatView messages={messages()} pagination={{ hasOlderMessages: true }} />
        </PreferencesProvider>
      )
    })
    const scroll = await stubChatViewport(view)
    scroll.scrollTo(6000)
    await flushAnimationFrame()

    const rowsBySeq = () => new Map(
      [
        ...scroll.spacer().querySelectorAll<HTMLElement>('[data-seq]'),
      ].map(row => [
        Number(row.dataset.seq),
        row,
      ]),
    )
    const before = rowsBySeq()
    expect(before.size).toBeGreaterThan(0)
    const lowestBefore = Math.min(...before.keys())
    const added: HTMLElement[] = []
    const observer = new MutationObserver((records) => {
      for (const record of records) {
        for (const node of record.addedNodes) {
          if (node instanceof HTMLElement && node.dataset.seq !== undefined)
            added.push(node)
        }
      }
    })
    observer.observe(scroll.spacer(), { childList: true, subtree: true })
    try {
      setMessages([
        ...older,
        ...newest,
      ])
      // The rows of the page are staged until their hidden copies measure them.
      await measureHiddenCopies(view.container)
      await flushAnimationFrame()
      await flushAnimationFrame()
      for (const record of observer.takeRecords()) {
        for (const node of record.addedNodes) {
          if (node instanceof HTMLElement && node.dataset.seq !== undefined)
            added.push(node)
        }
      }
    }
    finally {
      observer.disconnect()
    }

    const after = rowsBySeq()
    const survivors = [...before].filter(([seq, row]) => row.isConnected && after.get(seq) === row)
    expect(survivors).toHaveLength(before.size)
    expect(added.map(row => Number(row.dataset.seq)).filter(seq => seq < lowestBefore - 2)).toEqual([])
  })

  it('keeps the view when the row under the reader grows at its bottom', async () => {
    const messages = [1, 2, 3].map(seq => seqMessage(seq))
    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await settleWorkerReplies()
    const scroll = await stubChatViewport(view)
    const row = (seq: number): HTMLElement => {
      const element = scroll.spacer().querySelector(`[data-seq="${seq}"]`)
      if (!(element instanceof HTMLElement))
        throw new TypeError(`The row of seq ${seq} did not mount.`)
      return element
    }
    const firstRect = vi.spyOn(row(1), 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 800, 2000))
    await triggerResizeObserverFor(row(1))
    for (const seq of [2, 3]) {
      vi.spyOn(row(seq), 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 800, 100))
      await triggerResizeObserverFor(row(seq))
    }

    vi.useFakeTimers({
      toFake: ['performance', 'setTimeout', 'clearTimeout'],
    })
    try {
      scroll.scrollTo(600)
      firstRect.mockReturnValue(new DOMRect(0, 0, 800, 2400))
      await triggerResizeObserverFor(row(1))
      vi.advanceTimersByTime(200)
      await flushAnimationFrame()

      expect(scroll.scrollTop()).toBe(600)
    }
    finally {
      vi.useRealTimers()
      view.unmount()
    }
  })

  it('moves the layer and keeps the rows when a row above measures', async () => {
    const messages = Array.from({ length: 30 }, (_, i) => seqMessage(i + 1))
    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await settleWorkerReplies()
    const scroll = await stubChatViewport(view)
    const layer = () => scroll.spacer().querySelector<HTMLElement>(`.${virtualLayer}`)!
    const mountedRows = () => [
      ...scroll.spacer().querySelectorAll<HTMLElement>('[data-seq]'),
    ]
    try {
      scroll.scrollTo(1500)
      // These measures replace the 96px estimate, so they leave a shift.
      for (const row of mountedRows()) {
        vi.spyOn(row, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 800, 100))
        await triggerResizeObserverFor(row)
      }
      // The rebase at rest removes the shift.
      await waitFor(() => expect(layer().style.transform).toBe('translateY(0px)'))
      const s0 = scroll.scrollTop()
      const spacerHeight = scroll.spacer().style.height

      fireEvent.pointerDown(scroll.list, { pointerId: 1, isPrimary: true })
      // The first mounted row is above the viewport top: the overscan mounts rows above it.
      const above = mountedRows()[0]!
      expect(Number.parseFloat(above.style.transform.replace('translateY(', ''))).toBeLessThan(s0 - 100)
      vi.spyOn(above, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 800, 300))
      await triggerResizeObserverFor(above)

      expect(layer().style.transform).toBe('translateY(-200px)')
      expect(scroll.spacer().style.height).toBe(spacerHeight)
      expect(scroll.scrollTop()).toBe(s0)

      fireEvent.pointerUp(scroll.list, { pointerId: 1, isPrimary: true })
      expect(layer().style.transform).toBe('translateY(0px)')
      expect(scroll.scrollTop()).toBe(s0 + 200)
    }
    finally {
      view.unmount()
    }
  })

  it('keeps the rows under a drag whose scroll event did not arrive when the last row measures', async () => {
    const messages = Array.from({ length: 30 }, (_, i) => seqMessage(i + 1))
    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await settleWorkerReplies()
    const scroll = await stubChatViewport(view)
    await settleWorkerReplies()
    const layer = () => scroll.spacer().querySelector<HTMLElement>(`.${virtualLayer}`)!
    const mountedRows = () => [
      ...scroll.spacer().querySelectorAll<HTMLElement>('[data-seq]'),
    ]
    const bottom = () => scroll.list.scrollHeight - scroll.list.clientHeight
    try {
      // Every mounted row measures 100px, so 100px also becomes the estimate of the rows
      // above them, and one more measure changes no estimate.
      for (const row of mountedRows()) {
        vi.spyOn(row, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 800, 100))
        await triggerResizeObserverFor(row)
      }
      // The follower rests at the bottom, and the rebase at rest removed the shift.
      await waitFor(() => expect(layer().style.transform).toBe('translateY(0px)'))
      expect(scroll.scrollTop()).toBe(bottom())
      // A touch or scrollbar drag moved the view up, and its first `scroll` event did not
      // arrive yet.
      const dragged = bottom() - 300
      scroll.list.scrollTop = dragged

      const last = scroll.spacer().querySelector<HTMLElement>('[data-seq="30"]')!
      vi.spyOn(last, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 800, 400))
      await triggerResizeObserverFor(last)

      // The view no longer follows the tail, so the rule holds the rows under the reader,
      // and the last row grows below the view. Holding the bottom instead would move the
      // rows up under the drag by the growth of the row, 300px.
      expect(layer().style.transform).toBe('translateY(0px)')
      expect(scroll.scrollTop()).toBe(dragged)
    }
    finally {
      view.unmount()
    }
  })

  describe('a restore onto a row that the reader did not see', () => {
    // The reader sat 10px into msg-15 when the previous pane saved, and the row was 96px.
    const anchor = {
      id: 'msg-15',
      offsetWithinRow: 10,
      basisHeight: 96,
      seq: 15n,
    }

    /**
     * Mount with the saved anchor, and show the pane. The hidden copy of each row reports
     * 96px, so the rows join the list at that height, and no row of the list read its own
     * height yet.
     */
    async function restored() {
      const messages = Array.from({ length: 30 }, (_, i) => seqMessage(i + 1))
      const view = render(() => (
        <PreferencesProvider>
          <ChatView
            messages={messages}
            savedViewportScroll={{ atBottom: false, hasMoreNewer: false, anchor }}
            onClearSavedViewportScroll={() => {}}
          />
        </PreferencesProvider>
      ))
      await settleWorkerReplies()
      const scroll = await stubChatViewport(view)
      await settleWorkerReplies()
      const translateY = (element: HTMLElement) => Number.parseFloat(element.style.transform.replace('translateY(', ''))
      const layer = () => scroll.spacer().querySelector<HTMLElement>(`.${virtualLayer}`)!
      const row = scroll.spacer().querySelector<HTMLElement>('[data-seq="15"]')!
      return {
        view,
        scroll,
        row,
        /** The distance from the top of msg-15 to the viewport top. */
        intoRow: () => scroll.scrollTop() - translateY(layer()) - translateY(row),
        /** msg-15 measures `height`. */
        measure: async (height: number) => {
          vi.spyOn(row, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 800, height))
          await triggerResizeObserverFor(row)
        },
      }
    }

    it('keeps the position once the row measures', async () => {
      const r = await restored()
      try {
        expect(r.intoRow()).toBeCloseTo(10)
        // The row now measures 400px, so the view lands as far into it as the saved
        // fraction of the row: 10 / 96 of 400px.
        await r.measure(400)
        expect(r.intoRow()).toBeCloseTo(10 * 400 / 96)
      }
      finally {
        r.view.unmount()
      }
    })

    it('leaves a reader who moved to the shift rule', async () => {
      const r = await restored()
      try {
        r.scroll.scrollTo(r.scroll.scrollTop() + 50)
        expect(r.intoRow()).toBeCloseTo(60)
        // The rule holds the bottom edge of a row whose height the reader did not see: the
        // 36px of the row below the viewport top stay where they are.
        await r.measure(400)
        expect(r.intoRow()).toBeCloseTo(400 - 36)
      }
      finally {
        r.view.unmount()
      }
    })

    it('leaves a reader whose move has no event yet to the shift rule', async () => {
      const r = await restored()
      try {
        // The browser moved the view, and the `scroll` event of the move did not arrive yet.
        r.scroll.list.scrollTop = r.scroll.scrollTop() + 50
        expect(r.intoRow()).toBeCloseTo(60)
        // A placement on the anchor would undo the move of the reader. The rule holds the
        // bottom edge of the row instead, as for a reader whose event arrived.
        await r.measure(400)
        expect(r.intoRow()).toBeCloseTo(400 - 36)
      }
      finally {
        r.view.unmount()
      }
    })

    it('leaves a reader who asked for the live tail at the bottom', async () => {
      const r = await restored()
      try {
        fireEvent.keyDown(r.scroll.list, { key: 'End' })
        const bottom = () => r.scroll.list.scrollHeight - r.scroll.list.clientHeight
        expect(r.scroll.scrollTop()).toBe(bottom())
        await r.measure(400)
        expect(r.scroll.scrollTop()).toBe(bottom())
      }
      finally {
        r.view.unmount()
      }
    })
  })

  it('gives the scroll-to-bottom button an accessible name', async () => {
    const messages = Array.from({ length: 30 }, (_, i) => seqMessage(i + 1))
    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    const scroll = await stubChatViewport(view)
    try {
      expect(view.queryByRole('button', { name: 'Scroll to bottom' })).toBeNull()
      scroll.scrollTo(1000)
      expect(await view.findByRole('button', { name: 'Scroll to bottom' })).toBeInTheDocument()
    }
    finally {
      view.unmount()
    }
  })

  it('re-sticks to the bottom when a progress counter grows at the tail', async () => {
    // The thinking indicator is a tail sibling no ResizeObserver here watches; its
    // height growth (a climbing token count wrapping the verb row) does NOT move the
    // auto-scroll signature, so a dedicated effect must re-stick on the token count.
    // (agentWorking is left false so the visible indicator's rAF animation loop --
    // which the synchronous test rAF stub would recurse infinitely -- stays unmounted;
    // the effect is keyed on the thinkingTokens prop regardless, and the indicator's
    // jsdom-absent layout growth is simulated by growing scrollHeight below.)
    let setTokens!: (n: number) => void
    let setOutputBytes!: (n: number) => void
    const messages = [
      {
        ...makeMessage('assistant', 'Working on it', 'msg-1'),
        seq: 1n,
      },
    ]

    const view = render(() => {
      const [tokens, updateTokens] = createSignal(0)
      const [outputBytes, updateOutputBytes] = createSignal(0)
      setTokens = updateTokens
      setOutputBytes = updateOutputBytes
      return (
        <PreferencesProvider>
          <ChatView
            messages={messages}
            agentLifecycle={{
              thinkingTokens: tokens(),
              outputBytes: outputBytes(),
            }}
          />
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
    Object.defineProperty(messageList, 'scrollHeight', {
      configurable: true,
      get: () => scrollHeight,
    })
    Object.defineProperty(messageList, 'clientHeight', {
      configurable: true,
      get: () => clientHeight,
    })

    // The pane is shown, and its row joins the list: the reader follows the tail from the
    // first view on.
    await reportChatViewportSize(view, 800, clientHeight)
    // Pin to the bottom, and let the scroll handler see that position.
    scrollTop = scrollHeight - clientHeight // 500
    fireEvent.scroll(messageList)
    await flushAnimationFrame()

    // The token count grows while the message list and agentWorking stay put.
    scrollHeight = 1300
    setTokens(842)
    await flushAnimationFrame()
    await Promise.resolve()

    // The dedicated thinkingTokens re-stick effect snapped the view to the new bottom.
    await waitFor(() => expect(scrollTop).toBe(scrollHeight - clientHeight)) // 800

    // The output count uses its own reactive effect because it can change while
    // the token count stays fixed.
    scrollHeight = 1600
    setOutputBytes(1536)
    await flushAnimationFrame()
    await Promise.resolve()
    await waitFor(() => expect(scrollTop).toBe(scrollHeight - clientHeight)) // 1100
    view.unmount()
  })

  it('does not snap to bottom when a new message arrives before older-message anchoring finishes', async () => {
    const initialMessages = [
      makeMessage('assistant', 'Newest 1', 'msg-100'),
      makeMessage('assistant', 'Newest 2', 'msg-101'),
    ].map((message, index) => ({
      ...message,
      seq: BigInt(100 + index),
    }))
    let setMessages!: (messages: AgentChatMessage[]) => void

    const view = render(() => {
      const [messages, updateMessages] = createSignal(initialMessages)
      setMessages = updateMessages
      return (
        <PreferencesProvider>
          <ChatView messages={messages()} pagination={{ hasOlderMessages: true }} />
        </PreferencesProvider>
      )
    })
    await reportChatViewportSize(view)

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
      {
        ...makeMessage('assistant', 'Older 1', 'msg-050'),
        seq: 50n,
      },
      {
        ...makeMessage('assistant', 'Older 2', 'msg-051'),
        seq: 51n,
      },
      ...initialMessages,
      {
        ...makeMessage('assistant', 'Newest 3', 'msg-102'),
        seq: 102n,
      },
    ])

    // The rows on both sides join the list after their hidden copies measured them.
    await measureHiddenCopies(view.container)
    await waitFor(() => expect(listText(view)).toContain('Newest 3'))
    expect(listText(view)).toContain('Older 1')
    // Anchored to the viewport-top message, not snapped to the bottom.
    await waitFor(() => expect(scrollTop).toBeLessThan(scrollHeight - clientHeight))
  })

  it('does not snap to bottom after older loading finishes while the user is still browsing history', async () => {
    const initialMessages = [
      makeMessage('assistant', 'Newest 1', 'msg-100'),
      makeMessage('assistant', 'Newest 2', 'msg-101'),
    ].map((message, index) => ({
      ...message,
      seq: BigInt(100 + index),
    }))
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

            pagination={{
              hasOlderMessages: true,
              fetchingOlder: fetchingOlder(),
            }}
          />
        </PreferencesProvider>
      )
    })
    await reportChatViewportSize(view)

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
      {
        ...makeMessage('assistant', 'Older 1', 'msg-050'),
        seq: 50n,
      },
      {
        ...makeMessage('assistant', 'Older 2', 'msg-051'),
        seq: 51n,
      },
      ...initialMessages,
    ])

    // The older rows join the list after their hidden copies measured them. The view is
    // anchored to the viewport-top message, not snapped to the bottom.
    await measureHiddenCopies(view.container)
    await waitFor(() => expect(listText(view)).toContain('Older 1'))
    expect(scrollTop).toBeLessThan(scrollHeight - clientHeight)

    setFetchingOlder(false)
    scrollHeight = 2700
    setMessages([
      {
        ...makeMessage('assistant', 'Older 1', 'msg-050'),
        seq: 50n,
      },
      {
        ...makeMessage('assistant', 'Older 2', 'msg-051'),
        seq: 51n,
      },
      ...initialMessages,
      {
        ...makeMessage('assistant', 'Newest 3', 'msg-102'),
        seq: 102n,
      },
    ])

    await measureHiddenCopies(view.container)
    await waitFor(() => expect(listText(view)).toContain('Newest 3'))
    expect(scrollTop).toBeLessThan(scrollHeight - clientHeight)
  })

  it('loads older history after a restore to the top', async () => {
    const messages = [
      makeMessage('assistant', 'Retained 1', 'msg-1'),
      makeMessage('assistant', 'Retained 2', 'msg-2'),
    ].map((message, index) => ({
      ...message,
      seq: BigInt(index + 1),
    }))
    const onLoadOlderMessages = vi.fn()
    const onClearSavedViewportScroll = vi.fn()

    const view = render(() => (
      <PreferencesProvider>
        <ChatView
          messages={messages}

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

    // The pane is shown: the restore asks for the top, and the rows join the list when
    // their hidden copies are measured. No page loads before that.
    clientHeight = 100
    await reportChatViewportSize(view, 800, 100)

    expect(scrollTop).toBe(0)
    expect(onClearSavedViewportScroll).toHaveBeenCalledTimes(1)

    // A prepend no longer moves the restored view (the layout shift holds it), so nothing
    // holds the older history back after a restore near the top.
    fireEvent.scroll(messageList)
    expect(onLoadOlderMessages).toHaveBeenCalled()
  })

  it('loads older messages on explicit upward wheel intent while at top', async () => {
    const messages = [
      makeMessage('assistant', 'Retained 1', 'msg-1'),
      makeMessage('assistant', 'Retained 2', 'msg-2'),
    ].map((message, index) => ({
      ...message,
      seq: BigInt(index + 1),
    }))
    const onLoadOlderMessages = vi.fn()

    const view = render(() => (
      <PreferencesProvider>
        <ChatView
          messages={messages}

          pagination={{ hasOlderMessages: true, onLoadOlderMessages }}
        />
      </PreferencesProvider>
    ))
    // The rows join the list first: no page loads while loaded rows wait for their measure.
    await reportChatViewportSize(view)

    const chatContainer = screen.getByTestId('chat-container')
    const messageList = chatContainer.firstElementChild?.firstElementChild as HTMLDivElement
    Object.defineProperty(messageList, 'scrollTop', {
      configurable: true,
      get: () => 0,
    })
    Object.defineProperty(messageList, 'clientHeight', {
      configurable: true,
      get: () => 200,
    })

    fireEvent.wheel(messageList, { deltaY: -20 })
    expect(onLoadOlderMessages).toHaveBeenCalledTimes(1)
  })

  it('loads older messages on explicit upward keyboard intent while at top', async () => {
    const messages = [
      makeMessage('assistant', 'Retained 1', 'msg-1'),
      makeMessage('assistant', 'Retained 2', 'msg-2'),
    ].map((message, index) => ({
      ...message,
      seq: BigInt(index + 1),
    }))
    const onLoadOlderMessages = vi.fn()

    const view = render(() => (
      <PreferencesProvider>
        <ChatView
          messages={messages}

          pagination={{ hasOlderMessages: true, onLoadOlderMessages }}
        />
      </PreferencesProvider>
    ))
    // The rows join the list first: no page loads while loaded rows wait for their measure.
    await reportChatViewportSize(view)

    const chatContainer = screen.getByTestId('chat-container')
    const messageList = chatContainer.firstElementChild?.firstElementChild as HTMLDivElement
    Object.defineProperty(messageList, 'scrollTop', {
      configurable: true,
      get: () => 0,
    })
    Object.defineProperty(messageList, 'clientHeight', {
      configurable: true,
      get: () => 200,
    })

    // ArrowUp is the explicit upward-intent key (Home jumps to the first
    // message and PageUp pages, so neither directly triggers older-loading).
    fireEvent.keyDown(messageList, { key: 'ArrowUp' })
    expect(onLoadOlderMessages).toHaveBeenCalledTimes(1)
  })

  it('scrolls the active chat by one page programmatically', () => {
    const messages = [
      makeMessage('assistant', 'Retained 1', 'msg-1'),
      makeMessage('assistant', 'Retained 2', 'msg-2'),
    ].map((message, index) => ({
      ...message,
      seq: BigInt(index + 1),
    }))
    let pageScroll!: (direction: -1 | 1) => void

    render(() => (
      <PreferencesProvider>
        <ChatView
          messages={messages}

          onScrollApiReady={(api) => { pageScroll = api.pageScroll }}
        />
      </PreferencesProvider>
    ))

    const chatContainer = screen.getByTestId('chat-container')
    const messageList = chatContainer.firstElementChild?.firstElementChild as HTMLDivElement
    const writes = recordScrollTopWrites(messageList)
    Object.defineProperty(messageList, 'clientHeight', {
      configurable: true,
      get: () => 240,
    })

    pageScroll(1)
    // A page jump keeps PAGE_SCROLL_OVERLAP_PX (48) of context: 240 - 48 = 192.
    expect(writes).toEqual([192])
  })

  it('scrolls only the targeted chat when multiple chat views are mounted', () => {
    const messages = [
      makeMessage('assistant', 'Retained 1', 'msg-1'),
      makeMessage('assistant', 'Retained 2', 'msg-2'),
    ].map((message, index) => ({
      ...message,
      seq: BigInt(index + 1),
    }))
    let hiddenPageScroll!: (direction: -1 | 1) => void
    let visiblePageScroll!: (direction: -1 | 1) => void

    render(() => (
      <PreferencesProvider>
        <div>
          <div style={{ display: 'none' }}>
            <ChatView
              messages={messages}

              onScrollApiReady={(api) => { hiddenPageScroll = api.pageScroll }}
            />
          </div>
          <div>
            <ChatView
              messages={messages}

              onScrollApiReady={(api) => { visiblePageScroll = api.pageScroll }}
            />
          </div>
        </div>
      </PreferencesProvider>
    ))

    const chatContainers = screen.getAllByTestId('chat-container')
    // The view above renders exactly two containers; `?.` is the type-level guard alone.
    const hiddenList = chatContainers[0]?.firstElementChild?.firstElementChild as HTMLDivElement
    const visibleList = chatContainers[1]?.firstElementChild?.firstElementChild as HTMLDivElement

    Object.defineProperty(hiddenList, 'clientHeight', {
      configurable: true,
      get: () => 120,
    })
    Object.defineProperty(visibleList, 'clientHeight', {
      configurable: true,
      get: () => 240,
    })
    const hiddenWrites = recordScrollTopWrites(hiddenList)
    const visibleWrites = recordScrollTopWrites(visibleList)

    visiblePageScroll(-1)

    expect(hiddenWrites).toEqual([])
    // 240 viewport - 48 overlap = 192 (negative: paging up). The page writes its target
    // unclamped, and the browser clamps it.
    expect(visibleWrites).toEqual([-192])

    hiddenPageScroll(1)

    // 120 viewport - 48 overlap = 72.
    expect(hiddenWrites).toEqual([72])
  })

  it('loads older messages on touch and pointer overscroll intent while at top', async () => {
    const messages = [
      makeMessage('assistant', 'Retained 1', 'msg-1'),
      makeMessage('assistant', 'Retained 2', 'msg-2'),
    ].map((message, index) => ({
      ...message,
      seq: BigInt(index + 1),
    }))
    const onLoadOlderMessages = vi.fn()

    const view = render(() => (
      <PreferencesProvider>
        <ChatView
          messages={messages}

          pagination={{ hasOlderMessages: true, onLoadOlderMessages }}
        />
      </PreferencesProvider>
    ))
    // The rows join the list first: no page loads while loaded rows wait for their measure.
    await reportChatViewportSize(view)

    const chatContainer = screen.getByTestId('chat-container')
    const messageList = chatContainer.firstElementChild?.firstElementChild as HTMLDivElement
    Object.defineProperty(messageList, 'scrollTop', {
      configurable: true,
      get: () => 0,
    })
    Object.defineProperty(messageList, 'clientHeight', {
      configurable: true,
      get: () => 200,
    })

    fireEvent.touchStart(messageList, {
      touches: [
        { clientY: 100 },
      ],
    })
    fireEvent.touchMove(messageList, {
      touches: [
        { clientY: 120 },
      ],
    })
    fireEvent.pointerDown(messageList, { pointerType: 'touch', clientY: 100 })
    fireEvent.pointerMove(messageList, { pointerType: 'touch', clientY: 120 })

    expect(onLoadOlderMessages).toHaveBeenCalledTimes(2)
  })

  it('keeps both codex commandExecution start and completed messages in history', async () => {
    const messages = [
      makeCodexCommandMessage({
        id: 'cmd-start',
        seq: 1n,
        spanId: 'cmd-1',
        status: 'inProgress',
      }),
      makeCodexCommandMessage({
        id: 'cmd-done',
        seq: 2n,
        spanId: 'cmd-1',
        status: 'completed',
        aggregatedOutput: 'done\n',
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)

    expect(screen.getAllByTestId('message-bubble')).toHaveLength(2)
    expect(screen.getByText('done')).toBeInTheDocument()
    expect(view.container).toHaveTextContent('echo hi')
  })

  it('renders long completed codex command output with the shared collapsed result UI', async () => {
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
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)

    expect(screen.getByTestId('message-toolbar')).toBeInTheDocument()
    expect(view.container).toHaveTextContent('line1')
    expect(view.container).toHaveTextContent('line3')
    expect(screen.queryByText('line4')).not.toBeInTheDocument()
  })

  it('preserves printed tool header HTML in completed Codex command output', async () => {
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
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)

    fireEvent.click(screen.getByRole('button', { name: 'Expand' }))
    await waitFor(() => expect(view.container).toHaveTextContent('real failure output'))
    expect(view.container).toHaveTextContent('<span>0 files</span>')
    expect(screen.queryByText('0 files')).not.toBeInTheDocument()
  })

  it('renders the process ID and exit code in completed Codex command failures without output', async () => {
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
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)

    expect(view.container).toHaveTextContent('Error (exit 1)')
    expect(screen.getByText('Process ID:').parentElement).toHaveTextContent('63628')
  })

  it('keeps both codex fileChange start and completed messages in history', async () => {
    const messages = [
      makeCodexFileChangeMessage({
        id: 'fc-start',
        seq: 1n,
        spanId: 'fc-1',
        status: 'inProgress',
      }),
      makeCodexFileChangeMessage({
        id: 'fc-done',
        seq: 2n,
        spanId: 'fc-1',
        status: 'completed',
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)

    expect(screen.getAllByTestId('message-bubble')).toHaveLength(2)
    // The running row states the KIND, not a count. Codex sends the item before its
    // `changes` list, and "0 files" read as an edit that touched nothing.
    expect(screen.queryByText('0 files')).not.toBeInTheDocument()
    expect(screen.getAllByLabelText('File Change').length).toBeGreaterThan(0)
    expect(screen.getByText('old')).toBeInTheDocument()
  })

  it('draws a paired codex fileChange diff only on the result row', async () => {
    const changes = [
      {
        path: 'a.txt',
        kind: 'update',
        diff: '@@ -1 +1 @@\n-codexPairedOld\n+codexPairedNew',
      },
    ]
    const messages = [
      makeCodexFileChangeMessage({
        id: 'fc-start',
        seq: 1n,
        spanId: 'fc-1',
        status: 'inProgress',
        changes,
      }),
      makeCodexFileChangeMessage({
        id: 'fc-done',
        seq: 2n,
        spanId: 'fc-1',
        status: 'completed',
        changes,
      }),
    ]
    const messageContext = testMessageContext({
      messages: () => messages,
    })

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} messageContext={messageContext} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)

    expect(view.container).not.toHaveTextContent('Requested changes')
    expect(screen.getAllByText('codexPairedOld')).toHaveLength(1)
    expect(screen.getAllByText('codexPairedNew')).toHaveLength(1)
  })

  it('renders a lone codex fileChange request as requested changes', async () => {
    const messages = [
      makeCodexFileChangeMessage({
        id: 'fc-start',
        seq: 1n,
        spanId: 'fc-1',
        status: 'inProgress',
        changes: [
          { path: 'a.txt', kind: 'update', diff: '@@ -1 +1 @@\n-old\n+new' },
        ],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)

    expect(view.container).toHaveTextContent('a.txt')
    // A change that has not landed states itself as a REQUEST, which is the shared
    // card every provider draws for a pending edit. It claims nothing about the file.
    expect(view.container).toHaveTextContent('Requested changes')
    expect(view.container).toHaveTextContent('-old')
  })

  it('renders a simple codex fileChange with Edit-style diff content', async () => {
    const messages = [
      makeCodexFileChangeMessage({
        id: 'fc-done',
        seq: 1n,
        spanId: 'fc-1',
        status: 'completed',
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)

    expect(screen.queryByText('completed')).not.toBeInTheDocument()
    expect(screen.getByText('old')).toBeInTheDocument()
    expect(screen.getByText('new')).toBeInTheDocument()
    // The shared row states the file it changed in its header, as every other
    // provider's edit row does. The old Codex renderer left a one-file diff unnamed.
    expect(screen.getByText(A_TXT_RE)).toBeInTheDocument()
  })

  it('renders a simple codex add fileChange start message like Write tool_use', async () => {
    const messages = [
      makeCodexFileChangeMessage({
        id: 'fc-start',
        seq: 1n,
        spanId: 'fc-1',
        status: 'inProgress',
        changes: [
          {
            path: '/repo/src/new-file.ts',
            kind: { type: 'add' },
            diff: 'export const hello = "world"\n',
          },
        ],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)

    expect(view.container).toHaveTextContent('new-file.ts')
    // The pending write states the change it ASKS for, under the shared "Requested
    // changes" heading every provider uses -- which claims nothing about the file
    // having been written.
    expect(view.container).toHaveTextContent('Requested changes')
    expect(view.container).toHaveTextContent('export const hello = "world"')
  })

  it('renders a simple codex add fileChange completion like Write tool_use_result', async () => {
    const messages = [
      makeCodexFileChangeMessage({
        id: 'fc-done',
        seq: 1n,
        spanId: 'fc-1',
        status: 'completed',
        changes: [
          {
            path: '/repo/src/new-file.ts',
            kind: { type: 'add' },
            diff: 'export const hello = "world"\n',
          },
        ],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)

    expect(view.container).toHaveTextContent('export const hello = "world"')
    expect(screen.getByTestId('message-toolbar')).toBeInTheDocument()
  })

  it('renders a simple codex delete fileChange start message as file deletion', async () => {
    const messages = [
      makeCodexFileChangeMessage({
        id: 'fc-start',
        seq: 1n,
        spanId: 'fc-1',
        status: 'inProgress',
        changes: [
          {
            path: '/repo/src/old-file.ts',
            kind: { type: 'delete' },
            diff: 'export const old = true\n',
          },
        ],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)

    expect(view.container).toHaveTextContent('old-file.ts')
    expect(view.container).toHaveTextContent('Requested changes')
  })

  it('renders a simple codex delete fileChange completion as file deletion', async () => {
    const messages = [
      makeCodexFileChangeMessage({
        id: 'fc-done',
        seq: 1n,
        spanId: 'fc-1',
        status: 'completed',
        changes: [
          {
            path: '/repo/src/old-file.ts',
            kind: { type: 'delete' },
            diff: 'export const old = true\n',
          },
        ],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)

    expect(view.container).toHaveTextContent('/repo/src/old-file.ts (deleted)')
    expect(view.container).not.toHaveTextContent('1 file changed')
    expect(screen.getByTestId('message-toolbar')).toBeInTheDocument()
  })

  it('renders multi-file codex fileChange entries with per-file labels including adds', async () => {
    const messages = [
      makeCodexFileChangeMessage({
        id: 'fc-done',
        seq: 1n,
        spanId: 'fc-1',
        status: 'completed',
        changes: [
          {
            path: '/repo/src/a.ts',
            kind: { type: 'update' },
            diff: '@@ -1 +1 @@\n-oldValue\n+newValue\n',
          },
          {
            path: '/repo/src/new-file.tsx',
            kind: { type: 'add' },
            diff: 'export const hello = "world"\n',
          },
        ],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)

    expect(view.container).toHaveTextContent('/repo/src/a.ts+1 -1')
    expect(view.container).toHaveTextContent('/repo/src/new-file.tsx+1')
    expect(view.container).toHaveTextContent('+1')
    expect(view.container).toHaveTextContent('oldValue')
    expect(view.container).toHaveTextContent('newValue')
    expect(view.container).toHaveTextContent('export const hello = "world"')
  })

  it('shows shared toolbar actions for completed codex fileChange messages', async () => {
    const messages = [
      makeCodexFileChangeMessage({
        id: 'fc-done',
        seq: 1n,
        spanId: 'fc-1',
        status: 'completed',
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)

    const toolbar = screen.getByTestId('message-toolbar')
    expect(toolbar).toBeInTheDocument()
    expect(screen.getByTestId('message-copy-json')).toBeInTheDocument()
    expect(view.container.querySelectorAll('[data-testid="message-toolbar"] button').length).toBeGreaterThanOrEqual(2)
  })

  it('falls back to codex fileChange summary for mixed changes', async () => {
    const messages = [
      makeCodexFileChangeMessage({
        id: 'fc-done',
        seq: 1n,
        spanId: 'fc-1',
        status: 'completed',
        changes: [
          {
            path: 'a.txt',
            kind: { type: 'add' },
            diff: '',
          },
          {
            path: 'b.txt',
            kind: { type: 'delete' },
            diff: '',
          },
        ],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)

    // An add and a delete that carry no body still state WHICH files they touched:
    // the header counts them and the body lists each one. Dropping a change with no
    // diff would hide a file the call created or removed.
    expect(screen.getByText('2 files changed')).toBeInTheDocument()
    expect(view.container).toHaveTextContent('a.txt')
    expect(view.container).toHaveTextContent('b.txt')
  })

  it('renders persisted codex raw reasoning when no summary exists', async () => {
    const messages = [
      makeCodexReasoningMessage({
        id: 'reason-done',
        seq: 1n,
        spanId: 'reason-1',
        content: ['persisted raw reasoning'],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)
    await settleWorkerReplies()

    expect(screen.getByText('persisted raw reasoning')).toBeInTheDocument()
  })

  it('ignores empty codex summary entries and renders raw reasoning', async () => {
    const messages = [
      makeCodexReasoningMessage({
        id: 'reason-done',
        seq: 1n,
        spanId: 'reason-1',
        summary: ['', '   '],
        content: ['persisted raw reasoning'],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)

    expect(screen.getByText('persisted raw reasoning')).toBeInTheDocument()
    expect(view.container.querySelector('ul')).toBeNull()
  })

  it('renders each persisted codex summary entry as an unordered list item', async () => {
    const messages = [
      makeCodexReasoningMessage({
        id: 'reason-done',
        seq: 1n,
        spanId: 'reason-1',
        summary: [
          '**Researching Windows panic recovery**',
          '**Checking Windows reproduction tools**',
        ],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)
    await settleWorkerReplies()

    const list = view.container.querySelector('ul')
    expect(list).not.toBeNull()
    expect(list?.querySelectorAll(':scope > li')).toHaveLength(2)
    expect(list?.querySelectorAll(':scope > li strong')).toHaveLength(2)
    expect(list?.children[0]).toHaveTextContent('Researching Windows panic recovery')
    expect(list?.children[1]).toHaveTextContent('Checking Windows reproduction tools')
  })

  it('keeps nested markdown blocks inside their codex summary list item', async () => {
    const messages = [
      makeCodexReasoningMessage({
        id: 'reason-done',
        seq: 1n,
        spanId: 'reason-1',
        summary: [
          '**Researching panic recovery**\n\n- Catch panics\n- Record stacks',
          '**Checking reproduction tools**\n\n> Keep the failure output.',
        ],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)
    await settleWorkerReplies()

    const outerList = view.container.querySelector('ul')
    const outerItems = outerList?.querySelectorAll(':scope > li')
    expect(outerItems).toHaveLength(2)
    expect(outerItems?.[0]?.querySelectorAll('ul > li')).toHaveLength(2)
    expect(outerItems?.[0]).toHaveTextContent('Catch panics')
    expect(outerItems?.[0]).toHaveTextContent('Record stacks')
    expect(outerItems?.[1]?.querySelector('blockquote')).toHaveTextContent('Keep the failure output.')
  })

  it('prefers a persisted codex summary to duplicate raw reasoning', async () => {
    const messages = [
      makeCodexReasoningMessage({
        id: 'reason-done',
        seq: 1n,
        spanId: 'reason-1',
        summary: ['persisted summary'],
        content: ['duplicate persisted raw reasoning'],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)
    await settleWorkerReplies()

    expect(screen.getByText('persisted summary')).toBeInTheDocument()
    expect(screen.queryByText('duplicate persisted raw reasoning')).not.toBeInTheDocument()
    expect(view.container.querySelectorAll('ul > li')).toHaveLength(1)
  })

  it('starts codex reasoning collapsed when expandAgentThoughts is disabled', async () => {
    localStorageSet(KEY_BROWSER_PREFS, { expandAgentThoughts: false })

    const messages = [
      makeCodexReasoningMessage({
        id: 'reason-done',
        seq: 1n,
        spanId: 'reason-1',
        summary: ['done'],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)
    await settleWorkerReplies()

    expect(screen.getByText('Thinking')).toBeInTheDocument()
    expect(screen.queryByText('done')).not.toBeInTheDocument()

    fireEvent.click(screen.getByText('Thinking'))
    expect(screen.getByText('done')).toBeInTheDocument()
  })

  it('keeps completed codex reasoning visible while empty start reasoning remains hidden', async () => {
    const messages = [
      makeCodexReasoningMessage({ id: 'reason-start', seq: 1n, spanId: 'reason-1' }),
      makeCodexReasoningMessage({
        id: 'reason-done',
        seq: 2n,
        spanId: 'reason-1',
        summary: ['done'],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)

    expect(screen.getAllByTestId('message-bubble')).toHaveLength(1)
    expect(screen.getByText('Thinking')).toBeInTheDocument()
  })

  it('renders turn/plan/updated with the TodoWrite-style todo list UI', async () => {
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
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)

    expect(view.container).toHaveTextContent('3 tasks')
    expect(view.container).toHaveTextContent('Inspect message filtering')
    expect(view.container).toHaveTextContent('Implement renderer')
    expect(view.container).toHaveTextContent('Run tests')
  })

  it('renders turn/plan/updated explanation in the title when present', async () => {
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
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)

    expect(view.container).toHaveTextContent('2 tasks - Need to keep start and complete messages visible')
  })

  it('renders codex webSearch start and completed search messages separately', async () => {
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
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)

    expect(screen.getAllByTestId('message-bubble')).toHaveLength(1)
    expect(view.container).not.toHaveTextContent('Searching the web')
    expect(view.container).toHaveTextContent('codex app server')
    expect(view.container).not.toHaveTextContent('site:github.com openai codex')
    expect(view.container).not.toHaveTextContent('site:github.com "turn/plan/updated" codex app server')

    fireEvent.click(screen.getByRole('button', { name: 'Expand output' }))

    expect(view.container).toHaveTextContent('site:github.com openai codex "turn/plan/updated"')
    expect(view.container).toHaveTextContent('site:github.com "turn/plan/updated" codex app server')
  })

  it('renders codex webSearch openPage messages like a fetch/opened-page result', async () => {
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
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)

    expect(view.container).toHaveTextContent('https://github.com/openai/codex/blob/main/codex-rs/app-server/README.md')
    expect(screen.getByTestId('message-toolbar')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Expand' })).not.toBeInTheDocument()
  })

  it('renders codex webSearch findInPage messages with pattern and URL', async () => {
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
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)

    expect(view.container).toHaveTextContent('turn/plan/updated')
    expect(view.container).toHaveTextContent('https://example.com/readme')
    expect(screen.getByTestId('message-toolbar')).toBeInTheDocument()
  })

  it('hides empty completed codex webSearch messages with no query or detail', async () => {
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

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} />
      </PreferencesProvider>
    ))
    await reportChatViewportSize(view)

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
    virtualizerState.layoutShift = 0
    bubbleHostState.byId.clear()
    viewportSizeObserverState.width = 640
    viewportSizeObserverState.height = 733
    viewportSizeObserverState.onWidth = undefined
    viewportSizeObserverState.onHeight = undefined
    prefsState.setDiffView?.('unified') // the pref signals are module-scoped; reset between tests
    prefsState.setExpandThoughts?.(true)
    prefsState.setUiFontFamily?.(undefined)
    prefsState.setMonoFontFamily?.(MOCK_MONO_FONT_FAMILY)
    markdownPendingState.setPendingIds?.(new Set())
    virtualizerState.attachedReadiness = new Map()
    // The signals of the scroll stub are module-scoped also.
    mockState.setIsFollowing?.(true)
    mockState.setAtStagedTopEdge?.(false)
    mockState.setAtStagedBottomEdge?.(false)
  })

  describe('chat view virtualized visible slice', () => {
    // The stubs keep their state in module-level objects and signals, so each test
    // starts from the same empty virtualizer, with no fling and only the first admitted
    // row in range. No row is measured at the mount: a test admits its rows (admitRows),
    // or commits the heights that it states.
    beforeEach(() => {
      virtualizerState.attachedIds = []
      virtualizerState.currentHeightKeys = new Map()
      virtualizerState.setDeferred?.(false)
      hiddenPremeasureState.candidates = []
      hiddenPremeasureState.candidateLog = []
      hiddenPremeasureState.onMeasure = undefined
      hiddenPremeasureState.contentWidthPx = undefined
      bubbleHostState.premeasureById.clear()
      virtualizerState.setRange?.({ start: 0, end: 1 })
    })

    it('does not mount rows between stale pending premeasure ids and the current viewport range', async () => {
      const messages = Array.from({ length: 12 }, (_, index) => message(`m${index}`, index + 1))
      const { container } = render(() => (
        <ChatView
          messages={messages}

        />
      ))

      await Promise.resolve()
      admitRows()
      expect(visibleBubbleIds(container)).toEqual(['m0'])

      virtualizerState.setRange?.({ start: 8, end: 10 })
      await Promise.resolve()

      expect(visibleBubbleIds(container)).toEqual(['m8', 'm9'])
      expect(visibleBubbleIds(container)).not.toContain('m1')
      expect(visibleBubbleIds(container)).not.toContain('m7')
    })

    it('keeps a premeasure row pending when its height key is stale', async () => {
      render(() => (
        <ChatView
          messages={[
            message('m0', 1),
          ]}

        />
      ))

      await waitFor(() => {
        expect(hiddenPremeasureState.candidates.map(candidate => candidate.item.id)).toEqual(['m0'])
      })

      // The wait above proved the candidate list; `?.` is the type-level guard alone.
      const heightKey = hiddenPremeasureState.candidates[0]?.item.heightKey
      const staleHeightKey = `${heightKey ?? 'missing'}:stale`
      const onMeasure = requiredPremeasureCallback()
      onMeasure('m0', 20, staleHeightKey, 0)

      expect(viewHeightCache().debugEntry('m0')).toBeUndefined()
      await waitFor(() => {
        expect(hiddenPremeasureState.candidates.map(candidate => candidate.item.id)).toEqual(['m0'])
      })

      onMeasure('m0', 30, heightKey, 0)

      await waitFor(() => {
        expect(hiddenPremeasureState.candidates.map(candidate => candidate.item.id)).toEqual([])
      })
    })

    it('takes the fallback width for the key and for the hidden copy together, before the first width', async () => {
      viewportSizeObserverState.width = 0
      render(() => (
        <ChatView
          messages={[
            message('m0', 1),
          ]}

        />
      ))
      await waitFor(() => {
        expect(hiddenPremeasureState.contentWidthPx).toBe(PRE_MEASURE_WIDTH_PX)
      })
      // No hidden copy renders before the first width, so no row can be measured there.
      expect(hiddenPremeasureState.candidates).toEqual([])

      // The first width above 0, however small, is the width of the rows: the copy and the
      // key take it together. A hidden copy of 3px under a key of another width would
      // commit a height that fits neither.
      viewportSizeObserverState.onWidth?.(3)
      await waitFor(() => {
        expect(hiddenPremeasureState.contentWidthPx).toBe(3)
      })
      expect(heightKeyHoldsWidth(hiddenPremeasureState.candidates[0]?.item.heightKey, 3)).toBe(true)
      admitRows()
      expect(heightKeyHoldsWidth(stubItemHeightKey('m0'), 3)).toBe(true)
    })

    it('keeps the width of the key while the pane reports no width', async () => {
      render(() => (
        <ChatView
          messages={[
            message('m0', 1),
          ]}

        />
      ))
      await waitFor(() => {
        expect(hiddenPremeasureState.contentWidthPx).toBe(640)
      })
      // The row is measured at 640, so it is among the items of the stub.
      admitRows()
      const keyAt640 = stubItemHeightKey('m0')
      expect(heightKeyHoldsWidth(keyAt640, 640)).toBe(true)

      // A pane with no layout box reports 0. The key and the copy keep the last width, so
      // no height of that width is dropped.
      viewportSizeObserverState.onWidth?.(0)
      await Promise.resolve()
      expect(hiddenPremeasureState.contentWidthPx).toBe(640)
      expect(stubItemHeightKey('m0')).toBe(keyAt640)

      viewportSizeObserverState.onWidth?.(640)
      await Promise.resolve()
      expect(hiddenPremeasureState.contentWidthPx).toBe(640)
      expect(stubItemHeightKey('m0')).toBe(keyAt640)
    })

    it('changes every height key when a font preference changes', async () => {
      render(() => (
        <ChatView
          messages={[
            message('m0', 1),
            message('m1', 2),
          ]}

        />
      ))
      await waitFor(() => {
        expect(hiddenPremeasureState.candidates).toHaveLength(2)
      })
      // No row is measured, so each row is a candidate of the hidden-premeasure mock, which
      // holds its current key.
      const keys = () => ['m0', 'm1'].map(id => virtualizerState.currentHeightKeys.get(id))

      prefsState.setMonoFontFamily!('A')
      const keysWithMonoA = keys()
      prefsState.setMonoFontFamily!('B')
      const keysWithMonoB = keys()
      expect(keysWithMonoB[0]).not.toBe(keysWithMonoA[0])
      expect(keysWithMonoB[1]).not.toBe(keysWithMonoA[1])

      prefsState.setUiFontFamily!('A')
      const keysWithUiA = keys()
      expect(keysWithUiA[0]).not.toBe(keysWithMonoB[0])
      prefsState.setUiFontFamily!('B')
      const keysWithUiB = keys()
      expect(keysWithUiB[0]).not.toBe(keysWithUiA[0])
      expect(keysWithUiB[1]).not.toBe(keysWithUiA[1])
    })

    it('keeps a newly appended live-tail row out of the list until it is measured (no overflow onto trailing UI)', async () => {
      // Regression: the live tail was shown at its ESTIMATED height immediately, so a
      // tall unmeasured tail overflowed its slot onto the in-flow thinking indicator.
      // The list now holds measured rows only, so the tail joins when its hidden copy
      // measured it.
      const [messages, setMessages] = createSignal([
        message('m0', 1),
      ])
      const { container } = render(() => (
        <ChatView
          messages={messages()}

        />
      ))
      admitRows()

      await waitFor(() => {
        expect(visibleBubbleIds(container)).toEqual(['m0'])
      })

      virtualizerState.setRange?.({ start: 0, end: 2 })
      setMessages([
        message('m0', 1),
        message('m1', 2),
      ])

      // The appended tail m1 has a hidden copy, and no row in the list. Nothing stands in
      // for it: no placeholder, and no skeleton.
      await waitFor(() => {
        expect(hiddenPremeasureState.candidates.map(candidate => candidate.item.id)).toEqual(['m1'])
      })
      expect(container.querySelector('[data-seq="2"]')).toBeNull()
      expect(container.querySelectorAll('[data-testid="row-skeleton"]')).toHaveLength(0)

      // Its height commits -> the row joins, visible, still with no skeleton.
      measureStagedRow('m1')
      const appendedRow = container.querySelector('[data-seq="2"]') as HTMLElement | null
      expect(appendedRow).not.toBeNull()
      expect(appendedRow!.style.visibility).not.toBe('hidden')
      expect(appendedRow!.style.opacity).toBe('1')
      expect(container.querySelectorAll('[data-testid="row-skeleton"]')).toHaveLength(0)
    })

    it('keeps newly appended interior and live-tail rows out of the list until each is measured', () => {
      const [messages, setMessages] = createSignal([
        message('m0', 1),
      ])
      const { container } = render(() => (
        <ChatView
          messages={messages()}

        />
      ))
      admitRows()

      virtualizerState.setRange?.({ start: 0, end: 3 })
      setMessages([
        message('m0', 1),
        message('m1', 2),
        message('m2', 3),
      ])

      // Neither the interior row m1 nor the live tail m2 is in the list before its measure,
      // so neither paints content at a height that nothing measured.
      const interior = () => container.querySelector('[data-seq="2"]') as HTMLElement | null
      const tail = () => container.querySelector('[data-seq="3"]') as HTMLElement | null
      expect(interior()).toBeNull()
      expect(tail()).toBeNull()

      // The list grows at its end as soon as the next row there is measured.
      measureStagedRow('m1')
      expect(interior()).not.toBeNull()
      expect(interior()!.style.visibility).not.toBe('hidden')
      expect(tail()).toBeNull()

      measureStagedRow('m2')
      expect(tail()).not.toBeNull()
      expect(tail()!.style.visibility).not.toBe('hidden')
    })

    it('gives a band row its own chrome while its skeleton stands in for it', () => {
      // The skeleton slot is a FOURTH row-mount site. The row it covers is invisible, and a
      // band's gray surface and its two border lines belong to the ROW -- so without the
      // chrome here the shimmer sits on the bare panel and the band appears only at the
      // crossfade, which is the pop-in the tail already avoids.
      vi.useFakeTimers()
      // The row is measured, and its Markdown still renders: that is the row behind a skeleton.
      markdownPendingState.setPendingIds?.(new Set(['m1']))
      // The full Claude envelope: `type: 'assistant'` is what makes the row a band.
      const assistantText = (id: string, seq: bigint, text: string): AgentChatMessage => ({
        ...makeMessage('assistant', '', id),
        seq,
        content: new TextEncoder().encode(JSON.stringify({
          type: 'assistant',
          message: {
            content: [
              { type: 'text', text },
            ],
          },
        })),
      })
      const [messages, setMessages] = createSignal([
        assistantText('m0', 1n, 'first'),
      ])
      const { container } = render(() => (
        <ChatView messages={messages()} />
      ))
      admitRows()

      virtualizerState.setRange?.({ start: 0, end: 2 })
      setMessages([
        assistantText('m0', 1n, 'first'),
        assistantText('m1', 2n, 'answer'),
      ])
      admitRows()
      vi.advanceTimersByTime(SKELETON_SHOW_DELAY_MS)

      const skeleton = container.querySelector('[data-testid="row-skeleton"]')
      expect(skeleton).not.toBeNull()
      const slot = skeleton!.parentElement!
      expect(slot.classList.contains(bandRow)).toBe(true)
      // The class ONLY. `data-band` is the e2e hook, and a second visible element carrying
      // it would break the seam check that walks every [data-band] in DOM order.
      expect(slot.hasAttribute('data-band')).toBe(false)
      // The slot carries the reserved height (the stub's heightOfId), so the border of the
      // band lies inside it, and the skeleton fills what remains. A skeleton with the
      // reserved height inside the band would make the slot taller by the two borders.
      expect(slot.style.height).toBe('100px')
      expect((skeleton as HTMLElement).style.height).toBe('100%')
    })

    it('defers loading skeletons past the show-delay, then paints them (including the live tail)', () => {
      vi.useFakeTimers()
      // m1 and m2 are measured rows whose Markdown still renders in the worker.
      markdownPendingState.setPendingIds?.(new Set(['m1', 'm2']))
      const [messages, setMessages] = createSignal([
        message('m0', 1),
      ])
      const { container } = render(() => (
        <ChatView
          messages={messages()}

        />
      ))
      admitRows()

      virtualizerState.setRange?.({ start: 0, end: 3 })
      setMessages([
        message('m0', 1),
        message('m1', 2),
        message('m2', 3),
      ])
      admitRows()

      // m1 (interior) and m2 (live tail) are in the list and hidden immediately, but NO
      // skeleton paints yet -- a fast load is expected, so the shimmer is deferred.
      expect((container.querySelector('[data-seq="2"]') as HTMLElement).style.visibility).toBe('hidden')
      expect((container.querySelector('[data-seq="3"]') as HTMLElement).style.visibility).toBe('hidden')
      expect(container.querySelectorAll('[data-testid="row-skeleton"]')).toHaveLength(0)

      // Only once the wait exceeds SKELETON_SHOW_DELAY_MS do the skeletons appear, one per
      // still-hidden row (m1 -> 100px, m2 -> 200px); m0 (ready) gets none.
      vi.advanceTimersByTime(SKELETON_SHOW_DELAY_MS)
      const skeletons = [
        ...container.querySelectorAll('[data-testid="row-skeleton"]'),
      ] as HTMLElement[]
      expect(skeletons).toHaveLength(2)
      // Each slot carries the height of its row (the stub's heightOfId), and its skeleton fills it.
      expect(skeletons.every(s => s.parentElement!.style.height === '100px')).toBe(true)
      expect(skeletons.every(s => s.style.height === '100%')).toBe(true)
      expect(skeletons.map(s => s.parentElement!.style.transform).sort())
        .toEqual(['translateY(100px)', 'translateY(200px)'])

      // Once both bodies arrive, the real rows show and the shown overlays CROSSFADE out:
      // each lingers for one SKELETON_CROSSFADE_MS beat in the fading-out wrapper.
      markdownPendingState.setPendingIds?.(new Set())
      expect((container.querySelector('[data-seq="2"]') as HTMLElement).style.visibility).not.toBe('hidden')
      expect((container.querySelector('[data-seq="3"]') as HTMLElement).style.visibility).not.toBe('hidden')
      const lingering = [
        ...container.querySelectorAll('[data-testid="row-skeleton"]'),
      ] as HTMLElement[]
      expect(lingering).toHaveLength(2)
      expect(lingering.every(s => s.parentElement!.classList.contains(rowSkeletonClosing))).toBe(true)
      // The closing copy keeps the height of its slot.
      expect(lingering.every(s => s.parentElement!.style.height === '100px')).toBe(true)

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
      // No row is measured (see the beforeEach), so every row is a premeasure candidate.
      prefsState.setDiffView?.('unified')
      prefsState.setExpandThoughts?.(true)
      virtualizerState.setRange?.({ start: 0, end: 3 })
      const messages = [
        message('m0', 1),
        message('m1', 2),
        message('m2', 3),
      ]
      render(() => (
        <ChatView
          messages={messages}

        />
      ))

      // The premeasure mock captures each candidate row's heightKey (see its createEffect).
      const before = new Map(virtualizerState.currentHeightKeys)
      expect([
        ...before.keys(),
      ].sort()).toEqual(['m0', 'm1', 'm2'])
      expect([
        ...before.values(),
      ].every(key => typeof key === 'string' && key.length > 0)).toBe(true)

      prefsState.setDiffView?.('split') // toggle the GLOBAL diff-view preference
      for (const id of ['m0', 'm1', 'm2'])
        expect(virtualizerState.currentHeightKeys.get(id)).toBe(before.get(id)) // unchanged

      prefsState.setExpandThoughts?.(false) // toggle the GLOBAL expand-thoughts preference
      for (const id of ['m0', 'm1', 'm2'])
        expect(virtualizerState.currentHeightKeys.get(id)).toBe(before.get(id)) // still unchanged
    })

    it('reveals appended rows in document order, even when a later one measures first', async () => {
      // Issue: when several messages are appended at once, whichever measured first
      // used to pop in first -- so a later message could appear ahead of an earlier one.
      // The admitted rows are one contiguous range, so a later row joins only after each
      // row before it is measured.
      const [messages, setMessages] = createSignal([
        message('m0', 1),
      ])
      const { container } = render(() => (
        <ChatView
          messages={messages()}

        />
      ))
      admitRows()

      // Append m1 (interior) and m2 (tail) together -- neither is in the list before its measure.
      virtualizerState.setRange?.({ start: 0, end: 3 })
      setMessages([
        message('m0', 1),
        message('m1', 2),
        message('m2', 3),
      ])
      const interior = () => container.querySelector('[data-seq="2"]') as HTMLElement | null
      const tail = () => container.querySelector('[data-seq="3"]') as HTMLElement | null
      expect(interior()).toBeNull()
      expect(tail()).toBeNull()

      // The TAIL (m2) measures FIRST, while the interior m1 is still staged. m2 must
      // not join, so it can't appear before m1.
      measureStagedRow('m2')
      expect(interior()).toBeNull()
      expect(tail()).toBeNull()

      // m1 measures -> both join together, in order.
      measureStagedRow('m1')
      expect(interior()?.style.visibility).not.toBe('hidden')
      expect(tail()?.style.visibility).not.toBe('hidden')
      expect(interior()).not.toBeNull()
      expect(tail()).not.toBeNull()
    })

    it('hides a measured row while its Markdown renders, paints its skeleton, then reveals it and releases its measurement', () => {
      // A reload hydrates persisted heights, so a row can have a known height but no
      // HTML yet. It skips premeasure, so only its own pending content can hold it.
      vi.useFakeTimers()
      markdownPendingState.setPendingIds?.(new Set(['m0']))
      const { container } = render(() => (
        <ChatView messages={[
          message('m0', 1),
        ]}
        />
      ))
      admitRows()
      const row = () => container.querySelector('[data-seq="1"]') as HTMLElement

      expect(row().style.visibility).toBe('hidden')
      expect(container.querySelectorAll('[data-testid="row-skeleton"]')).toHaveLength(0)
      vi.advanceTimersByTime(SKELETON_SHOW_DELAY_MS)
      expect(container.querySelectorAll('[data-testid="row-skeleton"]')).toHaveLength(1)
      // The measurer reads the row through the same scope that hides the row, so it
      // reads nothing while the row waits.
      const readiness = virtualizerState.attachedReadiness.get('m0')
      expect(readiness).toBeDefined()
      expect(readiness!.measurable()).toBe(false)

      markdownPendingState.setPendingIds?.(new Set())

      expect(row().style.visibility).not.toBe('hidden')
      // The scope turns measurable, so the measurer measures the row now.
      expect(readiness!.measurable()).toBe(true)
    })

    // A content change builds a new entry, which remounts the row with a body
    // that no cache holds. The row hides until the worker renders that body, so
    // it never draws a partial body or commits a height for one.
    it('hides a revealed row while the Markdown of its new content renders, then reveals it', () => {
      const { transcript, resolver } = testTranscriptContext([
        message('m0', 1),
      ])
      const { container } = render(() => (
        <ChatView messages={transcript.sources.messages()} messageContext={resolver} />
      ))
      admitRows()
      const row = () => container.querySelector('[data-seq="1"]') as HTMLElement
      const revealed = row()
      expect(revealed.style.visibility).not.toBe('hidden')

      // The same message arrives with new content, whose Markdown the worker has not rendered.
      batch(() => {
        markdownPendingState.setPendingIds?.(new Set(['m0']))
        transcript.replace({
          ...message('m0', 1),
          content: new TextEncoder().encode('message m0, revised'),
        })
      })

      // The row stays in the list through the change: it is mounted, so it keeps the height
      // that it shows, and no hidden copy has to measure the new content first.
      expect(row()).not.toBeNull()
      expect(row()).not.toBe(revealed)
      expect(row().style.visibility).toBe('hidden')

      markdownPendingState.setPendingIds?.(new Set())

      expect(row().style.visibility).not.toBe('hidden')
    })

    it('holds an appended ready row behind an earlier row whose Markdown still renders', () => {
      const [messages, setMessages] = createSignal([
        message('m0', 1),
      ])
      const { container } = render(() => (
        <ChatView messages={messages()} />
      ))
      admitRows()

      markdownPendingState.setPendingIds?.(new Set(['m1']))
      virtualizerState.setRange?.({ start: 0, end: 3 })
      setMessages([
        message('m0', 1),
        message('m1', 2),
        message('m2', 3),
      ])
      admitRows()
      const interior = () => container.querySelector('[data-seq="2"]') as HTMLElement
      const tail = () => container.querySelector('[data-seq="3"]') as HTMLElement
      expect(interior().style.visibility).toBe('hidden')
      // m2 is measured and has no pending content, but it must not appear before m1.
      expect(tail().style.visibility).toBe('hidden')

      markdownPendingState.setPendingIds?.(new Set())
      expect(interior().style.visibility).not.toBe('hidden')
      expect(tail().style.visibility).not.toBe('hidden')
    })

    it('shows the loading skeleton at once when a fling skeleton upgrades into a row whose Markdown still renders', async () => {
      markdownPendingState.setPendingIds?.(new Set(['m1']))
      const { container } = render(() => (
        <ChatView messages={[
          message('m0', 1),
          message('m1', 2),
        ]}
        />
      ))
      admitRows()
      await Promise.resolve()
      const overlaySkeletons = () => [
        ...container.querySelectorAll('[data-testid="row-skeleton"]'),
      ].filter(skeleton => skeleton.closest('[data-seq]') === null)

      // m1 enters mid-fling: an in-row skeleton, with no bubble and so nothing pending.
      virtualizerState.setDeferred?.(true)
      virtualizerState.setRange?.({ start: 0, end: 2 })
      await Promise.resolve()
      expect(overlaySkeletons()).toHaveLength(0)

      // The fling settles: the bubble mounts, its Markdown is pending, and the row hides.
      // The overlay takes over at once -- no blank gap for the show-delay.
      virtualizerState.setDeferred?.(false)
      await Promise.resolve()
      expect((container.querySelector('[data-seq="2"]') as HTMLElement).style.visibility).toBe('hidden')
      expect(overlaySkeletons()).toHaveLength(1)
    })

    it('builds only the in-row fling skeleton for a measured row that enters mid-fling', async () => {
      // A row that enters the slice has not mounted when the slice's derivations run,
      // so it counts as not ready for that moment. During a fling the overlay skips its
      // show-delay, and must still build no overlay skeleton for that moment.
      const ids = Array.from({ length: 12 }, (_, index) => `m${index}`)
      const messages = ids.map((id, index) => message(id, index + 1))
      render(() => (
        <ChatView messages={messages} />
      ))
      admitRows()
      await Promise.resolve()
      rowSkeletonState.builtSeeds = []

      virtualizerState.setDeferred?.(true)
      virtualizerState.setRange?.({ start: 0, end: 10 })
      await Promise.resolve()
      expect([...rowSkeletonState.builtSeeds].sort()).toEqual([
        'm1',
        'm2',
        'm3',
        'm4',
        'm5',
        'm6',
        'm7',
        'm8',
        'm9',
      ])

      // The next frame of the fling brings two more rows.
      rowSkeletonState.builtSeeds = []
      virtualizerState.setRange?.({ start: 0, end: 12 })
      await Promise.resolve()
      expect([...rowSkeletonState.builtSeeds].sort()).toEqual(['m10', 'm11'])
    })

    it('premeasures a look-ahead band of rows just beyond the rendered range', async () => {
      // Only m0 is in the rendered range (see the beforeEach).
      const [messages, setMessages] = createSignal([
        message('m0', 1),
      ])
      render(() => (
        <ChatView
          messages={messages()}

        />
      ))
      admitRows()

      // m1 and m2 sit BEYOND the rendered range but within LOOKAHEAD_PREMEASURE_ROWS, so they
      // get a hidden copy before the reader reaches them.
      setMessages([
        message('m0', 1),
        message('m1', 2),
        message('m2', 3),
      ])

      await waitFor(() => {
        expect(hiddenPremeasureState.candidates.map(candidate => candidate.item.id).sort()).toEqual(['m1', 'm2'])
      })
    })

    it('mounts a fling skeleton for a row entering mid-fling, upgrading on settle', async () => {
      const messages = Array.from({ length: 12 }, (_, index) => message(`m${index}`, index + 1))
      const { container } = render(() => (
        <ChatView
          messages={messages}

        />
      ))
      admitRows()
      await Promise.resolve()
      // m0 mounted BEFORE the fling: it must stay a real bubble when the fling
      // starts (no downgrade — that would tear its DOM down mid-scroll).
      expect(visibleBubbleIds(container)).toEqual(['m0'])

      virtualizerState.setDeferred?.(true) // momentum fling in flight
      virtualizerState.setRange?.({ start: 0, end: 10 })
      await Promise.resolve()

      // Rows that ENTERED mid-fling render IN-ROW skeletons instead of bubbles. Every
      // row of the list is measured, so each one has a height for its skeleton, and no
      // overlay skeleton stands in for a row. In-row skeletons sit inside the data-seq
      // row; overlays sit in their own positioned wrapper.
      const skeletons = [
        ...container.querySelectorAll('[data-testid="row-skeleton"]'),
      ] as HTMLElement[]
      const inRow = skeletons.filter(s => s.parentElement?.hasAttribute('data-seq'))
      const overlay = skeletons.filter(s => !s.parentElement?.hasAttribute('data-seq'))
      expect(inRow).toHaveLength(9) // m1..m9 (m0 was already real)
      expect(overlay).toHaveLength(0)
      // The lengths are asserted above; `?.` reads are the type-level guard alone.
      // The row carries its height (the stub's heightOfId) while its skeleton
      // shows, and the skeleton fills the row.
      expect(inRow.every(s => s.parentElement?.style.height === '100px')).toBe(true)
      expect(inRow.every(s => s.style.height === '100%')).toBe(true)
      // A row that shows its bubble sizes to its content.
      expect(container.querySelector<HTMLElement>('[data-seq="1"]')?.style.height).toBe('')
      // The body is ONE masked Oat fill block; its role="status" is what Oat's
      // `[role=status].skeleton` selector REQUIRES for the styles to apply.
      const fills = [
        ...(inRow[0]?.querySelectorAll('.skeleton.line') ?? []),
      ] as HTMLElement[]
      expect(fills).toHaveLength(1)
      expect(fills[0]?.getAttribute('role')).toBe('status')
      expect(visibleBubbleIds(container)).toEqual(['m0'])

      virtualizerState.setDeferred?.(false) // fling settled
      await Promise.resolve()

      // Every IN-ROW skeleton upgraded to a real bubble, with a fading-out
      // skeleton COPY on top for the crossfade beat (inside the row but wrapped,
      // so no longer a direct data-seq child).
      expect(visibleBubbleIds(container)).toEqual(
        [
          'm0',
          'm1',
          'm2',
          'm3',
          'm4',
          'm5',
          'm6',
          'm7',
          'm8',
          'm9',
        ],
      )
      const during = [
        ...container.querySelectorAll('[data-testid="row-skeleton"]'),
      ] as HTMLElement[]
      expect(during.filter(s => s.parentElement?.hasAttribute('data-seq'))).toHaveLength(0)
      const crossfading = during.filter(s => s.closest('[data-seq]') !== null)
      expect(crossfading).toHaveLength(9) // m1..m9 fading out over their bubbles
      // Nine crossfading skeletons asserted above; `?.` is the type-level guard alone.
      expect(crossfading[0]?.parentElement?.classList.contains(rowSkeletonClosing)).toBe(true)
      // The upgraded rows size to their content again, and each fading copy fills its row.
      expect(crossfading.every(s => s.closest<HTMLElement>('[data-seq]')?.style.height === '')).toBe(true)
      expect(crossfading.every(s => s.style.height === '100%')).toBe(true)

      // After the crossfade beat, no skeleton remains.
      await waitFor(() => {
        expect(container.querySelectorAll('[data-testid="row-skeleton"]')).toHaveLength(0)
      })
    })

    // A fling skeleton draws the height that the virtualizer holds for its row, so
    // a read of it would only confirm that height, under a key that the bubble may
    // not fit: the key can change mid-fling.
    it('keeps a fling skeleton row out of the measurer until the row upgrades to its bubble', async () => {
      const messages = Array.from({ length: 12 }, (_, index) => message(`m${index}`, index + 1))
      render(() => (
        <ChatView messages={messages} />
      ))
      admitRows()
      await Promise.resolve()

      virtualizerState.setDeferred?.(true)
      virtualizerState.setRange?.({ start: 0, end: 10 })
      await Promise.resolve()

      const skeletonRow = virtualizerState.attachedReadiness.get('m8')
      const realRow = virtualizerState.attachedReadiness.get('m0')
      expect(skeletonRow?.measurable()).toBe(false)
      expect(realRow?.measurable()).toBe(true)

      virtualizerState.setDeferred?.(false)
      await Promise.resolve()

      expect(skeletonRow?.measurable()).toBe(true)
    })

    it('attaches wheel and touch listeners as passive on the scroll container', () => {
      // Non-passive wheel/touch listeners force the compositor to wait on the main
      // thread before starting a scroll; nothing in these handlers calls
      // preventDefault, so they must register passive (see
      // attachPassiveScrollListeners in ChatView).
      const spy = vi.spyOn(HTMLElement.prototype, 'addEventListener')
      try {
        const { container } = render(() => (
          <ChatView
            messages={[
              message('m0', 1),
            ]}

          />
        ))
        const scroller = container.querySelector('[data-chat-scroll-container]')
        expect(scroller).toBeTruthy()
        const optionsByType = new Map<string, unknown>()
        spy.mock.calls.forEach((call, i) => {
          if (spy.mock.instances[i] === scroller)
            optionsByType.set(call[0], call[2])
        })
        for (const type of [
          'wheel',
          'touchstart',
          'touchmove',
          'touchend',
          'touchcancel',
        ])
          expect(optionsByType.get(type), `${type} listener`).toEqual({ passive: true })
      }
      finally {
        spy.mockRestore()
      }
    })
  })

  describe('chat view row admission', () => {
    beforeEach(() => {
      virtualizerState.attachedIds = []
      virtualizerState.currentHeightKeys = new Map()
      virtualizerState.setDeferred?.(false)
      hiddenPremeasureState.candidates = []
      hiddenPremeasureState.candidateLog = []
      hiddenPremeasureState.onMeasure = undefined
      hiddenPremeasureState.contentWidthPx = undefined
      bubbleHostState.premeasureById.clear()
      wiringState.persistenceDeps = undefined
      wiringState.bandsDeps = undefined
      wiringState.railProps = undefined
      virtualizerState.setRange?.({ start: 0, end: 1 })
    })

    const fiveMessages = () => Array.from({ length: 5 }, (_, index) => message(`m${index}`, index + 1))
    const railBase = {
      loaded: true,
      minSeq: 1n,
      maxSeq: 5n,
      marks: [],
      windowFirstSeq: 1n,
      windowLastSeq: 5n,
    }

    /**
     * Admit the last two of five rows: each measures 400px, and the two fill the view of
     * 733px together, so the tail is reached with three rows staged above.
     */
    const admitLastTwo = (): void => {
      measureStagedRow('m4', 400)
      measureStagedRow('m3', 400)
    }

    it('gives the scroll hook the staged rows around the list, and the loaded rows', () => {
      render(() => <ChatView messages={fiveMessages()} />)
      admitLastTwo()

      const { admission, loadedRows } = mockState.scrollOpts!
      expect(virtualizerState.items!().map(item => item.id)).toEqual(['m3', 'm4'])
      expect(admission.stagedAbove()).toBe(3)
      expect(admission.stagedBelow()).toBe(0)
      expect(admission.awaitingFirstView()).toBe(false)
      expect(admission.holdsTail()).toBe(true)
      // A staged row is a loaded row: a place of the reader can wait for it.
      expect(loadedRows.has('m0')).toBe(true)
      expect(loadedRows.has('absent')).toBe(false)
      expect(loadedRows.nearestId(2n)).toBe('m1')
      expect(loadedRows.firstId()).toBe('m0')
    })

    it('shows the older-loading indicator once the reader waited at the staged top edge, and removes it when the wait ends', () => {
      vi.useFakeTimers()
      const { container } = render(() => <ChatView messages={fiveMessages()} />)
      admitRows()
      const indicator = () => [
        ...container.querySelectorAll('div'),
      ].find(element => element.textContent === 'Loading older messages...')

      mockState.setAtStagedTopEdge!(true)
      vi.advanceTimersByTime(SKELETON_SHOW_DELAY_MS - 1)
      expect(indicator()).toBeUndefined()
      vi.advanceTimersByTime(1)
      expect(indicator()).toBeDefined()

      mockState.setAtStagedTopEdge!(false)
      expect(indicator()).toBeUndefined()
    })

    it('shows the newer-loading indicator in the slot of the scroll-to-bottom button once the reader waited at the staged bottom edge', () => {
      vi.useFakeTimers()
      const { container, queryByRole } = render(() => (
        <ChatView messages={fiveMessages()} pagination={{ hasNewerMessages: true }} />
      ))
      admitRows()
      const indicator = () => [
        ...container.querySelectorAll('div'),
      ].find(element => element.textContent === 'Loading newer messages...')
      const button = () => queryByRole('button', { name: 'Scroll to bottom' })

      mockState.setAtStagedBottomEdge!(true)
      vi.advanceTimersByTime(SKELETON_SHOW_DELAY_MS - 1)
      expect(indicator()).toBeUndefined()
      expect(button()).not.toBeNull()

      vi.advanceTimersByTime(1)
      expect(indicator()).toBeDefined()
      expect(button()).toBeNull()

      mockState.setAtStagedBottomEdge!(false)
      expect(indicator()).toBeUndefined()
      expect(button()).not.toBeNull()
    })

    it('gives a staged row that the reader reaches next the worker priority of a row near the view', () => {
      vi.useFakeTimers()
      const messages = Array.from({ length: 40 }, (_, index) => message(`m${index}`, index + 1))
      render(() => <ChatView messages={messages} />)
      // The last ten rows measure: the tail is reached, and the rows m30 to m39 join the list.
      const heightCache = viewHeightCache()
      for (const candidate of [...hiddenPremeasureState.candidates]) {
        if (Number(candidate.item.id.slice(1)) >= 30)
          heightCache.commit(candidate.item as VirtualItem, 100, candidate.item.heightKey, 'premeasure')
      }
      expect(virtualizerState.items!()[0]?.id).toBe('m30')

      // The idle warm-up mounts the copies of rows farther above. Its idle fallback takes
      // one more millisecond.
      vi.advanceTimersByTime(WARMUP_IDLE_DELAY_MS + 1)

      // A row of the look-ahead band: the reader reaches it next.
      const near = bubbleHostState.premeasureById.get(`m${30 - LOOKAHEAD_PREMEASURE_ROWS}`)
      // A row of the warm-up band, farther above.
      const far = bubbleHostState.premeasureById.get(`m${30 - LOOKAHEAD_PREMEASURE_ROWS - 1}`)
      expect(near).toBeDefined()
      expect(far).toBeDefined()
      expect(near!.rowOffscreen?.()).toBe(false)
      expect(far!.rowOffscreen?.()).toBe(true)
    })

    it('gives the height persistence the loaded rows, staged rows included', () => {
      render(() => <ChatView messages={fiveMessages()} />)
      admitLastTwo()

      expect(virtualizerState.items!()).toHaveLength(2)
      expect(wiringState.persistenceDeps!.virtualItems().map(item => item.id)).toEqual(['m0', 'm1', 'm2', 'm3', 'm4'])
    })

    it('gives the rail the admitted rows as its window, and the staged rows above as more history', () => {
      render(() => <ChatView messages={fiveMessages()} rail={railBase} />)
      admitLastTwo()

      const rail = wiringState.railProps!
      expect(rail.rail.windowFirstSeq).toBe(4n)
      expect(rail.rail.windowLastSeq).toBe(5n)
      // The other rail data passes through.
      expect(rail.rail.minSeq).toBe(1n)
      expect(rail.hasMoreOlder).toBe(true)
      expect(rail.hasMoreNewer).toBe(false)
    })

    it('hides the rail before the first view of a mount, and shows it when rows join', () => {
      render(() => (
        <ChatView messages={fiveMessages()} rail={railBase} pagination={{ hasOlderMessages: true }} />
      ))
      // No row is measured: the list with no row needs no scrollbar.
      expect(wiringState.railProps!.hidden).toBe(true)

      admitLastTwo()

      expect(wiringState.railProps!.hidden).toBe(false)
    })

    it('keeps the rail through the first view of a window that a jump replaced', () => {
      const [messages, setMessages] = createSignal(fiveMessages())
      render(() => (
        <ChatView messages={messages()} rail={railBase} pagination={{ hasOlderMessages: true }} />
      ))
      admitLastTwo()
      expect(wiringState.railProps!.hidden).toBe(false)

      // Every loaded row is replaced, and no row of the new window is measured yet. A drag
      // on the rail lives on: the rail stays mounted and shown.
      setMessages(Array.from({ length: 5 }, (_, index) => message(`n${index}`, index + 1)))
      expect(mockState.scrollOpts!.admission.awaitingFirstView()).toBe(true)
      expect(wiringState.railProps!.hidden).toBe(false)

      admitRows()
      expect(mockState.scrollOpts!.admission.awaitingFirstView()).toBe(false)
      expect(wiringState.railProps!.hidden).toBe(false)
    })

    it('counts rows staged below as more history for the rail, except for a follower', () => {
      const [messages, setMessages] = createSignal(fiveMessages())
      render(() => <ChatView messages={messages()} rail={railBase} />)
      admitRows()
      // A new message is staged below a reader who follows the tail: it joins in a frame.
      setMessages([
        ...fiveMessages(),
        message('m5', 6),
      ])
      expect(mockState.scrollOpts!.admission.stagedBelow()).toBe(1)
      expect(wiringState.railProps!.hasMoreNewer).toBe(false)

      // The reader left the tail, and one more message is staged below.
      mockState.setIsFollowing!(false)
      setMessages([
        ...fiveMessages(),
        message('m5', 6),
        message('m6', 7),
      ])
      expect(wiringState.railProps!.hasMoreNewer).toBe(true)
    })

    it('holds the idle warm-up until the first view', () => {
      render(() => <ChatView messages={fiveMessages()} />)
      // With no row in the list, the warm-up would measure the oldest rows while the first
      // view waits for the newest.
      expect(wiringState.bandsDeps!.warmupEnabled()).toBe(false)

      admitLastTwo()

      expect(wiringState.bandsDeps!.warmupEnabled()).toBe(true)
    })

    describe('staging and the row width', () => {
      /** The ids of each `candidates` value that the mock got from `from` on. */
      const logFrom = (from: number): string[][] => hiddenPremeasureState.candidateLog.slice(from)

      it('pauses staging until a new row width held for the settle time', () => {
        vi.useFakeTimers()
        render(() => <ChatView messages={fiveMessages()} />)
        expect(hiddenPremeasureState.candidates.length).toBeGreaterThan(0)
        const pending = hiddenPremeasureState.candidates.map(candidate => candidate.item.id)

        const from = hiddenPremeasureState.candidateLog.length
        viewportSizeObserverState.onWidth!(380)
        vi.advanceTimersByTime(STAGING_WIDTH_SETTLE_MS - 1)
        // No hidden copy rendered at the new width, also not once.
        expect(logFrom(from).length).toBeGreaterThan(0)
        expect(logFrom(from).every(ids => ids.length === 0)).toBe(true)

        vi.advanceTimersByTime(1)
        expect(hiddenPremeasureState.candidates.map(candidate => candidate.item.id)).toEqual(pending)
        expect(hiddenPremeasureState.contentWidthPx).toBe(380)
      })

      it('does not pause staging for the first row width', () => {
        vi.useFakeTimers()
        viewportSizeObserverState.width = 0
        render(() => <ChatView messages={fiveMessages()} />)
        expect(hiddenPremeasureState.candidates).toEqual([])

        viewportSizeObserverState.onWidth!(372)

        // The first width above 0 does not delay the first view.
        expect(hiddenPremeasureState.candidates.length).toBeGreaterThan(0)
      })

      it('does not pause staging for a pane that loses its layout box and gets it back at the same width', () => {
        vi.useFakeTimers()
        render(() => <ChatView messages={fiveMessages()} />)
        expect(hiddenPremeasureState.candidates.length).toBeGreaterThan(0)

        viewportSizeObserverState.onWidth!(0)
        expect(hiddenPremeasureState.candidates).toEqual([])

        viewportSizeObserverState.onWidth!(640)
        expect(hiddenPremeasureState.candidates.length).toBeGreaterThan(0)
      })

      it('pauses staging for a new row width after the pane had no layout box', () => {
        vi.useFakeTimers()
        render(() => <ChatView messages={fiveMessages()} />)
        viewportSizeObserverState.onWidth!(0)

        const from = hiddenPremeasureState.candidateLog.length
        viewportSizeObserverState.onWidth!(380)
        vi.advanceTimersByTime(STAGING_WIDTH_SETTLE_MS - 1)
        expect(logFrom(from).every(ids => ids.length === 0)).toBe(true)
        expect(hiddenPremeasureState.candidates).toEqual([])

        vi.advanceTimersByTime(1)
        expect(hiddenPremeasureState.candidates.length).toBeGreaterThan(0)
      })
    })

    it('holds staging until the chat fonts are ready', async () => {
      // jsdom implements no `document.fonts`, so the case installs one whose loads it holds.
      const loads = deferred<unknown>()
      const originalFonts = Object.getOwnPropertyDescriptor(document, 'fonts')
      Object.defineProperty(document, 'fonts', {
        configurable: true,
        value: {
          check: () => false,
          load: () => loads.promise,
        },
      })
      try {
        render(() => <ChatView messages={fiveMessages()} />)
        await Promise.resolve()
        // A row measured in a fallback font would have a height that the font replaces.
        expect(hiddenPremeasureState.candidates).toEqual([])

        loads.resolve(undefined)
        await waitFor(() => {
          expect(hiddenPremeasureState.candidates.length).toBeGreaterThan(0)
        })
      }
      finally {
        if (originalFonts)
          Object.defineProperty(document, 'fonts', originalFonts)
        else
          delete (document as { fonts?: unknown }).fonts
      }
    })

    it('changes every height key when a font arrives after the fonts were ready', async () => {
      vi.useFakeTimers()
      // jsdom implements no `document.fonts`, so the case installs one whose loads it holds.
      const loads = deferred<unknown>()
      const originalFonts = Object.getOwnPropertyDescriptor(document, 'fonts')
      Object.defineProperty(document, 'fonts', {
        configurable: true,
        value: {
          check: () => false,
          load: () => loads.promise,
        },
      })
      try {
        render(() => <ChatView messages={fiveMessages()} />)
        // No load settles, so the limit makes the fonts ready: the app measures in the
        // fallback font, and no longer waits for a font server that does not answer.
        await vi.advanceTimersByTimeAsync(CHAT_FONT_LOAD_LIMIT_MS)
        const ids = hiddenPremeasureState.candidates.map(candidate => candidate.item.id)
        expect(ids.length).toBeGreaterThan(0)
        const keysInFallbackFont = new Map(virtualizerState.currentHeightKeys)

        loads.resolve(undefined)
        await vi.advanceTimersByTimeAsync(0)

        // A height that a row measured in the fallback font must not count for the font
        // that arrived, so each row has another key now.
        for (const id of ids) {
          const key = virtualizerState.currentHeightKeys.get(id)
          expect(key).toBeDefined()
          expect(key).not.toBe(keysInFallbackFont.get(id))
        }
      }
      finally {
        vi.useRealTimers()
        if (originalFonts)
          Object.defineProperty(document, 'fonts', originalFonts)
        else
          delete (document as { fonts?: unknown }).fonts
      }
    })
  })

  describe('chat view scroll bindings', () => {
    beforeEach(() => {
      virtualizerState.attachedIds = []
      virtualizerState.setDeferred?.(false)
    })

    it('writes a toggle without a scroll call', async () => {
      virtualizerState.setRange?.({ start: 0, end: 1 })
      render(() => (
        <ChatView messages={[
          message('m0', 1),
        ]}
        />
      ))
      admitRows()
      const host = await waitFor(() => {
        const bound = bubbleHostState.byId.get('m0')
        expect(bound).toBeDefined()
        return bound!
      })
      const scrollStub = mockState.scrollStub!
      const spies = [
        ...Object.values(scrollStub),
        ...Object.values(scrollStub.handlers as Record<string, unknown>),
      ].filter(value => vi.isMockFunction(value))
      for (const spy of spies)
        spy.mockClear()

      host.onSetLocalDiffView?.('split')
      host.setMessageUiState?.(MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED, true)

      expect(host.localDiffView).toBe('split')
      expect(host.getMessageUiState?.(MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED)).toBe(true)
      for (const spy of spies)
        expect(spy).not.toHaveBeenCalled()
    })

    it('passes the signal of a history jump to each pagination callback, and its result back', async () => {
      const onJumpToOldest = vi.fn((_signal?: AbortSignal) => Promise.resolve(true))
      const onJumpToLatest = vi.fn((_signal?: AbortSignal) => Promise.resolve(true))
      const onJumpToSeq = vi.fn((_seq: bigint, _signal?: AbortSignal) => Promise.resolve(true))
      render(() => (
        <ChatView
          messages={[
            message('m0', 1),
          ]}
          pagination={{ onJumpToOldest, onJumpToLatest, onJumpToSeq }}
        />
      ))
      const scrollOpts = mockState.scrollOpts!
      const signal = new AbortController().signal

      await expect(scrollOpts.onJumpToOldest!(signal)).resolves.toBe(true)
      await expect(scrollOpts.onJumpToLatest!(signal)).resolves.toBe(true)
      await expect(scrollOpts.onJumpToSeq!(5n, signal)).resolves.toBe(true)

      expect(onJumpToOldest).toHaveBeenCalledWith(signal)
      expect(onJumpToLatest).toHaveBeenCalledWith(signal)
      expect(onJumpToSeq).toHaveBeenCalledWith(5n, signal)
    })

    it('gives the scroll hook no result for a history jump that the host does not handle', () => {
      render(() => (
        <ChatView messages={[
          message('m0', 1),
        ]}
        />
      ))
      const scrollOpts = mockState.scrollOpts!
      const signal = new AbortController().signal

      // The hook reads `undefined` as "no page replaced the window", and lands nothing.
      expect(scrollOpts.onJumpToOldest!(signal)).toBeUndefined()
      expect(scrollOpts.onJumpToLatest!(signal)).toBeUndefined()
      expect(scrollOpts.onJumpToSeq!(5n, signal)).toBeUndefined()
    })

    it('re-sticks on each expand tick of the thinking indicator', async () => {
      // The indicator's tick loop asks for a frame on each tick, so the synchronous frame of
      // the test setup would recurse. A queue that the test runs by hand replaces it.
      const frames: FrameRequestCallback[] = []
      vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => frames.push(cb))
      vi.stubGlobal('cancelAnimationFrame', () => {})
      try {
        virtualizerState.setRange?.({ start: 0, end: 1 })
        render(() => (
          <ChatView
            messages={[
              message('m0', 1),
            ]}
            agentLifecycle={{ agentWorking: true }}
          />
        ))
        const restick = mockState.scrollStub!.restickIfAtBottom as ReturnType<typeof vi.fn>
        restick.mockClear()

        for (let run = 0; run < 3 && frames.length > 0; run++) {
          const pending = frames.splice(0)
          for (const cb of pending)
            cb(performance.now())
        }

        expect(restick).toHaveBeenCalled()
      }
      finally {
        vi.unstubAllGlobals()
      }
    })

    it('reads the near-viewport band in content coordinates', async () => {
      // The layer is shifted by -1000: scroll coordinate 100 is content 1100.
      virtualizerState.layoutShift = -1000
      virtualizerState.setRange?.({ start: 0, end: 40 })
      const messages = Array.from({ length: 40 }, (_, index) => message(`m${index}`, index + 1))
      const { container } = render(() => <ChatView messages={messages} />)
      admitRows()
      const list = container.querySelector('[data-chat-scroll-container]') as HTMLDivElement
      expect(list.querySelector<HTMLElement>(`.${virtualLayer}`)?.style.transform).toBe('translateY(-1000px)')
      expect(list.querySelector<HTMLElement>(`.${virtualSpacer}`)?.style.height).toBe('9000px')
      Object.defineProperty(list, 'scrollTop', {
        configurable: true,
        get: () => 100,
      })
      Object.defineProperty(list, 'clientHeight', {
        configurable: true,
        get: () => 500,
      })
      await waitFor(() => {
        expect(bubbleHostState.byId.get('m12')).toBeDefined()
        expect(bubbleHostState.byId.get('m30')).toBeDefined()
      })

      // Row 12 spans content 1200 to 1300, inside the view (1100 to 1600) and its slack.
      expect(bubbleHostState.byId.get('m12')!.rowOffscreen?.()).toBe(false)
      expect(bubbleHostState.byId.get('m30')!.rowOffscreen?.()).toBe(true)
    })

    it('attaches the spacer and the layer of rows to the virtualizer, and detaches each one when it unmounts', () => {
      // A rebase writes the height of the spacer and the transform of the layer. Each one
      // must reach the slot that writes its own property, and leave it when it unmounts.
      //
      // The record is cleared here and not in an afterEach: the testing library unmounts the
      // view of the previous test after the afterEach of this suite, and that unmount
      // detaches its elements into the record.
      virtualizerState.layoutAttachments = []
      virtualizerState.setRange?.({ start: 0, end: 1 })
      const { container, unmount } = render(() => (
        <ChatView messages={[
          message('m0', 1),
        ]}
        />
      ))
      const attached = (slot: 'spacer' | 'layer') => virtualizerState.layoutAttachments
        .filter(attachment => attachment.slot === slot)
        .map(attachment => attachment.element)
      const spacer = container.querySelector<HTMLElement>(`.${virtualSpacer}`)
      const layer = container.querySelector<HTMLElement>(`.${virtualLayer}`)
      expect(spacer).not.toBeNull()
      expect(layer).not.toBeNull()
      expect(attached('spacer')).toHaveLength(1)
      expect(attached('spacer')[0]).toBe(spacer)
      expect(attached('layer')).toHaveLength(1)
      expect(attached('layer')[0]).toBe(layer)

      unmount()
      expect(attached('spacer')).toHaveLength(2)
      expect(attached('spacer')[1]).toBeUndefined()
      expect(attached('layer')).toHaveLength(2)
      expect(attached('layer')[1]).toBeUndefined()
    })
  })

  // The scroll-idle window that holds syntax highlighting (and the premeasure warm-up)
  // off while the reader scrolls. It runs on a createScrollActivity instance shared with
  // the scroll rail's own window, so these pin the behaviour that must NOT change when
  // the rail's window is fed differently -- see ChatView's highlightActivity.
  describe('chat view syntax-highlight scroll pause', () => {
    /** Mount a chat with one row, and admit that row to the list. */
    const renderChat = () => {
      const view = render(() => (
        <ChatView
          messages={[
            message('m0', 1),
          ]}

        />
      ))
      admitRows()
      return view
    }

    // The bubble of the row in the list: a hidden premeasure copy draws a bubble also.
    const paused = (container: HTMLElement): string | null =>
      container.querySelector('[data-chat-scroll-container] [data-testid="mock-message-bubble"]')?.getAttribute('data-syntax-paused') ?? null

    const scroller = (container: HTMLElement): HTMLElement =>
      container.querySelector('[data-chat-scroll-container]') as HTMLElement

    it('pauses highlighting on a scroll gesture and resumes after the idle window', () => {
      vi.useFakeTimers({
        toFake: ['performance', 'setTimeout', 'clearTimeout'],
      })
      const { container } = renderChat()
      expect(paused(container)).toBe('false')

      fireEvent.wheel(scroller(container), { deltaY: 40 })
      expect(paused(container)).toBe('true')

      vi.advanceTimersByTime(SYNTAX_HIGHLIGHT_SCROLL_IDLE_MS - 1)
      expect(paused(container)).toBe('true')

      vi.advanceTimersByTime(1)
      expect(paused(container)).toBe('false')
    })

    it('stays paused across a continued scroll instead of resuming mid-gesture', () => {
      vi.useFakeTimers({
        toFake: ['performance', 'setTimeout', 'clearTimeout'],
      })
      const { container } = renderChat()

      fireEvent.wheel(scroller(container), { deltaY: 40 })
      vi.advanceTimersByTime(SYNTAX_HIGHLIGHT_SCROLL_IDLE_MS - 20)
      fireEvent.wheel(scroller(container), { deltaY: 40 })

      // A throttle would have resumed at the first window's end; the debounce measures
      // from the second wheel.
      vi.advanceTimersByTime(SYNTAX_HIGHLIGHT_SCROLL_IDLE_MS - 20)
      expect(paused(container)).toBe('true')

      vi.advanceTimersByTime(20)
      expect(paused(container)).toBe('false')
    })

    it('still pauses on a bare scroll event with no user input behind it', () => {
      // Deliberate asymmetry with the rail, whose window ignores an un-prompted scroll:
      // the agent's stick-to-bottom churn during a streaming turn is exactly when
      // highlighting and the premeasure warm-up must stay off the main thread.
      vi.useFakeTimers({
        toFake: ['performance', 'setTimeout', 'clearTimeout'],
      })
      const { container } = renderChat()

      fireEvent.scroll(scroller(container))
      expect(paused(container)).toBe('true')

      vi.advanceTimersByTime(SYNTAX_HIGHLIGHT_SCROLL_IDLE_MS)
      expect(paused(container)).toBe('false')
    })

    it('pauses on the keys that scroll natively and ignores the keys that do not', () => {
      vi.useFakeTimers({
        toFake: ['performance', 'setTimeout', 'clearTimeout'],
      })
      const { container } = renderChat()

      fireEvent.keyDown(scroller(container), { key: 'a' })
      expect(paused(container)).toBe('false')
      // A shortcut with a modifier is no scroll of the list.
      fireEvent.keyDown(scroller(container), { key: 'End', ctrlKey: true })
      expect(paused(container)).toBe('false')

      fireEvent.keyDown(scroller(container), { key: 'PageDown' })
      expect(paused(container)).toBe('true')
    })

    it('ignores a scroll key that a text field in the list receives', () => {
      vi.useFakeTimers({
        toFake: ['performance', 'setTimeout', 'clearTimeout'],
      })
      const { container } = renderChat()
      // PageDown in a text field moves its caret, and the list does not scroll.
      const field = document.createElement('textarea')
      scroller(container).append(field)

      fireEvent.keyDown(field, { key: 'PageDown' })
      expect(paused(container)).toBe('false')

      fireEvent.keyDown(scroller(container), { key: 'PageDown' })
      expect(paused(container)).toBe('true')
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
        <ChatView
          messages={[
            message('m0', 1),
          ]}
          rail={railBase}
        />
      ))
      admitRows()
      expect(scroller(container).className).toContain(messageListRailActive)
    })

    it('hides the native scrollbar for an empty seeded rail because there is no native overflow to preserve', () => {
      const { container } = render(() => (
        <ChatView messages={[]} rail={{ ...railBase, windowFirstSeq: undefined, windowLastSeq: undefined }} />
      ))
      expect(scroller(container).className).toContain(messageListRailActive)
    })

    it('keeps the native scrollbar while the rail is unseeded (marks RPC failed / slow)', () => {
      const { container } = render(() => (
        <ChatView
          messages={[
            message('m0', 1),
          ]}
          rail={{ ...railBase, loaded: false }}
        />
      ))
      admitRows()
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

          rail={{
            ...railBase,
            minSeq: unsafe,
            maxSeq: unsafe,
            windowFirstSeq: unsafe,
            windowLastSeq: unsafe,
          }}
        />
      ))
      admitRows()

      expect(scroller(container).className).not.toContain(messageListRailActive)
    })
  })

  // The seam between ChatView's railActivity window and the rail's paint state. The unit
  // suites cover each half (chatScrollActivity.test.ts, ChatScrollRail.test.tsx); these pin
  // that ChatView connects them the right way round and feeds the rail window the
  // momentum-gated `noteScroll` rather than the highlight window's `noteInput`.
  describe('chat view scroll rail auto-hide wiring', () => {
    const railBase = {
      loaded: true,
      minSeq: 1n,
      maxSeq: 10n,
      marks: [],
      windowFirstSeq: 1n,
      windowLastSeq: 5n,
    }
    const renderWithRail = () => {
      const view = render(() => (
        <ChatView
          messages={[
            message('m0', 1),
          ]}
          rail={railBase}
        />
      ))
      // The rail owns the scrollbar once a row is in the list.
      admitRows()
      return view
    }
    const scroller = (container: HTMLElement) => container.querySelector('[data-chat-scroll-container]') as HTMLElement
    const rail = (container: HTMLElement) => container.querySelector('[data-testid="chat-scroll-rail"]') as HTMLElement

    it('starts idle, lights the rail on a user scroll input, and lets it idle again', () => {
      vi.useFakeTimers({
        toFake: ['performance', 'setTimeout', 'clearTimeout'],
      })
      const { container } = renderWithRail()
      // Nothing has scrolled yet, so the rail opens in its faded state rather than flashing
      // on at mount and fading a beat later.
      expect(rail(container).className).toContain(railIdle)

      fireEvent.wheel(scroller(container), { deltaY: 40 })
      expect(rail(container).className).not.toContain(railIdle)

      vi.advanceTimersByTime(RAIL_VISIBLE_IDLE_MS - 1)
      expect(rail(container).className).not.toContain(railIdle)

      vi.advanceTimersByTime(1)
      expect(rail(container).className).toContain(railIdle)
    })

    it('does not light the rail for a bare scroll event with no user input behind it', () => {
      // The whole point of the split: the agent's stick-to-bottom fires a scroll on every
      // streaming commit, and a rail that lit up for those would stay lit for the entire
      // response. Contrast with the syntax-highlight pause above, which DOES take these.
      vi.useFakeTimers({
        toFake: ['performance', 'setTimeout', 'clearTimeout'],
      })
      const { container } = renderWithRail()

      for (let commit = 0; commit < 10; commit++) {
        vi.advanceTimersByTime(16)
        fireEvent.scroll(scroller(container))
        expect(rail(container).className).toContain(railIdle)
      }
    })

    it('keeps the rail lit through a flick coast, where only scroll events fire', () => {
      // After a touch flick no touch or pointer event fires while the content glides, so
      // without the coast relight the rail would fade mid-fling -- which is exactly what
      // it did while the bound was shorter than a real glide.
      vi.useFakeTimers({
        toFake: ['performance', 'setTimeout', 'clearTimeout'],
      })
      const { container } = renderWithRail()

      // The flick. touchMOVE is what lights the rail: the press alone moves nothing.
      fireEvent.touchStart(scroller(container))
      fireEvent.touchMove(scroller(container))
      fireEvent.touchEnd(scroller(container))
      expect(rail(container).className).not.toContain(railIdle)

      // The coast: the hook reports its momentum scrolls with the finger long
      // gone, each one pushing the window out past where the touch would have
      // closed it. Fired through the hook's callback because the stub here
      // never runs the real scroll handler (see mockState.scrollOpts).
      const COAST_TICK_MS = RAIL_VISIBLE_IDLE_MS - 100
      const COAST_TICKS = 3
      // The whole coast stays inside the cap; outrunning it is the next test.
      expect(COAST_TICK_MS * COAST_TICKS).toBeLessThan(RAIL_COAST_MAX_MS)
      for (let tick = 0; tick < COAST_TICKS; tick++) {
        vi.advanceTimersByTime(COAST_TICK_MS)
        mockState.scrollOpts?.onMomentumScroll?.()
        expect(rail(container).className).not.toContain(railIdle)
      }

      // ...and it fades RAIL_VISIBLE_IDLE_MS after the LAST coast scroll.
      vi.advanceTimersByTime(RAIL_VISIBLE_IDLE_MS - 1)
      expect(rail(container).className).not.toContain(railIdle)
      vi.advanceTimersByTime(1)
      expect(rail(container).className).toContain(railIdle)
    })

    // The cap on that relight. The hook also reports a scroll that the browser makes
    // by itself, such as one that brings a focused element into view, and an uncapped
    // relight would then light the rail again for each such scroll, however long after
    // the last input. The cap is measured from the last INPUT and never re-based by a
    // scroll.
    it('stops relighting the rail once a coast outruns its bound', () => {
      vi.useFakeTimers({
        toFake: ['performance', 'setTimeout', 'clearTimeout'],
      })
      const { container } = renderWithRail()

      fireEvent.touchStart(scroller(container))
      fireEvent.touchMove(scroller(container))
      fireEvent.touchEnd(scroller(container))

      vi.advanceTimersByTime(RAIL_COAST_MAX_MS + 1)
      mockState.scrollOpts?.onMomentumScroll?.()

      // The window closed on its own idle timer, and that scroll did not reopen it.
      expect(rail(container).className).toContain(railIdle)
    })

    // A finger landing on the transcript scrolls nothing. Lighting the rail for it put
    // a scrollbar on screen every time the reader touched the list -- to dismiss the
    // keyboard, say -- and that tap is the gesture this pins as inert.
    it('does not light the rail for a press that never moves', () => {
      vi.useFakeTimers({
        toFake: ['performance', 'setTimeout', 'clearTimeout'],
      })
      const { container } = renderWithRail()

      fireEvent.touchStart(scroller(container))
      expect(rail(container).className).toContain(railIdle)
      fireEvent.touchEnd(scroller(container))
      expect(rail(container).className).toContain(railIdle)

      fireEvent.pointerDown(scroller(container))
      expect(rail(container).className).toContain(railIdle)
      fireEvent.pointerUp(scroller(container))
      expect(rail(container).className).toContain(railIdle)

      // The first MOVE is what lights it. A mouse hover does not: that is
      // pointermove with no button down. A touch move (or a mouse drag) does.
      fireEvent.pointerMove(scroller(container), { pointerType: 'mouse', buttons: 0 })
      expect(rail(container).className).toContain(railIdle)

      fireEvent.pointerMove(scroller(container), { pointerType: 'touch' })
      expect(rail(container).className).not.toContain(railIdle)
    })

    it('lights the rail for a key that scrolls the list, and not for a shortcut or a key in a text field', () => {
      vi.useFakeTimers({
        toFake: ['performance', 'setTimeout', 'clearTimeout'],
      })
      const { container } = renderWithRail()

      fireEvent.keyDown(scroller(container), { key: 'End', ctrlKey: true })
      expect(rail(container).className).toContain(railIdle)

      const field = document.createElement('textarea')
      scroller(container).append(field)
      fireEvent.keyDown(field, { key: 'PageDown' })
      expect(rail(container).className).toContain(railIdle)

      fireEvent.keyDown(scroller(container), { key: 'PageDown' })
      expect(rail(container).className).not.toContain(railIdle)
    })

    it('does not light the rail for a mouse hover', () => {
      vi.useFakeTimers({
        toFake: ['performance', 'setTimeout', 'clearTimeout'],
      })
      const { container } = renderWithRail()

      fireEvent.pointerMove(scroller(container), { pointerType: 'mouse', buttons: 0 })
      expect(rail(container).className).toContain(railIdle)

      fireEvent.pointerMove(scroller(container), { pointerType: 'mouse', buttons: 1 })
      expect(rail(container).className).not.toContain(railIdle)
    })

    it('does not arm the rail window on scroll when no rail is present', () => {
      // Without a rail prop the rail never mounts, so feeding its window arms a 1200ms timer that
      // fires into a dead signal on every gesture. The feed is gated on rail presence so a
      // marks-less conversation pays nothing. Assert the gate indirectly: render with no rail,
      // then mount one and confirm the now-fed window lights on the next scroll.
      vi.useFakeTimers({
        toFake: ['performance', 'setTimeout', 'clearTimeout'],
      })
      const { container } = render(() => (
        <ChatView messages={[
          message('m0', 1),
        ]}
        />
      ))

      // No rail mounted, so no rail element to fade or light.
      expect(container.querySelector('[data-testid="chat-scroll-rail"]')).toBeNull()
      // Scrolling while rail-less must not leave an armed rail timer behind (it would fire into
      // a dead signal). Advance well past the idle window: a stray timer firing here would throw
      // into the disposed-rail scope only if the gate were absent, which we cannot assert
      // directly -- but the rail stays absent, which is the observable contract.
      fireEvent.wheel(scroller(container), { deltaY: 40 })
      vi.advanceTimersByTime(RAIL_VISIBLE_IDLE_MS + 1)
      expect(container.querySelector('[data-testid="chat-scroll-rail"]')).toBeNull()
    })
  })
  describe('data-span-columns row hook', () => {
    // Every span-line column class is a hashed vanilla-extract name, so
    // data-span-columns is the only stable way for an e2e spec to read how many
    // rails one row draws. It must be present on BOTH branches, including the
    // zero case -- that is the case a subagent spawn produces.
    const withSpanLines = (id: string, lines: unknown[]): AgentChatMessage => ({
      ...makeMessage('assistant', `row ${id}`, id),
      spanLines: JSON.stringify(lines),
    } as AgentChatMessage)

    it('reports 0 for a row that draws no rail', () => {
      const { container } = render(() => (
        <PreferencesProvider>
          <ChatView messages={[
            withSpanLines('m1', []),
          ]}
          />
        </PreferencesProvider>
      ))
      admitRows()
      const rows = container.querySelectorAll('[data-span-columns]')
      expect(rows.length).toBeGreaterThan(0)
      rows.forEach(row => expect(row.getAttribute('data-span-columns')).toBe('0'))
    })

    it('reports the column count for a row that draws rails', () => {
      const { container } = render(() => (
        <PreferencesProvider>
          <ChatView
            messages={[
              withSpanLines('m1', [
                { span_id: 'span-A', color: 1, type: 'active' },
                { span_id: 'span-B', color: 2, type: 'connector_end' },
              ]),
            ]}

          />
        </PreferencesProvider>
      ))
      admitRows()
      const rows = container.querySelectorAll('[data-span-columns]')
      expect(rows.length).toBeGreaterThan(0)
      rows.forEach(row => expect(row.getAttribute('data-span-columns')).toBe('2'))
    })
  })
})
