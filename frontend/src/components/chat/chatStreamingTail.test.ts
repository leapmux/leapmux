import type { StreamingTail } from './chatStreamingTail'
import { batch, createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createStreamingTail } from './chatStreamingTail'

// Deterministic markdown so the tail HTML is trivially assertable.
vi.mock('~/lib/renderMarkdown', () => ({
  renderMarkdown: (text: string) => `<p>${text}</p>`,
}))

describe('chatstreamingtail', () => {
  // A manual requestAnimationFrame queue so the rAF-throttled render can be stepped
  // deterministically (mirrors rafCoalesce.test's harness). Monotonic handles keep the
  // coalescer's rafId bookkeeping correct across re-scheduling.
  let frames: Array<FrameRequestCallback | undefined>
  let nextHandle: number
  const flushFrames = (): void => {
    for (let handle = 0; handle < frames.length; handle++) {
      const frame = frames[handle]
      if (frame) {
        frames[handle] = undefined
        frame(0)
      }
    }
  }

  beforeEach(() => {
    frames = []
    nextHandle = 1
    vi.stubGlobal('requestAnimationFrame', vi.fn((cb: FrameRequestCallback) => {
      const handle = nextHandle++
      frames[handle] = cb
      return handle
    }))
    vi.stubGlobal('cancelAnimationFrame', vi.fn((handle: number) => {
      frames[handle] = undefined
    }))
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  function setup(initial: {
    streamingText?: string
    streamingType?: string
    hasNewer?: boolean
    tailId?: string
    measured?: string[]
  } = {}) {
    const [streamingText, setStreamingText] = createSignal(initial.streamingText ?? '')
    const [streamingType, setStreamingType] = createSignal<string | undefined>(initial.streamingType)
    const [hasNewer, setHasNewer] = createSignal(initial.hasNewer ?? false)
    const [tailId, setTailId] = createSignal<string | undefined>(initial.tailId)
    // Signal-backed so a later measurement re-runs the effects that read it (virt's real
    // hasMeasuredHeight is reactive the same way).
    const [measured, setMeasured] = createSignal<ReadonlySet<string>>(new Set(initial.measured ?? []))
    let api!: StreamingTail
    let dispose!: () => void
    createRoot((d) => {
      dispose = d
      api = createStreamingTail({
        streamingText,
        streamingType,
        hasNewerMessages: hasNewer,
        tailVisibleId: tailId,
        hasMeasuredHeight: id => measured().has(id),
      })
    })
    return { api, setStreamingText, setStreamingType, setHasNewer, setTailId, setMeasured, dispose }
  }

  it('renders streaming markdown once per frame and exposes it at the tail', () => {
    const h = setup()
    h.setStreamingText('answer')
    // The render is throttled to a frame: nothing yet.
    expect(h.api.renderedStreamHtml()).toBe('')
    flushFrames()
    expect(h.api.renderedStreamHtml()).toBe('<p>answer</p>')
    expect(h.api.streamingTailRender()).toEqual({ html: '<p>answer</p>', type: undefined })
    h.dispose()
  })

  it('coalesces a burst of stream chunks into one render of the latest text', () => {
    const h = setup()
    h.setStreamingText('a')
    h.setStreamingText('ab')
    h.setStreamingText('abc')
    // Only one frame was scheduled for the whole burst.
    expect(requestAnimationFrame).toHaveBeenCalledTimes(1)
    flushFrames()
    expect(h.api.renderedStreamHtml()).toBe('<p>abc</p>')
    h.dispose()
  })

  it('carries the streaming type through to the tail render (plan chrome)', () => {
    const h = setup({ streamingType: 'plan' })
    h.setStreamingText('a plan')
    flushFrames()
    expect(h.api.streamingTailRender()).toEqual({ html: '<p>a plan</p>', type: 'plan' })
    h.dispose()
  })

  it('clears the render and tail when streaming stops with no replacement row', () => {
    const h = setup()
    h.setStreamingText('answer')
    flushFrames()
    expect(h.api.streamingTailRender()).toBeDefined()
    h.setStreamingText('') // tail id never set -> nothing to hold
    expect(h.api.renderedStreamHtml()).toBe('')
    expect(h.api.streamingTailRender()).toBeUndefined()
    expect(h.api.streamReplacementTailId()).toBeUndefined()
    h.dispose()
  })

  it('holds the streaming HTML over the unmeasured replacement row, then releases it once measured', () => {
    const h = setup({ tailId: 'baseline' })
    h.setStreamingText('answer')
    flushFrames()
    // Streaming ends and a NEW persisted row becomes the tail, unmeasured (mirrors
    // ChatView's batched setMessages + setStreamingText('')).
    batch(() => {
      h.setStreamingText('')
      h.setTailId('m0')
    })
    // The finished stream's HTML stays in flow, covering the estimate-sized row.
    expect(h.api.streamReplacementTailId()).toBe('m0')
    expect(h.api.isCoveredByInFlowTail('m0')).toBe(true)
    expect(h.api.streamingTailRender()).toEqual({ html: '<p>answer</p>', type: undefined })

    // Once the row has real geometry, the cover is released so the virtualized row takes over.
    h.setMeasured(new Set(['m0']))
    expect(h.api.isCoveredByInFlowTail('m0')).toBe(false)
    expect(h.api.streamingTailRender()).toBeUndefined()
    h.dispose()
  })

  it('drops a held replacement when a NEW stream starts before the row measured', () => {
    const h = setup({ tailId: 'baseline' })
    h.setStreamingText('answer')
    flushFrames()
    batch(() => {
      h.setStreamingText('')
      h.setTailId('m0')
    })
    expect(h.api.isCoveredByInFlowTail('m0')).toBe(true)
    // A new turn begins before m0 was ever measured: the stale cover must drop so it
    // can't linger over the new stream.
    h.setStreamingText('next answer')
    expect(h.api.streamReplacementTailId()).toBeUndefined()
    expect(h.api.isCoveredByInFlowTail('m0')).toBe(false)
    flushFrames()
    expect(h.api.streamingTailRender()).toEqual({ html: '<p>next answer</p>', type: undefined })
    h.dispose()
  })

  it('captures the replacement when the persisted tail arrives a beat AFTER streaming clears', () => {
    // Same baseline tail at stream end (a hidden lifecycle/meta row, not the answer yet):
    // the machine keeps one exemption pending (awaitingStreamReplacementTail) so the real
    // row, arriving later, still gets covered instead of blinking behind premeasure.
    const h = setup({ tailId: 'baseline' })
    h.setStreamingText('answer')
    flushFrames()
    h.setStreamingText('') // stops while the tail is still 'baseline'
    expect(h.api.streamReplacementTailId()).toBeUndefined()
    // The real assistant row lands a tick later.
    h.setTailId('m0')
    expect(h.api.streamReplacementTailId()).toBe('m0')
    expect(h.api.isCoveredByInFlowTail('m0')).toBe(true)
    expect(h.api.streamingTailRender()).toEqual({ html: '<p>answer</p>', type: undefined })
    h.dispose()
  })

  it('drops the held replacement when the view windows away from the live tail', () => {
    const h = setup({ tailId: 'baseline' })
    h.setStreamingText('answer')
    flushFrames()
    batch(() => {
      h.setStreamingText('')
      h.setTailId('m0')
    })
    expect(h.api.isCoveredByInFlowTail('m0')).toBe(true)
    // Scrolled away from the tail: the loaded bottom isn't the real bottom, so drop it.
    h.setHasNewer(true)
    expect(h.api.isCoveredByInFlowTail('m0')).toBe(false)
    expect(h.api.streamingTailRender()).toBeUndefined()
    h.dispose()
  })

  it('does not hold a replacement row that is already measured', () => {
    const h = setup({ tailId: 'baseline', measured: ['m0'] })
    h.setStreamingText('answer')
    flushFrames()
    batch(() => {
      h.setStreamingText('')
      h.setTailId('m0')
    })
    // m0 already has real geometry -> nothing to bridge, so no held cover.
    expect(h.api.streamReplacementTailId()).toBe('m0')
    expect(h.api.isCoveredByInFlowTail('m0')).toBe(false)
    expect(h.api.streamingTailRender()).toBeUndefined()
    h.dispose()
  })
})
