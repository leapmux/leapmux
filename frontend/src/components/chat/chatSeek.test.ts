import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ScrollAnchor } from '~/stores/chatTypes'
import { describe, expect, it, vi } from 'vitest'
import { MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { deferred } from '~/test-support/async'
import { nearestServerRowIndexBySeq } from './chatScrollAnchor'
import { createChatSeek } from './chatSeek'
import { makeFakeScrollDiv, makeScrollContext, makeStubVirtualizer } from './useChatScroll.testkit'

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
 * 100 to 104. The fetch of each out-of-window seek stays pending until the test settles
 * it: `fetch` is the fetch of the first seek, and `fetches` holds each one in order. The
 * test resolves a fetch with `true` for a page that replaced the window. `swapWindow`
 * replaces the loaded window, as the paginator does before the fetch resolves. `landings`
 * holds the anchor of each landing. Each message is a loaded visible row: the
 * `loadedRows` of the context give the id of the message nearest a seq. The context shows
 * each requested place at once (see makeScrollContext).
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
  const fetches = [
    deferred<boolean>(),
  ]
  const onJumpToSeq = vi.fn((_seq: bigint, _signal?: AbortSignal) => {
    calls.push('onJumpToSeq')
    // The first seek takes the fetch that exists already, and each later one a new fetch.
    const fetch = onJumpToSeq.mock.calls.length === 1
      ? fetches[0]!
      : deferred<boolean>()
    if (!fetches.includes(fetch))
      fetches.push(fetch)
    return fetch.promise
  })
  const onSaveViewportScroll = vi.fn()
  let messages = messagesAt([100, 101, 102, 103, 104])
  const landings: ScrollAnchor[] = []
  const ctx = makeScrollContext({
    getEl: () => div.el,
    virt: {
      ...makeStubVirtualizer(),
      scrollTopNearAnchor: (anchor) => {
        landings.push(anchor)
        return 1000
      },
    },
    hasNewerMessages: () => opts.hasNewer ?? false,
    cancelScrollAnimation: spy('cancelScrollAnimation'),
    retakeScrollControl: spy('retakeScrollControl'),
    rearmScrollBufferFiller: spy('rearmScrollBufferFiller'),
    leaveFollow: spy('leaveFollow'),
    checkPosition: spy('checkPosition'),
    // A landing that moves the view.
    placeAt: vi.fn((resolve: () => number | null) => {
      calls.push('placeAt')
      resolve()
      return true
    }),
    loadedRows: {
      has: id => messages.some(message => message.id === id),
      nearestId: vi.fn((seq: bigint) => {
        const index = nearestServerRowIndexBySeq(messages, seq)
        return index < 0
          ? undefined
          : messages[index]?.id
      }),
      firstId: () => messages[0]?.id,
    },
  })
  const seek = createChatSeek(ctx, {
    messages: () => messages,
    onJumpToSeq,
    onSaveViewportScroll,
  })
  return {
    div,
    ctx,
    calls,
    fetch: fetches[0]!,
    fetches,
    landings,
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
      fetch.resolve(true)

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

  it('lands on the nearest row of the page when the fetch of an out-of-window seek arrives', async () => {
    const { calls, fetch, seek, swapWindow, landings, ctx, onJumpToSeq } = setup()
    const landing = seek.jumpToSeq(5n)
    // The fetch is the pending history jump of the hook, so a later command can end it.
    expect(onJumpToSeq).toHaveBeenCalledWith(5n, vi.mocked(ctx.beginHistoryJump).mock.results[0]!.value)
    swapWindow([3, 4, 5, 6, 7])
    fetch.resolve(true)

    expect(await landing).toBe(true)

    expect(calls.filter(call => call === 'placeAt')).toHaveLength(1)
    expect(landings).toEqual([
      { id: 'm5', offsetWithinRow: 0, seq: 5n },
    ])
  })

  it('lands once, on the newer seq, when a newer seek replaces a seek outside the window', async () => {
    const { calls, fetches, seek, swapWindow, landings, onJumpToSeq } = setup()
    const first = seek.jumpToSeq(5n)
    const second = seek.jumpToSeq(9n)
    const firstSignal = onJumpToSeq.mock.calls[0]![1]!
    expect(firstSignal.aborted).toBe(true)
    swapWindow([7, 8, 9, 10, 11])
    fetches[0]!.resolve(true)
    fetches[1]!.resolve(true)

    expect(await first).toBe(false)
    expect(await second).toBe(true)

    expect(calls.filter(call => call === 'placeAt')).toHaveLength(1)
    expect(landings).toEqual([
      { id: 'm9', offsetWithinRow: 0, seq: 9n },
    ])
  })

  it('lands nothing for a seek that the reader abandoned', async () => {
    const { calls, fetch, seek, swapWindow, ctx } = setup()
    const landing = seek.jumpToSeq(5n)
    ctx.abandonHistoryJump()
    swapWindow([3, 4, 5, 6, 7])
    fetch.resolve(true)

    expect(await landing).toBe(false)

    expect(calls).not.toContain('placeAt')
  })

  it('lands nothing when another fetch replaced the page of the seek', async () => {
    const { calls, fetch, seek } = setup()
    const landing = seek.jumpToSeq(5n)
    // The window still holds the seqs 100 to 104: the page of this seek never arrived.
    fetch.resolve(false)

    expect(await landing).toBe(false)

    expect(calls).not.toContain('placeAt')
  })

  it('checks the position when the fetch of a seek fails', async () => {
    const { calls, fetch, seek } = setup()
    const landing = seek.jumpToSeq(5n)
    fetch.reject(new Error('the page did not load'))

    expect(await landing).toBe(false)

    expect(calls).not.toContain('placeAt')
    expect(calls).toContain('checkPosition')
  })

  it('abandons the pending history jump for a seek inside the window', async () => {
    const { seek, ctx } = setup()

    await seek.jumpToSeq(102n)

    expect(ctx.abandonHistoryJump).toHaveBeenCalledOnce()
    expect(ctx.beginHistoryJump).not.toHaveBeenCalled()
  })

  it('saves nothing when the pane is hidden at the landing and the window holds no row', async () => {
    const { div, fetch, seek, swapWindow, onSaveViewportScroll } = setup()
    const landing = seek.jumpToSeq(5n)
    swapWindow([])
    div.setClientHeight(0)
    fetch.resolve(true)

    expect(await landing).toBe(false)

    expect(onSaveViewportScroll).not.toHaveBeenCalled()
  })

  it('asks for the place of the loaded visible row nearest the seq', async () => {
    const { ctx, seek } = setup()
    // The message of the seq is hidden, so the nearest visible row is another message.
    vi.mocked(ctx.loadedRows.nearestId).mockReturnValue('m7')

    await seek.jumpToSeq(102n)

    expect(ctx.requestPlace).toHaveBeenCalledOnce()
    const [place, show] = vi.mocked(ctx.requestPlace).mock.calls[0]!
    expect(place).toEqual({
      kind: 'row',
      anchor: { id: 'm7', seq: 102n, offsetWithinRow: 0 },
    })
    expect(show).toBeTypeOf('function')
  })

  it('resolves false, and asks for no place, when no loaded visible row exists', async () => {
    const { ctx, seek } = setup()
    vi.mocked(ctx.loadedRows.nearestId).mockReturnValue(undefined)

    expect(await seek.jumpToSeq(102n)).toBe(false)

    expect(ctx.requestPlace).not.toHaveBeenCalled()
  })

  it('resolves false when the wait for the row ended without its placement', async () => {
    const { ctx, seek, calls } = setup()
    // A later command, or an input of the reader, ended the wait: no `show` ran.
    vi.mocked(ctx.requestPlace).mockResolvedValue(false)

    expect(await seek.jumpToSeq(102n)).toBe(false)

    expect(calls).not.toContain('placeAt')
  })

  it('resolves false when the placement ran and did not move the view', async () => {
    const { ctx, seek } = setup()
    vi.mocked(ctx.placeAt).mockReturnValue(false)

    expect(await seek.jumpToSeq(102n)).toBe(false)

    expect(ctx.placeAt).toHaveBeenCalledOnce()
  })
})
