import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { describe, expect, it, vi } from 'vitest'
import { MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { deferred } from '~/test-support/async'
import { createChatSeek } from './chatSeek'
import { makeFakeScrollDiv, makeScrollContext } from './useChatScroll.testkit'

/** One message for each seq of `seqs`, with the id `m${seq}`. */
function messagesAt(seqs: readonly number[]): AgentChatMessage[] {
  return seqs.map(seq => ({
    id: `m${seq}`,
    seq: BigInt(seq),
    source: MessageSource.USER,
  } as AgentChatMessage))
}

/**
 * A seek over a fake element with `clientHeight` 500, and spies on the context primitives
 * that it calls. `calls` records the order of the calls. The loaded window holds the seqs
 * 100 to 104. The fetch of an out-of-window seek stays pending until the test settles
 * `fetch`, and `swapWindow` replaces the loaded window, as the paginator does before the
 * fetch resolves.
 */
function setup(opts: { hasNewer?: boolean } = {}) {
  const div = makeFakeScrollDiv()
  div.setClientHeight(500)
  div.setScrollHeight(5000)
  div.setScrollTop(2000)
  const calls: string[] = []
  const spy = (name: string) => vi.fn(() => {
    calls.push(name)
  })
  const fetch = deferred<void>()
  const onJumpToSeq = vi.fn((_seq: bigint, _signal?: AbortSignal) => {
    calls.push('onJumpToSeq')
    return fetch.promise
  })
  const onSaveViewportScroll = vi.fn()
  let messages = messagesAt([100, 101, 102, 103, 104])
  const seek = createChatSeek(makeScrollContext({
    getEl: () => div.el,
    hasNewerMessages: () => opts.hasNewer ?? false,
    cancelScrollAnimation: spy('cancelScrollAnimation'),
    retakeScrollControl: spy('retakeScrollControl'),
    rearmScrollBufferFiller: spy('rearmScrollBufferFiller'),
    leaveFollow: spy('leaveFollow'),
    checkPosition: spy('checkPosition'),
    // A landing that moves the view.
    placeAt: vi.fn(() => {
      calls.push('placeAt')
      return true
    }),
  }), {
    messages: () => messages,
    onJumpToSeq,
    onSaveViewportScroll,
  })
  return {
    div,
    calls,
    fetch,
    onJumpToSeq,
    onSaveViewportScroll,
    seek,
    swapWindow: (seqs: readonly number[]) => {
      messages = messagesAt(seqs)
    },
  }
}

describe('createChatSeek', () => {
  it('prepares the hook for the swap of the window before an out-of-window fetch', () => {
    const { calls, seek } = setup()

    void seek.jumpToSeq(5n)

    // Each step comes before the fetch: no animation, pause, per-window state of the
    // filler, or request for the tail outlasts the old window.
    expect(calls.at(-1)).toBe('onJumpToSeq')
    expect(calls.slice(0, -1).sort()).toEqual([
      'cancelScrollAnimation',
      'leaveFollow',
      'rearmScrollBufferFiller',
      'retakeScrollControl',
    ])
  })

  it('re-takes control, and keeps the per-window state of the filler, for an in-window seek', async () => {
    const { calls, seek, onJumpToSeq } = setup()

    expect(await seek.jumpToSeq(102n)).toBe(true)

    expect(calls).toContain('retakeScrollControl')
    expect(calls).toContain('placeAt')
    expect(calls).not.toContain('rearmScrollBufferFiller')
    expect(onJumpToSeq).not.toHaveBeenCalled()
  })

  it('saves the target for the next show when the pane is hidden at the landing', async () => {
    for (const hasNewer of [false, true]) {
      const { div, calls, fetch, seek, swapWindow, onSaveViewportScroll } = setup({ hasNewer })
      const landing = seek.jumpToSeq(5n)
      swapWindow([3, 4, 5, 6, 7])
      // The tab switches away while the page loads.
      div.setClientHeight(0)
      fetch.resolve()

      expect(await landing).toBe(false)

      expect(calls).not.toContain('placeAt')
      expect(onSaveViewportScroll).toHaveBeenCalledOnce()
      expect(onSaveViewportScroll).toHaveBeenCalledWith({
        anchor: { id: 'm5', offsetWithinRow: 0, seq: 5n },
        atBottom: false,
        hasMoreNewer: hasNewer,
      })
    }
  })

  it('saves nothing when the pane is hidden at the landing and the window holds no row', async () => {
    const { div, fetch, seek, swapWindow, onSaveViewportScroll } = setup()
    const landing = seek.jumpToSeq(5n)
    swapWindow([])
    div.setClientHeight(0)
    fetch.resolve()

    expect(await landing).toBe(false)

    expect(onSaveViewportScroll).not.toHaveBeenCalled()
  })
})
