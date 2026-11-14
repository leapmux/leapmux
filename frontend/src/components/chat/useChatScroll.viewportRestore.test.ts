import type { ChatScrollState, ChatScrollVirtualizer } from './useChatScroll'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { createRoot, createSignal, onCleanup } from 'solid-js'
import { insert } from 'solid-js/web'
import { describe, expect, it, vi } from 'vitest'
import {
  triggerResizeObserversSync,
} from '~/test-support/resizeObserverStub'
import { useChatScroll } from './useChatScroll'
import { installScrollTestEnv, makeFakeScrollDiv, makeRowVirtualizer, makeStubVirtualizer, mountScrollHarness, virtualizerNoOps } from './useChatScroll.testkit'

installScrollTestEnv()

describe('useChatScroll viewport restore', () => {
  it('jumps to the live tail on restore when the saved anchor was trimmed away', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setScrollHeight(5000)
          div.setClientHeight(0) // hidden tab
          const [messages] = createSignal<AgentChatMessage[]>([])
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
          reject(e instanceof Error
            ? e
            : new Error(String(e)))
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
          reject(e instanceof Error
            ? e
            : new Error(String(e)))
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
          reject(e instanceof Error
            ? e
            : new Error(String(e)))
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
            ...virtualizerNoOps(),
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
          // The restore itself pages no older history: the buffer fill runs on a
          // scroll event or a message change, and neither follows the restore here.
          expect(olderLoads).toBe(0)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error
            ? e
            : new Error(String(e)))
        }
      })
    }))
})

describe('useChatScroll restore under a layout shift', () => {
  it('loads older history after a restore near the top', async () => {
    const onLoadOlderMessages = vi.fn()
    const [saved, setSaved] = createSignal<ChatScrollState | undefined>({
      anchor: { id: 'r1', offsetWithinRow: 0 },
      atBottom: false,
      hasMoreNewer: false,
    })
    const h = mountScrollHarness({
      clientHeight: 0,
      hookOptions: {
        hasOlderMessages: () => true,
        onLoadOlderMessages,
        savedViewportScroll: saved,
        onClearSavedViewportScroll: () => setSaved(undefined),
      },
    })
    h.div.setClientHeight(500)
    await h.resizeFlush()
    expect(h.div.getScrollTop()).toBe(100)
    // The scroll event of the restore write.
    h.hook.handlers.onScroll()

    expect(onLoadOlderMessages).toHaveBeenCalledOnce()
    h.dispose()
  })

  it('restores a hidden reader under a shift', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    h.scrollTo(1990)
    h.changeAbove(300)
    expect(h.virt.layoutShift()).toBe(-300)

    await h.hideThenShow()

    expect(h.virt.layoutShift()).toBe(0)
    expect(h.div.getScrollTop()).toBe(2290)
    h.dispose()
  })

  it('restores the row when the position changed while the pane was hidden', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    h.scrollTo(1990)
    h.changeAbove(300)
    h.div.setClientHeight(0)
    await h.resizeFlush()
    h.div.setScrollTop(0)
    h.div.setClientHeight(500)
    await h.resizeFlush()

    expect(h.virt.layoutShift()).toBe(0)
    expect(h.div.getScrollTop()).toBe(2290)
    h.dispose()
  })

  it('restores the row when a scroll event of the browser arrives before the flush', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    h.scrollTo(1990)
    h.div.setClientHeight(0)
    await h.resizeFlush()
    h.div.setClientHeight(500)
    triggerResizeObserversSync()
    h.div.setScrollTop(0)
    h.hook.handlers.onScroll()
    await h.frame()

    // A restore that reads the anchor at the flush leaves 0.
    expect(h.div.getScrollTop()).toBe(1990)
    h.dispose()
  })

  it('restores the anchor of a reader who left at the loaded bottom of a paged-away window', async () => {
    const onJumpToLatest = vi.fn()
    const [saved, setSaved] = createSignal<ChatScrollState | undefined>()
    const h = mountScrollHarness({
      hasNewer: true,
      hookOptions: {
        onJumpToLatest,
        savedViewportScroll: saved,
        onClearSavedViewportScroll: () => setSaved(undefined),
      },
    })
    await h.anchoredAt(4000)
    h.scrollTo(4500)
    setSaved(h.hook.getScrollState())

    await h.hideThenShow()

    // A save that records the position as at-bottom makes the show jump to the latest page.
    expect(onJumpToLatest).not.toHaveBeenCalled()
    expect(h.div.getScrollTop()).toBe(4500)
    h.dispose()
  })

  it('keeps the anchor when rows change between the show and its flush', async () => {
    const h = mountScrollHarness()
    await h.anchoredAt(2000)
    h.scrollTo(1990)
    h.div.setClientHeight(0)
    h.div.setScrollTop(0)
    await h.resizeFlush()
    h.div.setClientHeight(500)
    // The geometry effect runs its position check at the position that the hook did not see.
    h.changeBelow(100)
    await h.resizeFlush()

    expect(h.div.getScrollTop()).toBe(1990)
    h.dispose()
  })
})

