import { describe, expect, it } from 'vitest'
import { countMatchingChatBubbles } from './chatBubbleCount'

const original = (sequence: string) => ({ sequence, role: 'agent', text: 'The original answer.' })
function viewport(windows = [
  [
    original('2'),
  ],
  [
    original('2'),
  ],
  [
    original('5'),
  ],
  [
    { sequence: '8', role: 'agent', text: 'The resumed answer.' },
  ],
]) {
  const state = { top: 240, contentHeight: 340, visits: [] as number[] }
  return {
    state,
    reveal: async () => {},
    scrollToEnd: async () => { state.top = state.contentHeight - 100 },
    read: async () => ({
      top: state.top,
      height: 100,
      contentHeight: state.contentHeight,
      bubbles: windows[Math.min(3, Math.floor(state.top / 80))] ?? [],
    }),
    scrollTo: async (top: number) => {
      state.top = top
      state.visits.push(top)
    },
  }
}

describe('countMatchingChatBubbles', () => {
  it('finds distant original bubbles while the viewport initially holds only the resumed answer', async () => {
    const view = viewport()
    expect(await countMatchingChatBubbles(view, 'agent', 'The original answer.')).toBe(2)
    expect(view.state.top).toBe(240)
    expect(view.state.visits).toContain(0)
    expect(view.state.visits).toContain(160)
  })

  it('counts a tall bubble once across overlapping windows', async () => {
    const view = viewport([
      [
        original('2'),
      ],
      [
        original('2'),
      ],
      [
        original('2'),
      ],
      [
        original('2'),
      ],
    ])
    expect(await countMatchingChatBubbles(view, 'agent', 'The original answer.')).toBe(1)
    expect(view.state.top).toBe(240)
  })

  it('restores the viewport after rejecting duplicate bubbles in one window', async () => {
    const view = viewport([
      [
        original('2'),
        original('2'),
      ],
      [],
      [],
      [],
    ])
    await expect(countMatchingChatBubbles(view, 'agent', 'The original answer.')).rejects.toThrow('duplicate')
    expect(view.state.top).toBe(240)
  })

  it.each(['0', '-1', '', 'not-a-sequence'])('rejects the invalid sequence %j and restores the viewport', async (sequence) => {
    const view = viewport([
      [
        original(sequence),
      ],
      [],
      [],
      [],
    ])
    await expect(countMatchingChatBubbles(view, 'agent', 'The original answer.')).rejects.toThrow('positive')
    expect(view.state.top).toBe(240)
  })

  it('rejects a viewport that cannot advance and restores its original position', async () => {
    const view = viewport()
    view.scrollTo = async (top: number) => {
      if (top === 0 || top === 240)
        view.state.top = top
    }
    await expect(countMatchingChatBubbles(view, 'agent', 'The original answer.')).rejects.toThrow('did not advance')
    expect(view.state.top).toBe(240)
  })

  it.each([
    { top: Number.NaN },
    { top: -1 },
    { height: 0 },
    { height: -1 },
    { contentHeight: Number.POSITIVE_INFINITY },
    { contentHeight: -1 },
  ])('rejects invalid initial geometry %j without changing the viewport', async (geometry) => {
    const view = viewport()
    const read = view.read
    view.read = async () => ({
      ...await read(),
      ...geometry,
    })
    await expect(countMatchingChatBubbles(view, 'agent', 'The original answer.')).rejects.toThrow('finite scroll geometry')
    expect(view.state.visits).toEqual([])
  })

  it('restores the viewport after a read fails during traversal', async () => {
    const view = viewport()
    const read = view.read
    view.read = async () => {
      if (view.state.top === 80)
        throw new Error('The virtual window disappeared.')
      return read()
    }
    await expect(countMatchingChatBubbles(view, 'agent', 'The original answer.')).rejects.toThrow('virtual window disappeared')
    expect(view.state.top).toBe(240)
  })

  it('keeps large Worker sequences exact', async () => {
    const view = viewport([
      [
        original('9223372036854775806'),
      ],
      [],
      [
        original('9223372036854775807'),
      ],
      [],
    ])
    expect(await countMatchingChatBubbles(view, 'agent', 'The original answer.')).toBe(2)
  })

  it.each(['', '  '])('rejects empty search text %j', async (text) => {
    const view = viewport()
    await expect(countMatchingChatBubbles(view, 'agent', text)).rejects.toThrow('nonempty text')
    expect(view.state.visits).toEqual([])
  })

  it('reveals saved history before counting a partial initial transcript', async () => {
    const view = viewport()
    const read = view.read
    let ready = false
    view.read = async () => ({
      ...await read(),
      bubbles: ready
        ? (await read()).bubbles
        : [],
    })
    view.reveal = async () => {
      ready = true
    }
    expect(await countMatchingChatBubbles(view, 'agent', 'The original answer.')).toBe(2)
    expect(ready).toBe(true)
  })

  it('preserves end intent when measuring rows grows the transcript', async () => {
    const view = viewport()
    const read = view.read
    view.read = async () => {
      if (view.state.top === 160)
        view.state.contentHeight = 500
      return read()
    }
    expect(await countMatchingChatBubbles(view, 'agent', 'The original answer.')).toBe(2)
    expect(view.state.top).toBe(400)
  })

  it('accepts advancing sequences when measurement adjusts the pixel offset', async () => {
    const view = viewport()
    let phase = 0
    view.read = async () => ({
      top: view.state.top,
      height: 100,
      contentHeight: 340,
      bubbles: [
        original(String(phase + 1)),
      ],
    })
    view.scrollTo = async (top: number) => {
      if (top === 0) {
        phase = 0
        view.state.top = 0
      }
      else {
        phase += 1
        view.state.top = phase < 3
          ? 0
          : 240
      }
    }
    expect(await countMatchingChatBubbles(view, 'agent', 'The original answer.')).toBe(4)
    expect(view.state.top).toBe(240)
  })

  it('restores a middle position after measurement grows the transcript', async () => {
    const view = viewport()
    view.state.top = 80
    const read = view.read
    view.read = async () => {
      if (view.state.top === 160)
        view.state.contentHeight = 500
      return read()
    }
    expect(await countMatchingChatBubbles(view, 'agent', 'The original answer.')).toBe(2)
    expect(view.state.top).toBe(80)
  })

  it('restores the new end after a failure following geometry growth', async () => {
    const view = viewport()
    const read = view.read
    view.read = async () => {
      if (view.state.top === 160) {
        view.state.contentHeight = 500
        throw new Error('The grown window is unavailable.')
      }
      return read()
    }
    await expect(countMatchingChatBubbles(view, 'agent', 'The original answer.')).rejects.toThrow('grown window is unavailable')
    expect(view.state.top).toBe(400)
  })
})
