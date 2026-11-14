import { afterEach, describe, expect, it, onTestFinished, vi } from 'vitest'
import { createManualFrames } from '~/test-support/manualFrames'
import {
  markedParagraphs,
  paragraphMarker,
  rowDistanceIn,
  startRowWatchIn,
  stopRowWatchIn,
  topParagraphIn,
  topRowIn,
} from './chatScrollStability'

/** Give `element` a box whose top is `top` and whose height is `height`. */
function placeAt<E extends Element>(element: E, top: number, height: number): E {
  element.getBoundingClientRect = () => DOMRect.fromRect({ x: 0, y: top, width: 100, height })
  return element
}

/** A scroll container whose top edge is at 100, with rows of the given seqs, tops, and heights. */
function containerWith(rows: { seq: string, top: number, height: number }[]): HTMLElement {
  const container = placeAt(document.createElement('div'), 100, 500)
  for (const { seq, top, height } of rows) {
    const row = placeAt(document.createElement('div'), top, height)
    row.dataset.seq = seq
    container.append(row)
  }
  document.body.append(container)
  return container
}

afterEach(() => {
  document.body.replaceChildren()
})

describe('topRowIn', () => {
  it('returns the row whose top edge is highest among the rows that reach below the container top', () => {
    const container = containerWith([
      { seq: '1', top: 20, height: 50 },
      { seq: '2', top: 80, height: 60 },
      { seq: '3', top: 150, height: 60 },
    ])

    // Row 1 ends at 70, above the container top at 100. Row 2 starts 20px above the top and reaches below it.
    expect(topRowIn(container)).toEqual({ seq: '2', distance: -20 })
  })

  it('ignores a row that ends exactly at the container top', () => {
    const container = containerWith([
      { seq: '1', top: 40, height: 60 },
      { seq: '2', top: 100, height: 60 },
    ])

    expect(topRowIn(container)).toEqual({ seq: '2', distance: 0 })
  })

  it('returns the highest row whatever the document order', () => {
    const container = containerWith([
      { seq: '5', top: 300, height: 60 },
      { seq: '4', top: 140, height: 60 },
    ])

    expect(topRowIn(container)).toEqual({ seq: '4', distance: 40 })
  })

  it('returns null for a container with no row', () => {
    expect(topRowIn(containerWith([]))).toBeNull()
  })
})

describe('rowDistanceIn', () => {
  it('returns the distance from the container top to the top of the row', () => {
    const container = containerWith([
      { seq: '7', top: 135.5, height: 40 },
    ])

    expect(rowDistanceIn(container, '7')).toBe(35.5)
  })

  it('returns null for a row that is not mounted', () => {
    expect(rowDistanceIn(containerWith([
      { seq: '7', top: 120, height: 40 },
    ]), '8')).toBeNull()
  })
})

describe('topParagraphIn', () => {
  /** A row of `seq` that holds one paragraph for each of `paragraphs`, each with its text and its top. */
  function rowWithParagraphs(seq: string, rowTop: number, paragraphs: { text: string, top: number }[]): HTMLElement {
    const container = containerWith([
      { seq, top: rowTop, height: 400 },
    ])
    const row = container.querySelector(`[data-seq="${seq}"]`)!
    for (const { text, top } of paragraphs) {
      const paragraph = placeAt(document.createElement('p'), top, 30)
      paragraph.textContent = text
      row.append(paragraph)
    }
    return container
  }

  it('returns the first marked paragraph at or below the container top, with the height of its row', () => {
    const container = rowWithParagraphs('3', 60, [
      { text: 'P0: above the top', top: 70 },
      { text: 'no marker here', top: 110 },
      { text: 'P1: the first one in view', top: 150 },
      { text: 'P2: further down', top: 190 },
    ])

    expect(topParagraphIn(container, paragraphMarker('P').source)).toEqual({ marker: 'P1:', distance: 50, rowHeight: 400 })
  })

  it('returns null when no marked paragraph is in view', () => {
    const container = rowWithParagraphs('3', 60, [
      { text: 'P0: above the top', top: 70 },
    ])

    expect(topParagraphIn(container, paragraphMarker('P').source)).toBeNull()
  })

  it('returns null for a marked paragraph outside a row', () => {
    const container = containerWith([])
    container.append(Object.assign(placeAt(document.createElement('p'), 120, 30), { textContent: 'P0: loose' }))

    expect(topParagraphIn(container, paragraphMarker('P').source)).toBeNull()
  })
})