describe('useChatScroll unmount save + visible-mount restore', () => {
  // A tile split/merge or workspace switch REMOUNTS ChatView over a still-populated
  // store: the old instance saves its final viewport state from onCleanup
  // (onSaveViewportScroll) and the fresh instance -- which mounts VISIBLE, so the
  // hidden->visible RO path can never fire -- restores it at mount (restoreOnMount),
  // beating the default tail-stick placement.
  it('round-trips a scrolled-up reading position across unmount and visible remount', () =>
    new Promise<void>((resolve, reject) => {
      let savedState: ChatScrollState | undefined
      createRoot(async (disposeA) => {
        try {
          const divA = makeFakeScrollDiv()
          divA.setClientHeight(500)
          divA.setScrollHeight(3000)
          const { virt: virtA } = makeRowVirtualizer([500, 500, 500, 500, 500, 500])
          const [messages] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
          const hookA = useChatScroll({
            virtualizer: virtA,
            messages,
            onSaveViewportScroll: (state) => { savedState = state },
          })
          hookA.attachListRef(divA.el)
          // Mount placement sticks the populated fresh mount to the tail...
          await Promise.resolve()
          expect(divA.getScrollTop()).toBe(2500)
          // ...then the reader scrolls up into history (a genuine scroll event).
          divA.setScrollTop(1000)
          hookA.handlers.onScroll()
          disposeA()

          // The unmount saved the final position, anchored to the viewport-top row.
          expect(savedState).toBeDefined()
          expect(savedState!.atBottom).toBe(false)
          expect(savedState!.anchor?.id).toBe('g0_2')
        }
        catch (e) {
          disposeA()
          reject(e instanceof Error
            ? e
            : new Error(String(e)))
          return
        }

        createRoot(async (disposeB) => {
          try {
            // The remount: a FRESH container at scrollTop 0 over the same window
            // (same row ids -> the saved anchor resolves against the new offset map).
            const divB = makeFakeScrollDiv()
            divB.setClientHeight(500)
            divB.setScrollHeight(3000)
            const { virt: virtB } = makeRowVirtualizer([500, 500, 500, 500, 500, 500])
            const [messagesB] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
            let cleared = false
            const hookB = useChatScroll({
              virtualizer: virtB,
              messages: messagesB,
              savedViewportScroll: () => savedState,
              onClearSavedViewportScroll: () => { cleared = true },
            })
            hookB.attachListRef(divB.el)
            await Promise.resolve()
            await Promise.resolve()

            // Restored to the saved row -- NOT the default tail stick -- and consumed.
            expect(divB.getScrollTop()).toBe(1000)
            expect(hookB.atBottom()).toBe(false)
            expect(cleared).toBe(true)
            disposeB()
            resolve()
          }
          catch (e) {
            disposeB()
            reject(e instanceof Error
              ? e
              : new Error(String(e)))
          }
        })
      })
    }))

  it('does not save on unmount when the pane is hidden (a tab-switch save must survive)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(0) // hidden tab
          div.setScrollHeight(3000)
          const [messages] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
          let saves = 0
          const hook = useChatScroll({
            virtualizer: makeStubVirtualizer(),
            messages,
            onSaveViewportScroll: () => { saves++ },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          dispose()

          // An unmeasurable pane carries no position: the host's existing saved
          // entry (written when the tab was switched away) must not be clobbered.
          expect(saves).toBe(0)
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error
            ? e
            : new Error(String(e)))
        }
      })
    }))

  it('does not read the host pagination accessor while its owner disposes', () => {
    const div = makeFakeScrollDiv()
    div.setClientHeight(500)
    div.setScrollHeight(3000)
    const [messages] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
    let disposalStarted = false
    let reentered = false
    let savedState: ChatScrollState | undefined
    let disposeRoot!: () => void

    createRoot((dispose) => {
      disposeRoot = dispose
      const hook = useChatScroll({
        virtualizer: makeStubVirtualizer(),
        messages,
        hasNewerMessages: () => {
          if (disposalStarted && !reentered) {
            reentered = true
            dispose()
          }
          return true
        },
        onSaveViewportScroll: (state) => { savedState = state },
      })
      hook.attachListRef(div.el)
    })

    disposalStarted = true
    expect(() => disposeRoot()).not.toThrow()
    expect(reentered).toBe(false)
    // A mount on a window that is paged away and never scrolled holds no request for the tail.
    expect(savedState).toMatchObject({ atBottom: false, hasMoreNewer: true })
  })

  it('consumes an at-bottom save on a visible mount (stick + clear)', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(3000)
          const { virt } = makeRowVirtualizer([500, 500, 500, 500, 500, 500])
          const [messages] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
          const [saved] = createSignal<ChatScrollState | undefined>({ atBottom: true, hasMoreNewer: false })
          let cleared = false
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            savedViewportScroll: saved,
            onClearSavedViewportScroll: () => { cleared = true },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()

          expect(div.getScrollTop()).toBe(2500)
          expect(hook.atBottom()).toBe(true)
          // The mount restore CONSUMED the save (vs the default placement, which
          // would leave it lingering for a later hidden->visible to misapply).
          expect(cleared).toBe(true)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error
            ? e
            : new Error(String(e)))
        }
      })
    }))

  it('jumps to the live tail on a visible mount of an at-bottom save that was windowed away', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(500)
          div.setScrollHeight(3000)
          const { virt } = makeRowVirtualizer([500, 500, 500, 500, 500, 500])
          const [messages] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
          const [saved] = createSignal<ChatScrollState | undefined>({ atBottom: true, hasMoreNewer: true })
          let jumped = false
          let cleared = false
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            hasNewerMessages: () => true,
            onJumpToLatest: () => { jumped = true },
            savedViewportScroll: saved,
            onClearSavedViewportScroll: () => { cleared = true },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // The saved in-memory bottom is NOT the live tail: re-fetch the tail
          // (forceScrollToBottom jumps when hasNewerMessages) instead of sticking
          // to the stale loaded bottom.
          expect(jumped).toBe(true)
          expect(cleared).toBe(true)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error
            ? e
            : new Error(String(e)))
        }
      })
    }))

  it('leaves a saved state for the hidden->visible path when the mount is hidden', () =>
    new Promise<void>((resolve, reject) => {
      createRoot(async (dispose) => {
        try {
          const div = makeFakeScrollDiv()
          div.setClientHeight(0) // hidden tab: the RO hidden->visible path owns the restore
          div.setScrollHeight(3000)
          const { virt } = makeRowVirtualizer([500, 500, 500, 500, 500, 500])
          const [messages] = createSignal<AgentChatMessage[]>([{} as AgentChatMessage])
          const [saved] = createSignal<ChatScrollState | undefined>({
            anchor: { id: 'g0_2', seq: 3n, offsetWithinRow: 0 },
            atBottom: false,
            hasMoreNewer: false,
          })
          let cleared = false
          const hook = useChatScroll({
            virtualizer: virt,
            messages,
            savedViewportScroll: saved,
            onClearSavedViewportScroll: () => { cleared = true },
          })
          hook.attachListRef(div.el)
          await Promise.resolve()
          await Promise.resolve()

          // Not consumed at mount; the tab-switch (hidden->visible) restore -- covered
          // by the suite above -- picks it up when the pane becomes visible.
          expect(cleared).toBe(false)
          expect(div.getScrollTop()).toBe(0)
          dispose()
          resolve()
        }
        catch (e) {
          dispose()
          reject(e instanceof Error
            ? e
            : new Error(String(e)))
        }
      })
    }))

  it('framework contract: onCleanup runs while the unmounting DOM is still connected', () => {
    // The unmount save reads clientHeight/scrollTop from onCleanup, which only works
    // if Solid disposes owners BEFORE detaching their DOM. Pin that contract: if a
    // Solid upgrade ever flips the order, the save would silently degrade to a no-op
    // (clientHeight 0 -> getScrollState undefined), and this test names the cause.
    const host = document.createElement('div')
    document.body.appendChild(host)
    try {
      const [show, setShow] = createSignal(true)
      let connectedAtCleanup: boolean | undefined
      const Inner = () => {
        const el = document.createElement('div')
        el.textContent = 'content'
        onCleanup(() => {
          connectedAtCleanup = el.isConnected
        })
        return el
      }
      // The same reactive insert a compiled <Show>/component boundary lowers to:
      // toggling the accessor disposes the branch owner, then swaps the DOM. The
      // toggle happens OUTSIDE the root body -- createRoot batches writes made
      // inside it, deferring the re-run past these assertions.
      const dispose = createRoot((d) => {
        insert(host, () => (show()
          ? Inner()
          : null))
        return d
      })
      expect(host.textContent).toContain('content')
      setShow(false)
      expect(connectedAtCleanup).toBe(true)
      expect(host.textContent).not.toContain('content')
      dispose()
    }
    finally {
      host.remove()
    }
  })
})
