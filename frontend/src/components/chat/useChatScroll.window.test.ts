import type { ChatScrollVirtualizer } from './useChatScroll'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { MAX_LOADED_CHAT_MESSAGES } from '~/stores/chat.store'

import { FLING_OVERSCAN_HARD_CAP_PX, flingOverscanCapPx, useChatScroll } from './useChatScroll'
import { installScrollTestEnv, makeFakeScrollDiv, makeGrowableVirtualizer, measurementDeferralNoOps } from './useChatScroll.testkit'

installScrollTestEnv()

describe('usechatscroll render-ahead overscan', () => {
  it('keeps the fling lead cap in screens of the pane, bounded against the mount-burst budget', () => {
    // A 1200px cap left only ~2.3k px of forward coverage on a 733px pane once base
    // overscan was included, which can still expose blank spacer under coalesced
    // momentum. A 4000px cap mounted 30+ rows in one observed 732px-pane scroll commit.
    // The cap is now derived in SCREENS: on the ~733px calibration pane it must land in
    // the same middle band the fixed 1800px was tuned to, a taller pane keeps the same
    // screens of coverage instead of degrading toward one screen, and the hard ceiling
    // rejects any return toward the old multi-screen mount burst on extreme panes.
    expect(flingOverscanCapPx(733)).toBeGreaterThanOrEqual(1800)
    expect(flingOverscanCapPx(733)).toBeLessThanOrEqual(2400)
    expect(flingOverscanCapPx(1200)).toBeCloseTo(flingOverscanCapPx(733) * (1200 / 733), 0)
    expect(flingOverscanCapPx(10000)).toBe(FLING_OVERSCAN_HARD_CAP_PX)
    expect(FLING_OVERSCAN_HARD_CAP_PX).toBeLessThanOrEqual(4000)
  })

  it('extends the rendered slice ahead in the fling direction during a fast scroll', () =>
    new Promise<void>((resolve, reject) => {
      // Real timers (no fake): the velocity tracker reads a monotonic clock, so a
      // genuine time gap between the two scroll events is what measures a fast fling.
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(50000)
          div.setScrollTop(40000) // scrolled deep into a long transcript
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          // Record the look-ahead args handleScroll passes to updateViewport.
          let lastLeadPx = -1
          let lastLeadDir: 'older' | 'newer' | undefined = 'newer'
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => 50000,
            geometryVersion: () => 0,
            updateViewport: (_st, _ch, lead) => {
              lastLeadPx = lead?.px ?? 0
              lastLeadDir = lead?.dir
            },
            anchorAt: () => null,
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => null,
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // First scroll establishes the velocity baseline; with no measured speed yet
          // (the unknown Infinity seed) there is no look-ahead. Read synchronously,
          // before any deferred refresh can overwrite it with the base (no-lead) slice.
          div.setScrollTop(39000)
          hook.handlers.onScroll()
          expect(lastLeadPx).toBe(0)

          // A real time gap, then a hard UP jump: a fast fling. The render-ahead must
          // be positive and point 'older' (the gesture direction) so the rows the next
          // frame lands on are already mounted.
          await new Promise(r => setTimeout(r, 20))
          div.setScrollTop(1000) // ~38000px up
          hook.handlers.onScroll()
          const flungLeadPx = lastLeadPx
          const flungLeadDir = lastLeadDir
          expect(flungLeadDir).toBe('older')
          // A ~38000px jump over ~20ms is far past the cap (velocity * look-ahead >>
          // flingOverscanCapPx(500)), so the lead clamps to the ceiling deterministically.
          expect(flungLeadPx).toBe(flingOverscanCapPx(500))

          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('renders ahead toward the tail on a fast DOWNWARD fling (newer direction)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(50000)
          div.setScrollTop(1000) // near the top, away from the bottom
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          let lastLeadPx = -1
          let lastLeadDir: 'older' | 'newer' | undefined = 'older'
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => 50000,
            geometryVersion: () => 0,
            updateViewport: (_st, _ch, lead) => {
              lastLeadPx = lead?.px ?? 0
              lastLeadDir = lead?.dir
            },
            anchorAt: () => null,
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => null,
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Seed the baseline (no measured speed yet -> no look-ahead).
          div.setScrollTop(2000)
          hook.handlers.onScroll()
          expect(lastLeadPx).toBe(0)

          // A real gap, then a hard DOWN jump: the render-ahead points 'newer' and
          // clamps to the cap.
          await new Promise(r => setTimeout(r, 20))
          div.setScrollTop(40000) // ~38000px down
          hook.handlers.onScroll()
          const flungLeadPx = lastLeadPx
          const flungLeadDir = lastLeadDir
          expect(flungLeadDir).toBe('newer')
          expect(flungLeadPx).toBe(flingOverscanCapPx(500))

          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('drops the lead on a zero-delta event instead of pointing the stale direction', () =>
    new Promise<void>((resolve, reject) => {
      // Fake timers freeze the velocity tracker's monotonic clock (perf.now), so two
      // samples at the SAME scrollTop with NO time advance produce dt=0 -- the
      // coalesced same-tick case where speed() keeps the prior fling value. That is
      // exactly the state where the lead must NOT trust the stale lastScrollDir.
      vi.useFakeTimers()
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(50000)
          div.setScrollTop(40000)
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          let lastLeadPx = -1
          let lastLeadDir: 'older' | 'newer' | undefined
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => 50000,
            geometryVersion: () => 0,
            updateViewport: (_st, _ch, lead) => {
              lastLeadPx = lead?.px ?? 0
              lastLeadDir = lead?.dir
            },
            anchorAt: () => null,
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => null,
          }
          const hook = useChatScroll({ virtualizer: virt, messages, streamingText })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Seed, then a 20ms gap and a hard UP jump = a fast 'older' fling at the cap.
          div.setScrollTop(39000)
          hook.handlers.onScroll()
          vi.advanceTimersByTime(20)
          div.setScrollTop(1000)
          hook.handlers.onScroll()
          expect(lastLeadDir).toBe('older')
          expect(lastLeadPx).toBe(flingOverscanCapPx(500))

          // A coalesced same-tick event at the SAME scrollTop (no time advance): speed()
          // still reads the fling value (dt=0 kept it), but the direction is now
          // indeterminate. The lead must collapse to none rather than extend 'older'
          // (the side the viewport just LEFT) and flash an unrendered gap.
          hook.handlers.onScroll()
          expect(lastLeadPx).toBe(0)

          dispose()
          vi.useRealTimers()
          resolve()
        }
        catch (e) {
          dispose()
          vi.useRealTimers()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))
})

describe('usechatscroll windowing trim', () => {
  const mkMsgs = (n: number): AgentChatMessage[] =>
    Array.from({ length: n }, (_, i) => ({ seq: BigInt(i + 1) } as AgentChatMessage))

  it('trims the oldest end while following the tail even after the geometry re-pin advanced the sticky record', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(1000)
          div.setClientHeight(500)
          div.setScrollTop(500) // at the bottom
          // A window already at the cap: one new tail message pushes it over.
          const [messages, setMessages] = createSignal<AgentChatMessage[]>(mkMsgs(MAX_LOADED_CHAT_MESSAGES))
          const [streamingText] = createSignal('')
          const { virt, setTotal } = makeGrowableVirtualizer()
          setTotal(1000)
          let trims = 0
          let lastKeep = -1
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            onTrimOldMessages: (k) => {
              trims++
              lastKeep = k
            },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Establish the sticky record at the current bottom while following.
          hook.forceScrollToBottom()
          expect(hook.atBottom()).toBe(true)

          // A new tail message arrives (length crosses the cap) AND the spacer
          // grows. The geometry re-pin effect is created before the auto-scroll
          // effect, so it runs first: it re-sticks to the new bottom and advances
          // stickyScrollHeight to the grown value. The auto-scroll effect then
          // sees scrollHeight === stickyScrollHeight. The trim must STILL fire --
          // it's the only window cap while following the tail (pagination trims
          // don't run then), so gating it on the height delta would let the
          // in-memory set grow unbounded during streaming.
          div.setScrollHeight(1100)
          setTotal(1100)
          setMessages(mkMsgs(MAX_LOADED_CHAT_MESSAGES + 1))
          await Promise.resolve()
          await Promise.resolve()

          expect(div.getScrollTop()).toBe(600) // followed to the new bottom
          expect(trims).toBeGreaterThanOrEqual(1)
          // keepNewest is buffer-aware: less than a buffer (1500px) of visible content
          // sits above the 600px bottom here, so it keeps the whole window rather than
          // reaping rows the filler surfaced (the trim/refetch loop). A NORMAL followed
          // tail has far more than a buffer of visible content above the bottom, so
          // keepNewest clamps to the lean base instead.
          expect(lastKeep).toBe(MAX_LOADED_CHAT_MESSAGES + 1)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('trims the oldest end while SCROLLED UP at the live tail (not just while following)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(4500) // start at the bottom
          const [messages, setMessages] = createSignal<AgentChatMessage[]>(mkMsgs(MAX_LOADED_CHAT_MESSAGES))
          const [streamingText] = createSignal('')
          const { virt, setTotal } = makeGrowableVirtualizer()
          setTotal(5000)
          let trims = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            // Still AT the live tail (no newer history unloaded), so live messages
            // append to the tail rather than being dropped by the beyond-window guard.
            hasNewerMessages: () => false,
            onTrimOldMessages: () => { trims++ },
          })
          // (This stub resolves no anchor, so currentAnchor stays null and keepNewest
          // is 0 -- the viewport-protecting keepNewest is covered by the next test.)
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Now scroll UP and register the position so atBottom is false.
          div.setScrollTop(2000)
          hook.handlers.onScroll()
          expect(hook.atBottom()).toBe(false)

          // A live message arrives while the user is scrolled up watching history.
          // Before the fix the trim was gated behind atBottom, so the in-memory
          // window grew unbounded during a long stream. It must fire regardless of
          // scroll position -- it's the only window cap at the live tail.
          setMessages(mkMsgs(MAX_LOADED_CHAT_MESSAGES + 1))
          await Promise.resolve()
          await Promise.resolve()

          expect(trims).toBeGreaterThanOrEqual(1)
          // The user's scroll position is NOT yanked to the bottom by the append.
          expect(hook.atBottom()).toBe(false)
          expect(div.getScrollTop()).toBe(2000)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('does not trim the older buffer after an older prepend wakes the auto-scroll effect again', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(8000)
          div.setClientHeight(733)
          div.setScrollTop(2500)
          const mkSeqMsgs = (first: number, last: number): AgentChatMessage[] =>
            Array.from({ length: last - first + 1 }, (_, i) => {
              const seq = BigInt(first + i)
              return { id: `m${seq}`, seq } as AgentChatMessage
            })
          const [messages, setMessages] = createSignal<AgentChatMessage[]>(mkSeqMsgs(101, 250))
          const [streamingText, setStreamingText] = createSignal('')
          const { virt, setTotal } = makeGrowableVirtualizer()
          setTotal(8000)
          let trims = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => false,
            onTrimOldMessages: () => { trims++ },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()
          trims = 0

          // Older prefetch: the first server seq moves earlier, while the newest edge
          // is unchanged. The auto-scroll effect must treat this as an older-buffer
          // mutation, not as live-tail growth to cap.
          setMessages(mkSeqMsgs(51, 250))
          await Promise.resolve()
          await Promise.resolve()
          expect(trims).toBe(0)

          // A later non-row wake-up used to see the stale pre-prepend signature and
          // trim the just-prefetched older rows, producing a prepend/trim re-pin pair.
          setStreamingText('tail is still streaming')
          await Promise.resolve()
          await Promise.resolve()
          expect(trims).toBe(0)

          // A real newest-edge row append still runs the live-tail cap.
          setMessages(mkSeqMsgs(51, 251))
          await Promise.resolve()
          await Promise.resolve()
          expect(trims).toBeGreaterThan(0)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('still trims when an older prepend also advances the newest edge', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(8000)
          div.setClientHeight(733)
          div.setScrollTop(2500)
          const mkSeqMsgs = (first: number, last: number): AgentChatMessage[] =>
            Array.from({ length: last - first + 1 }, (_, i) => {
              const seq = BigInt(first + i)
              return { id: `m${seq}`, seq } as AgentChatMessage
            })
          const [messages, setMessages] = createSignal<AgentChatMessage[]>(mkSeqMsgs(101, 250))
          const [streamingText] = createSignal('')
          const { virt, setTotal } = makeGrowableVirtualizer()
          setTotal(8000)
          let trims = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => false,
            onTrimOldMessages: () => { trims++ },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()
          trims = 0

          setMessages(mkSeqMsgs(51, 251))
          await Promise.resolve()
          await Promise.resolve()

          expect(trims).toBeGreaterThan(0)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('passes a viewport-protecting keepNewest so the oldest trim spares the reader\'s anchored row', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(4500) // start at the bottom
          // Rows carry ids so the trim can locate the anchored row in the window.
          const mkIdMsgs = (n: number): AgentChatMessage[] =>
            Array.from({ length: n }, (_, i) => ({ id: `m${i + 1}`, seq: BigInt(i + 1) } as AgentChatMessage))
          const [messages, setMessages] = createSignal<AgentChatMessage[]>(mkIdMsgs(MAX_LOADED_CHAT_MESSAGES))
          const [streamingText] = createSignal('')
          // A virtualizer that pins the viewport midpoint to row 'm50' (the reader
          // scrolled up to it). totalHeight constant so the geometry effect is quiet.
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => 5000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => ({ id: 'm50', offsetWithinRow: 0 }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => 2000,
          }
          let lastKeep = -1
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => false,
            onTrimOldMessages: (k) => { lastKeep = k },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Scroll up so the anchor (row m50) is captured.
          div.setScrollTop(2000)
          hook.handlers.onScroll()
          expect(hook.atBottom()).toBe(false)

          // A live message appends at the tail (window now 151 rows). The trim must
          // keep from the anchored row m50 (index 49) to the tail: 151 - 49 = 102,
          // so the reader's viewport rows are never dropped.
          setMessages(mkIdMsgs(MAX_LOADED_CHAT_MESSAGES + 1))
          await Promise.resolve()
          await Promise.resolve()

          expect(lastKeep).toBe(102)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('excludes trailing optimistic locals from keepNewest so the trim cap stays on server rows', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(4500)
          // Server rows (seq 1..n) followed by two trailing optimistic locals
          // (seq 0n) -- the exact shape the store keeps while a send is in flight.
          const mkMsgs = (server: number): AgentChatMessage[] => [
            ...Array.from({ length: server }, (_, i) => ({ id: `m${i + 1}`, seq: BigInt(i + 1) } as AgentChatMessage)),
            { id: 'local-1', seq: 0n } as AgentChatMessage,
            { id: 'local-2', seq: 0n } as AgentChatMessage,
          ]
          const [messages, setMessages] = createSignal<AgentChatMessage[]>(mkMsgs(MAX_LOADED_CHAT_MESSAGES))
          const [streamingText] = createSignal('')
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => 5000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => ({ id: 'm50', offsetWithinRow: 0 }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => 2000,
          }
          let lastKeep = -1
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => false,
            onTrimOldMessages: (k) => { lastKeep = k },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          div.setScrollTop(2000)
          hook.handlers.onScroll()
          expect(hook.atBottom()).toBe(false)

          // Append a live SERVER row: window is now 151 server + 2 locals (153).
          // The anchor m50 sits at index 49; the SERVER rows from there to the tail
          // are 151 - 49 = 102. The two trailing locals must NOT inflate the cap
          // (counting them would yield 104 and over-retain two old server rows).
          setMessages(mkMsgs(MAX_LOADED_CHAT_MESSAGES + 1))
          await Promise.resolve()
          await Promise.resolve()

          expect(lastKeep).toBe(102)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('protects the whole window when the anchored row was displaced out of the list', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(4500)
          const mkIdMsgs = (n: number): AgentChatMessage[] =>
            Array.from({ length: n }, (_, i) => ({ id: `m${i + 1}`, seq: BigInt(i + 1) } as AgentChatMessage))
          const [messages, setMessages] = createSignal<AgentChatMessage[]>(mkIdMsgs(MAX_LOADED_CHAT_MESSAGES))
          const [streamingText] = createSignal('')
          // The captured anchor row is NOT present in the window (deleted / reseq'd
          // out between capture and the trim), so findIndex returns -1.
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => 5000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => ({ id: 'gone', offsetWithinRow: 0 }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => 2000,
          }
          let lastKeep = -1
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => false,
            onTrimOldMessages: (k) => { lastKeep = k },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          div.setScrollTop(2000)
          hook.handlers.onScroll()
          expect(hook.atBottom()).toBe(false)

          // A displaced anchor must NOT drop keepNewest to 0 (the normal cap),
          // which could trim rows the reader can still see. Protect the whole
          // current window instead; the store clamps it to the hard ceiling.
          setMessages(mkIdMsgs(MAX_LOADED_CHAT_MESSAGES + 1))
          await Promise.resolve()
          await Promise.resolve()

          expect(lastKeep).toBe(MAX_LOADED_CHAT_MESSAGES + 1)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('recaptures a resolvable anchor when displaced, so the trim keeps the viewport cap not the whole window', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(500)
          div.setScrollTop(4500)
          const mkIdMsgs = (start: number, n: number): AgentChatMessage[] =>
            Array.from({ length: n }, (_, i) => ({ id: `m${start + i}`, seq: BigInt(start + i) } as AgentChatMessage))
          const [messages, setMessages] = createSignal<AgentChatMessage[]>(mkIdMsgs(1, MAX_LOADED_CHAT_MESSAGES))
          const [streamingText] = createSignal('')
          // anchorAt resolves to m1 while it's present (capture), then to a present
          // MIDDLE row m10 once m1 is gone -- modelling an optimistic local that
          // reconciled to a server echo under a new id between capture and trim.
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => 5000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => ({ id: messages().some(m => m.id === 'm1') ? 'm1' : 'm10', offsetWithinRow: 0 }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => 2000,
          }
          let lastKeep = -1
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => false,
            onTrimOldMessages: (k) => { lastKeep = k },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          div.setScrollTop(2000)
          hook.handlers.onScroll() // captures anchor = m1
          expect(hook.atBottom()).toBe(false)

          // Replace the window so m1 (the anchor) is gone but m10 is present in the
          // MIDDLE. Length changes so the auto-scroll signature wakes the trim.
          const replaced = mkIdMsgs(5, MAX_LOADED_CHAT_MESSAGES + 1) // m5..m(MAX+5)
          setMessages(replaced)
          await Promise.resolve()
          await Promise.resolve()

          // m1 displaced -> recapture lands on m10 (a middle row), so keepNewest is
          // the viewport cap (rows from m10 down), NOT the whole-window fallback and
          // NOT 0. Without the recapture this would protect the whole window.
          const m10Idx = replaced.findIndex(m => m.id === 'm10')
          expect(m10Idx).toBeGreaterThan(0)
          expect(lastKeep).toBe(replaced.length - m10Idx)
          expect(lastKeep).toBeLessThan(replaced.length)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('keepNewest retains the older pre-fetch buffer ABOVE the viewport, beyond the base cap', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const rowH = 100
          const div = makeFakeScrollDiv()
          // clientHeight 2000 -> bufferTargetPx = 3 * 2000 = 6000px = 60 rows of buffer.
          div.setClientHeight(2000)
          div.setScrollHeight(40000)
          div.setScrollTop(28000) // scrolled up; viewport top at row index 280 (m281)
          const mkIdMsgs = (n: number): AgentChatMessage[] =>
            Array.from({ length: n }, (_, i) => ({ id: `m${i + 1}`, seq: BigInt(i + 1) } as AgentChatMessage))
          const [messages, setMessages] = createSignal<AgentChatMessage[]>(mkIdMsgs(400))
          const [streamingText] = createSignal('')
          // A virtualizer whose anchorAt maps a scroll offset to the row at that offset
          // (rows are rowH tall), so the buffer-top anchor (bufferTargetPx above the
          // viewport top) resolves to a row STRICTLY above the viewport anchor.
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => 40000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: (y: number) => ({ id: `m${Math.max(0, Math.floor(y / rowH)) + 1}`, offsetWithinRow: 0 }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: (a: { id: string }) => (Number(a.id.slice(1)) - 1) * rowH,
          }
          let lastKeep = -1
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => false,
            onTrimOldMessages: (k) => { lastKeep = k },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Scroll up: capture the viewport-midpoint anchor m291 (index 290).
          div.setScrollTop(28000)
          hook.handlers.onScroll()
          expect(hook.atBottom()).toBe(false)

          // A live message appends at the tail (window now 401 rows). The trim keeps
          // from the row bufferTargetPx (6000px = 60 rows) ABOVE the viewport top --
          // index 220 (m221) -- down to the tail: 401 - 220 = 181. Crucially that
          // EXCEEDS the base cap (150), so the store (Math.max(150, keepNewest)) actually
          // retains the buffer. The OLD viewport-only keepNewest would be 401 - 280 = 121,
          // which clamps to just the base 150 -- reaping the 31-row older buffer the
          // filler maintains and forcing a refetch. The midpoint anchor alone would keep
          // even less (401 - 290 = 111), so this scenario is production-
          // observable: 181 kept vs 150.
          setMessages(mkIdMsgs(401))
          await Promise.resolve()
          await Promise.resolve()

          expect(lastKeep).toBe(181)
          expect(lastKeep).toBeGreaterThan(MAX_LOADED_CHAT_MESSAGES) // beyond the base cap -> real retention
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('applies the lean base cap (keepNewest 0) while the tab is hidden (clientHeight 0)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(0) // hidden/inactive tab -- viewport & buffer unmeasurable
          div.setScrollHeight(5000)
          div.setScrollTop(2000)
          const mkIdMsgs = (n: number): AgentChatMessage[] =>
            Array.from({ length: n }, (_, i) => ({ id: `m${i + 1}`, seq: BigInt(i + 1) } as AgentChatMessage))
          const [messages, setMessages] = createSignal<AgentChatMessage[]>(mkIdMsgs(MAX_LOADED_CHAT_MESSAGES))
          const [streamingText] = createSignal('')
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => 5000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            // Would resolve a buffer-top row (from the stale scrollTop) if consulted --
            // the clientHeight===0 guard must skip the buffer-aware computation entirely.
            anchorAt: (y: number) => ({ id: `m${Math.max(0, Math.floor(y / 100)) + 1}`, offsetWithinRow: 0 }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => 2000,
          }
          let lastKeep = -1
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasNewerMessages: () => false,
            onTrimOldMessages: (k) => { lastKeep = k },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // A tail append fires the trim while the tab is hidden. The viewport/buffer
          // can't be measured, so keepNewest is the lean base (0) -- NOT a buffer-aware
          // value derived from the stale scrollTop (which would be ~131 here).
          setMessages(mkIdMsgs(MAX_LOADED_CHAT_MESSAGES + 1))
          await Promise.resolve()
          await Promise.resolve()

          expect(lastKeep).toBe(0)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))
})