describe('markedParagraphs', () => {
  it('joins the marked paragraphs with a blank line', () => {
    expect(markedParagraphs('A', 3, 'text')).toBe('A0: text\n\nA1: text\n\nA2: text')
  })

  it('returns an empty text for no paragraph', () => {
    expect(markedParagraphs('A', 0, 'text')).toBe('')
  })
})

describe('paragraphMarker', () => {
  it('matches the marker at the start of a paragraph only', () => {
    const marker = paragraphMarker('S1P')

    expect(marker.exec('S1P12: text')?.[0]).toBe('S1P12:')
    expect(marker.test('text S1P12:')).toBe(false)
    expect(marker.test('S2P12: text')).toBe(false)
  })
})

describe('startRowWatchIn', () => {
  const KEY = 'testRowWatch'

  /** Install manual frames as the frames of the page, and remove them after the test. */
  function installFrames() {
    const frames = createManualFrames()
    vi.stubGlobal('requestAnimationFrame', frames.requestFrame)
    vi.stubGlobal('cancelAnimationFrame', frames.cancelFrame)
    onTestFinished(() => {
      vi.unstubAllGlobals()
      Reflect.deleteProperty(window, KEY)
    })
    return frames
  }

  it('records the smallest and the largest distance of the row over the frames', () => {
    const frames = installFrames()
    const container = containerWith([
      { seq: '4', top: 150, height: 40 },
    ])
    const row = container.querySelector<HTMLElement>('[data-seq="4"]')!

    startRowWatchIn(container, { rowSeq: '4', key: KEY })
    frames.runFrames()
    placeAt(row, 146.5, 40)
    frames.runFrames()
    placeAt(row, 152, 40)
    frames.runFrames()

    expect(stopRowWatchIn(KEY)).toEqual({
      frames: 3,
      missingFrames: 0,
      min: 46.5,
      max: 52,
    })
  })

  it('counts each frame in which the row is not mounted', () => {
    const frames = installFrames()
    const container = containerWith([
      { seq: '4', top: 150, height: 40 },
    ])

    startRowWatchIn(container, { rowSeq: '4', key: KEY })
    frames.runFrames()
    container.querySelector('[data-seq="4"]')!.remove()
    frames.runFrames()
    frames.runFrames()

    expect(stopRowWatchIn(KEY)).toEqual({
      frames: 3,
      missingFrames: 2,
      min: 50,
      max: 50,
    })
  })

  it('samples nothing before the first frame, and nothing after the stop', () => {
    const frames = installFrames()
    const container = containerWith([
      { seq: '4', top: 150, height: 40 },
    ])

    startRowWatchIn(container, { rowSeq: '4', key: KEY })
    expect(stopRowWatchIn(KEY)).toEqual({
      frames: 0,
      missingFrames: 0,
      min: Number.POSITIVE_INFINITY,
      max: Number.NEGATIVE_INFINITY,
    })
    frames.runFrames()

    expect(frames.pendingFrames(), 'a stopped watch requests no further frame').toBe(0)
  })
})

describe('stopRowWatchIn', () => {
  it('returns null when no watch runs under the key', () => {
    expect(stopRowWatchIn('noRowWatch')).toBeNull()
  })

  it('returns null for a second stop of one watch', () => {
    vi.stubGlobal('requestAnimationFrame', createManualFrames().requestFrame)
    onTestFinished(() => {
      vi.unstubAllGlobals()
    })
    startRowWatchIn(containerWith([]), { rowSeq: '1', key: 'stoppedTwice' })

    expect(stopRowWatchIn('stoppedTwice')).not.toBeNull()
    expect(stopRowWatchIn('stoppedTwice')).toBeNull()
  })
})
