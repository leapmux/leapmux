import { afterEach, describe, expect, it, vi } from 'vitest'
import { installGenerationObservation, parseGenerationCounters, verifyCompletedGenerationOutput } from './generationProgress'

afterEach(() => {
  window.__nativeGenerationProbe?.stop()
  delete window.__nativeGenerationProbe
  document.body.replaceChildren()
  vi.restoreAllMocks()
})

describe('parseGenerationCounters', () => {
  it('reads token and byte labels without treating elapsed time as output', () => {
    expect(parseGenerationCounters('Working for 2s · 123 tokens')).toEqual({ tokens: 123 })
    expect(parseGenerationCounters('Working for 2s · 1.2k tokens · ≥1.5 KB')).toEqual({ tokens: 1200, bytes: 1536 })
    expect(parseGenerationCounters('1.5m tokens · 3 MB')).toEqual({ tokens: 1_500_000, bytes: 3 * 1024 ** 2 })
    expect(parseGenerationCounters('1.2 k tokens')).toEqual({ tokens: 1200 })
    expect(parseGenerationCounters('123     tokens')).toEqual({ tokens: 123 })
    expect(parseGenerationCounters('Working for 300ms')).toEqual({})
  })

  it('keeps zero distinct from an absent counter', () => {
    expect(parseGenerationCounters('1 token')).toEqual({ tokens: 1 })
    expect(parseGenerationCounters('0 tokens · 0 B')).toEqual({ tokens: 0, bytes: 0 })
    expect(parseGenerationCounters('')).toEqual({})
  })

  it('rejects a counter that exceeds the numeric range', () => {
    expect(() => parseGenerationCounters(`${'9'.repeat(400)} tokens`)).toThrow('token counter')
    expect(() => parseGenerationCounters(`${'9'.repeat(400)} MB`)).toThrow('byte counter')
  })
})

describe('installGenerationObservation', () => {
  function indicator(markup: string): void {
    const element = document.createElement('div')
    element.dataset.testid = 'thinking-indicator'
    element.innerHTML = markup
    const rectangle = new DOMRect(0, 0, 100, 20)
    const rectangles = Object.assign([rectangle], { item: (index: number) => index === 0 ? rectangle : null })
    vi.spyOn(element, 'getClientRects').mockReturnValue(rectangles)
    document.body.append(element)
    installGenerationObservation()
  }

  it('reads current counter values without hidden digit strips or unrelated task text', () => {
    indicator(`
      <span>Task title: read 999 tokens</span>
      <span data-animated-count><span>5 tokens</span><span aria-hidden="true">012345678901234567890123456789</span> tokens</span>
      <span data-animated-count><span>1 KB</span><span aria-hidden="true">012345678901234567890123456789</span> KB</span>
    `)
    expect(window.__nativeGenerationProbe?.samples).toEqual(['5 tokens · 1 KB'])
  })

  it('ignores token text when the native turn exposes no counter element', () => {
    indicator('<span>Task title: read 777 tokens</span>')
    expect(window.__nativeGenerationProbe?.samples).toEqual([''])
  })
})

describe('verifyCompletedGenerationOutput', () => {
  it('awaits result expansion before checking both exact output markers', async () => {
    const events: string[] = []
    let release!: () => void
    const pending = new Promise<void>((resolve) => {
      release = resolve
    })
    const proof = verifyCompletedGenerationOutput('call_actual_native', { first: 'FIRST42', second: 'SECOND77' }, {
      prepareView: async (callId) => {
        events.push(callId)
        await pending
      },
      assertVisible: async (marker) => { events.push(marker) },
    })
    expect(events).toEqual(['call_actual_native'])
    release()
    await proof
    expect(events).toEqual(['call_actual_native', 'FIRST42', 'SECOND77'])
  })

  it('keeps both marker assertions when the provider needs no expansion', async () => {
    const assertVisible = vi.fn(async (_marker: string) => {})
    await verifyCompletedGenerationOutput('call', { first: 'FIRST42', second: 'SECOND77' }, { assertVisible })
    expect(assertVisible.mock.calls).toEqual([['FIRST42'], ['SECOND77']])
  })

  it('propagates the preparation error before either marker assertion', async () => {
    const failure = new Error('The actual result did not expose its Expand action.')
    const assertVisible = vi.fn(async (_marker: string) => {})
    await expect(verifyCompletedGenerationOutput('call', { first: 'FIRST42', second: 'SECOND77' }, {
      prepareView: async () => { throw failure },
      assertVisible,
    })).rejects.toBe(failure)
    expect(assertVisible).not.toHaveBeenCalled()
  })

  it('propagates a missing second marker instead of accepting the first marker alone', async () => {
    const markers: string[] = []
    const failure = new Error('The second native output marker is absent.')
    await expect(verifyCompletedGenerationOutput('call', { first: 'FIRST42', second: 'SECOND77' }, {
      assertVisible: async (marker) => {
        markers.push(marker)
        if (marker === 'SECOND77')
          throw failure
      },
    })).rejects.toBe(failure)
    expect(markers).toEqual(['FIRST42', 'SECOND77'])
  })

  it.each([{ call: '', first: 'one', second: 'two' }, { call: 'call', first: '', second: 'two' }, { call: 'call', first: 'one', second: '' }, { call: 'call', first: 'same', second: 'same' }])('rejects invalid completed output proof %j', async ({ call, first, second }) => {
    const assertVisible = vi.fn(async (_marker: string) => {})
    await expect(verifyCompletedGenerationOutput(call, { first, second }, { assertVisible })).rejects.toThrow('distinct output markers')
    expect(assertVisible).not.toHaveBeenCalled()
  })
})
