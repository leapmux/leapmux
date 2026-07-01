import type { ChatScrollState, ChatScrollVirtualizer } from './useChatScroll'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import {
  triggerResizeObserversSync,
} from '../../../tests/unit/helpers/resizeObserverStub'
import { useChatScroll } from './useChatScroll'
import { installScrollTestEnv, makeFakeScrollDiv, makeStubVirtualizer, measurementDeferralNoOps } from './useChatScroll.testkit'

installScrollTestEnv()

describe('usechatscroll viewport restore', () => {
  it('jumps to the live tail on restore when the saved anchor was trimmed away', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(0) // hidden tab
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          // Saved while scrolled away from the tail; the anchored row (id m42)
          // was trimmed out of the window while hidden, so the stub's
          // scrollTopForAnchor returns null (the anchor no longer resolves).
          const [saved] = createSignal<ChatScrollState | undefined>({
            anchor: { id: 'm42', offsetWithinRow: 10 },
            atBottom: false,
            hasMoreNewer: true,
          })
          let jumped = false
          let cleared = false
          const onJumpToLatest = () => {
            jumped = true
          }
          const onClearSavedViewportScroll = () => {
            cleared = true
          }
          const hook = useChatScroll({
            virtualizer: makeStubVirtualizer(),
            messages,
            streamingText,
            hasNewerMessages: () => true,
            onJumpToLatest,
            savedViewportScroll: saved,
            onClearSavedViewportScroll,
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Tab becomes visible: the hidden->visible transition runs the
          // restore path. The anchor can't resolve and we were windowed away,
          // so it jumps to the live tail instead of clamping to the top.
          div.setClientHeight(500)
          triggerResizeObserversSync()
          await Promise.resolve()
          await Promise.resolve()

          expect(jumped).toBe(true)
          expect(cleared).toBe(true)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('jumps to the live tail on restore when the saved at-bottom view was windowed away', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(0) // hidden tab
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          // Saved at the IN-MEMORY bottom while still windowed away from the live
          // tail (hasMoreNewer): the in-memory bottom is NOT the real bottom, so
          // restore must re-fetch the tail rather than stick to the stale window.
          const [saved] = createSignal<ChatScrollState | undefined>({
            atBottom: true,
            hasMoreNewer: true,
          })
          let jumped = false
          let cleared = false
          const hook = useChatScroll({
            virtualizer: makeStubVirtualizer(),
            messages,
            streamingText,
            hasNewerMessages: () => true,
            onJumpToLatest: () => { jumped = true },
            savedViewportScroll: saved,
            onClearSavedViewportScroll: () => { cleared = true },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Tab becomes visible: the hidden->visible restore path runs.
          div.setClientHeight(500)
          triggerResizeObserversSync()
          await Promise.resolve()
          await Promise.resolve()

          // forceScrollToBottom jumps to the live tail (not a plain stickToBottom).
          expect(jumped).toBe(true)
          expect(cleared).toBe(true)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('does not auto-load older from a passive scroll after a NEAR-TOP anchor restore', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(0) // hidden tab
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          // The saved anchor RESOLVES to a near-top scrollTop (100 < clientHeight/2).
          const [saved] = createSignal<ChatScrollState | undefined>({
            anchor: { id: 'm10', offsetWithinRow: 0 },
            atBottom: false,
            hasMoreNewer: false,
          })
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => 5000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => ({ id: 'm10', offsetWithinRow: 0 }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => 100, // resolves near the top
          }
          let olderLoads = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasOlderMessages: () => true,
            onLoadOlderMessages: () => { olderLoads++ },
            savedViewportScroll: saved,
            onClearSavedViewportScroll: () => {},
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Tab becomes visible: restore resolves the anchor to scrollTop 100.
          div.setClientHeight(500)
          triggerResizeObserversSync()
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(100) // restored near the top

          // A passive scroll event near the top must NOT auto-page older history --
          // the restore landed us here, the user didn't scroll to the top. Without
          // arming the one-shot suppression on the anchor-resolve branch, this would
          // fire loadOlderMessages immediately after restore.
          div.setScrollTop(50)
          hook.handlers.onScroll()
          expect(olderLoads).toBe(0)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('still fills the NEWER side from a passive scroll after a near-top restore (suppression gates only older)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          // Small window: both edges sit inside the buffer target (3 * clientHeight),
          // so a fill would page BOTH sides -- letting us prove the older side stays
          // suppressed while the newer side keeps loading (the stall fix).
          const div = makeFakeScrollDiv()
          div.setScrollHeight(1000)
          div.setClientHeight(0) // hidden tab
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          // Saved anchor resolves NEAR THE TOP (100 < clientHeight/2), arming the
          // one-shot older-load suppression on restore.
          const [saved] = createSignal<ChatScrollState | undefined>({
            anchor: { id: 'm10', offsetWithinRow: 0 },
            atBottom: false,
            hasMoreNewer: true,
          })
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => 1000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => ({ id: 'm10', offsetWithinRow: 0 }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => 100, // resolves near the top
          }
          let olderLoads = 0
          let newerLoads = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasOlderMessages: () => true,
            hasNewerMessages: () => true,
            onLoadOlderMessages: () => { olderLoads++ },
            onLoadNewerMessages: () => { newerLoads++ },
            savedViewportScroll: saved,
            onClearSavedViewportScroll: () => {},
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Tab becomes visible: restore resolves the anchor to scrollTop 100 and
          // arms the older-load suppression. No fill has run yet.
          div.setClientHeight(500)
          triggerResizeObserversSync()
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(100)
          expect(newerLoads).toBe(0)

          // A passive scroll (to a pixel != the restored 100, so it's not an echo):
          // the older side stays suppressed, but the newer buffer must still top up.
          // Before the fix the whole fill was skipped here and the list stalled short
          // of the live tail until the user scrolled all the way back to the top.
          div.setScrollTop(150)
          hook.handlers.onScroll()
          expect(olderLoads).toBe(0) // older still suppressed
          expect(newerLoads).toBeGreaterThan(0) // newer side keeps loading
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('keeps the post-restore older-suppression through an unrelated (non-restore) resize', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(0) // hidden tab
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const [saved] = createSignal<ChatScrollState | undefined>({
            anchor: { id: 'm10', offsetWithinRow: 0 },
            atBottom: false,
            hasMoreNewer: false,
          })
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => 5000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => ({ id: 'm10', offsetWithinRow: 0 }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => 100, // resolves near the top
          }
          let olderLoads = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasOlderMessages: () => true,
            onLoadOlderMessages: () => { olderLoads++ },
            savedViewportScroll: saved,
            onClearSavedViewportScroll: () => {},
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Restore on show: lands near the top (100 < clientHeight/2), arms the
          // one-shot older-load suppression.
          div.setClientHeight(500)
          triggerResizeObserversSync()
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(100)

          // An UNRELATED, non-restore resize (editor grow / keyboard) must NOT clear the
          // one-shot suppression -- the regression the resize-time clear removal fixes.
          div.setClientHeight(600)
          triggerResizeObserversSync()
          await Promise.resolve()
          await Promise.resolve()

          // A passive scroll still WITHIN the near-top band (50 < 600/2) stays suppressed.
          div.setScrollTop(50)
          hook.handlers.onScroll()
          expect(olderLoads).toBe(0)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('resumes older pre-fetch once the user scrolls OUT of the near-top band after a restore', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(0) // hidden tab
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const [saved] = createSignal<ChatScrollState | undefined>({
            anchor: { id: 'm10', offsetWithinRow: 0 },
            atBottom: false,
            hasMoreNewer: false,
          })
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => 5000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => ({ id: 'm10', offsetWithinRow: 0 }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => 100,
          }
          let olderLoads = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasOlderMessages: () => true,
            onLoadOlderMessages: () => { olderLoads++ },
            savedViewportScroll: saved,
            onClearSavedViewportScroll: () => {},
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Restore near the top (100), arming the suppression.
          div.setClientHeight(500)
          triggerResizeObserversSync()
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(100)

          // Scroll DOWN out of the near-top band (300 >= clientHeight/2 = 250): the
          // restored landing is abandoned, so older pre-fetch resumes (no fling-up stall).
          div.setScrollTop(300)
          hook.handlers.onScroll()
          await Promise.resolve()
          expect(olderLoads).toBeGreaterThan(0)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('clears the older-suppression when a trim/measure-shrink makes content fit (no resize, no scroll)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          // The wedge this guards: a near-top restore arms the one-shot older-load
          // suppression, then a window trim / row re-measure shrinks the content until
          // it fits the viewport -- changing virt.totalHeight()/geometryVersion() but NOT
          // the container box (no resize) and emitting no scroll event. Neither
          // handleResize nor handleScroll runs, so without the geometry-effect clear the
          // suppression sticks forever and older history never background-loads.
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(0) // hidden tab
          const [messages, setMessages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const [saved] = createSignal<ChatScrollState | undefined>({
            anchor: { id: 'm10', offsetWithinRow: 0 },
            atBottom: false,
            hasMoreNewer: false,
          })
          const [total, setTotal] = createSignal(5000)
          const [geom, setGeom] = createSignal(0)
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => total(),
            geometryVersion: () => geom(),
            updateViewport: () => {},
            anchorAt: () => ({ id: 'm10', offsetWithinRow: 0 }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => 100, // resolves near the top
          }
          let olderLoads = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasOlderMessages: () => true,
            onLoadOlderMessages: () => { olderLoads++ },
            savedViewportScroll: saved,
            onClearSavedViewportScroll: () => {},
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Restore on show: lands near the top (100 < clientHeight/2), arming the
          // one-shot suppression. Content is still scrollable (5000 > 500).
          div.setClientHeight(500)
          triggerResizeObserversSync()
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(100)

          // CONTROL: a window change re-runs the buffer filler. The older side is still
          // suppressed, so it does NOT load older despite a near-top deficit. (We never
          // call onScroll, so the scroll-path clear can't be the thing under test.)
          setMessages(m => [...m, { id: 'a', seq: 1n } as AgentChatMessage])
          await Promise.resolve()
          await Promise.resolve()
          expect(olderLoads).toBe(0)

          // A trim / measurement shrink makes the content fit (scrollHeight <=
          // clientHeight, so maxScrollTop 0) and bumps geometry -- the geometry effect
          // must clear the now-pointless suppression.
          div.setScrollHeight(400)
          setTotal(400)
          setGeom(v => v + 1)
          await Promise.resolve()
          await Promise.resolve()

          // TEST: the next window change runs the filler with the suppression cleared, so
          // older history finally loads. Before the fix it stayed wedged off forever.
          setMessages(m => [...m, { id: 'b', seq: 2n } as AgentChatMessage])
          await Promise.resolve()
          await Promise.resolve()
          expect(olderLoads).toBeGreaterThan(0)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('resumes older pre-fetch when a scrollbar drag reaches the very TOP after a restore (no wheel/key)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(0) // hidden tab
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          const [saved] = createSignal<ChatScrollState | undefined>({
            anchor: { id: 'm10', offsetWithinRow: 0 },
            atBottom: false,
            hasMoreNewer: false,
          })
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => 5000,
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => ({ id: 'm10', offsetWithinRow: 0 }),
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => 100,
          }
          let olderLoads = 0
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            hasOlderMessages: () => true,
            onLoadOlderMessages: () => { olderLoads++ },
            savedViewportScroll: saved,
            onClearSavedViewportScroll: () => {},
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Restore near the top (100), arming the suppression.
          div.setClientHeight(500)
          triggerResizeObserversSync()
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(100)

          // A scrollbar-thumb DRAG up to the very top -- plain `scroll` events, NOT
          // wheel/key/touch, so it never reaches tryLoadOlderOnExplicitTopIntent. The
          // drag fires a sequence: the first event seeds the upward direction baseline at
          // the restored position; the drag then continues to the very top (0), still
          // WITHIN the near-top band (0 < 250) so the band-exit clear does not apply.
          // Reaching the top WHILE SCROLLING UP must still clear the older-suppression so
          // the filler can page up; without it the reader wedges unable to load older.
          div.setScrollTop(100)
          hook.handlers.onScroll()
          expect(olderLoads).toBe(0) // the in-band seed event must NOT clear yet
          div.setScrollTop(0)
          hook.handlers.onScroll()
          await Promise.resolve()
          expect(olderLoads).toBeGreaterThan(0)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('restores a saved raw scrollTop fallback when no row anchor resolves', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(0) // hidden tab
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          // Saved while scrolled up in an all-hidden window: no anchor, but a raw
          // scrollTop fallback (no virtual spacer there, so no drift to fear).
          const [saved] = createSignal<ChatScrollState | undefined>({
            rawScrollTop: 1200,
            atBottom: false,
            hasMoreNewer: false,
          })
          const hook = useChatScroll({
            // anchorAt / scrollTopForAnchor return null -> the anchor never resolves.
            virtualizer: makeStubVirtualizer(),
            messages,
            streamingText,
            savedViewportScroll: saved,
            onClearSavedViewportScroll: () => {},
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Tab becomes visible: restore falls back to the raw scrollTop (clamped to
          // scrollHeight - clientHeight = 4500), not a snap to the live tail.
          div.setClientHeight(500)
          triggerResizeObserversSync()
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(1200)
          expect(hook.atBottom()).toBe(false)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error ? e : new Error(String(e)))
        }
      })
    }))

  it('discards a stale raw scrollTop and clamps to the top when virtual rows appeared between save and restore', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(0) // hidden tab
          const [messages] = createSignal<AgentChatMessage[]>([])
          const [streamingText] = createSignal('')
          // Saved while all-hidden (raw fallback, no anchor). But by restore time
          // the window has virtual content: totalHeight > 0 means a spacer now sits
          // where the raw offset pointed, so 1200 no longer maps to that content.
          const [saved] = createSignal<ChatScrollState | undefined>({
            rawScrollTop: 1200,
            atBottom: false,
            hasMoreNewer: false,
          })
          let olderLoads = 0
          const virt: ChatScrollVirtualizer = {
            ...measurementDeferralNoOps(),
            totalHeight: () => 3000, // virtual rows exist now
            geometryVersion: () => 0,
            updateViewport: () => {},
            anchorAt: () => null, // the all-hidden save carried no anchor
            scrollTopNearAnchor: () => null,
            scrollTopForAnchor: () => null,
          }
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            streamingText,
            savedViewportScroll: saved,
            onClearSavedViewportScroll: () => {},
            hasOlderMessages: () => true,
            onLoadOlderMessages: () => { olderLoads++ },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Tab becomes visible: the stale raw 1200 is discarded; restore clamps to
          // the top (best-effort, since there's no anchor to resolve) instead of
          // landing on the wrong rows.
          div.setClientHeight(500)
          triggerResizeObserversSync()
          await Promise.resolve()
          await Promise.resolve()
          expect(div.getScrollTop()).toBe(0)
          expect(hook.atBottom()).toBe(false)
          // The clamp-to-top fallback arms the one-shot auto-load-older suppression,
          // so the restore itself doesn't page older history.
          expect(olderLoads).toBe(0)
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
