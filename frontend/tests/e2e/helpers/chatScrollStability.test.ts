import type { Page } from '@playwright/test'
import type { WatchedFrame } from './chatScrollStability'
import { IDBFactory } from 'fake-indexeddb'
import { afterEach, beforeEach, describe, expect, it, onTestFinished, vi } from 'vitest'
import { LARGE_MARKDOWN_BODY_CHARS } from '~/lib/markdownWorkerClient'
import { createManualFrames } from '~/test-support/manualFrames'
import {
  clearObjectStoreIn,
  clearStoredRowHeights,
  contentBoxWidthIn,
  frameJumps,
  framesPassIn,
  heldReplyCountIn,
  installReplyHoldIn,
  LARGE_BODY_FILLER_CHARS,
  largeBodyFiller,
  markedParagraphs,
  mountedRowSeqsIn,
  paragraphMarker,
  premeasureCommitIn,
  releaseHeldRepliesIn,
  REMOUNT_WINDOW_FRAMES,
  rowBoxIn,
  rowDistanceIn,
  rowHeightChangesIn,
  rowSeqWithTextIn,
  rowStatesIn,
  shortTurn,
  startFrameWatchIn,
  startPremeasureRecordIn,
  startRowHeightRecordIn,
  startRowWatchIn,
  stopFrameWatchIn,
  stopRowWatchIn,
  topParagraphIn,
  topRowIn,
  widthSensitiveParagraphs,
} from './chatScrollStability'
import { deleteEntries, storageKeys, writeEntry } from './storage'

// Run the real poll of a Page helper with a short unit-test deadline.
vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  return {
    ...actual,
    expect: actual.expect.configure({ timeout: 500 }),
  }
})

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

describe('rowBoxIn', () => {
  it('returns the distance from the container top to the top of the row, and the height of the row', () => {
    const container = containerWith([
      { seq: '7', top: 60.5, height: 4000 },
    ])

    // The row starts 39.5px above the container top at 100.
    expect(rowBoxIn(container, '7')).toEqual({ distance: -39.5, height: 4000 })
  })

  it('returns null for a row that is not mounted', () => {
    expect(rowBoxIn(containerWith([
      { seq: '7', top: 120, height: 40 },
    ]), '8')).toBeNull()
  })
})

describe('rowSeqWithTextIn', () => {
  /** A container with one row for each of `rows`, each with its text. */
  function containerWithTexts(rows: { seq: string, text: string }[]): HTMLElement {
    const container = containerWith(rows.map(({ seq }, index) => ({ seq, top: 100 + index * 50, height: 40 })))
    for (const { seq, text } of rows)
      container.querySelector(`[data-seq="${seq}"]`)!.textContent = text
    return container
  }

  it('returns the sequence number of the row whose text contains the text', () => {
    const container = containerWithTexts([
      { seq: '4', text: 'Turn 1.' },
      { seq: '5', text: 'Tall0: the first paragraph. Tall1: the second one.' },
    ])

    expect(rowSeqWithTextIn(container, 'Tall0:')).toBe('5')
  })

  it('returns the first row in document order when two rows contain the text', () => {
    const container = containerWithTexts([
      { seq: '9', text: 'Answer 12.' },
      { seq: '3', text: 'Answer 12. Again.' },
    ])

    expect(rowSeqWithTextIn(container, 'Answer 12.')).toBe('9')
  })

  it('returns null when no mounted row contains the text', () => {
    const container = containerWithTexts([
      { seq: '4', text: 'Turn 11.' },
    ])

    expect(rowSeqWithTextIn(container, 'Turn 1.')).toBeNull()
    expect(rowSeqWithTextIn(containerWith([]), 'Turn 1.')).toBeNull()
  })
})

describe('contentBoxWidthIn', () => {
  /** A container with the given client width and paddings. jsdom computes no layout, so the test sets the width. */
  function containerOfWidth(clientWidth: number, padding: { left: string, right: string }): HTMLElement {
    const container = document.createElement('div')
    container.style.paddingLeft = padding.left
    container.style.paddingRight = padding.right
    Object.defineProperty(container, 'clientWidth', { configurable: true, value: clientWidth })
    document.body.append(container)
    return container
  }

  it('subtracts the left and the right padding from the client width', () => {
    expect(contentBoxWidthIn(containerOfWidth(388, { left: '8px', right: '8px' }))).toBe(372)
  })

  it('keeps a fraction of a pixel, and takes each padding on its own', () => {
    expect(contentBoxWidthIn(containerOfWidth(400, { left: '4.5px', right: '16px' }))).toBe(379.5)
  })

  it('returns the client width of a container with no padding', () => {
    expect(contentBoxWidthIn(containerOfWidth(388, { left: '0px', right: '0px' }))).toBe(388)
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

describe('shortTurn', () => {
  it('gives the turn a prompt and an answer that hold its number, and inserts the prompt', () => {
    expect(shortTurn(7)).toEqual({ prompt: 'Turn 7.', answer: 'Answer 7.', entry: 'insert' })
  })

  // A spec finds an answer with a text filter, which matches a part of a text.
  it('gives no turn a text that contains the text of another turn', () => {
    expect(shortTurn(11).answer).not.toContain(shortTurn(1).answer)
    expect(shortTurn(10).prompt).not.toContain(shortTurn(1).prompt)
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

describe('widthSensitiveParagraphs', () => {
  it('marks each paragraph, and separates two paragraphs with a blank line', () => {
    const paragraphs = widthSensitiveParagraphs('Tall', 4).split('\n\n')

    expect(paragraphs).toHaveLength(4)
    paragraphs.forEach((paragraph, index) => {
      expect(paragraph.startsWith(`Tall${index}: `)).toBe(true)
    })
    expect(paragraphMarker('Tall').exec(paragraphs[3]!)?.[0]).toBe('Tall3:')
  })

  it('gives each paragraph one unbroken text of letters and digits', () => {
    for (const paragraph of widthSensitiveParagraphs('Tall', 150).split('\n\n'))
      expect(paragraph).toMatch(/^Tall\d+: [a-z0-9]+$/i)
  })

  it('gives the texts many different lengths, from 200 to 699 characters', () => {
    const lengths = widthSensitiveParagraphs('Tall', 150).split('\n\n').map(paragraph => paragraph.split(' ')[1]!.length)

    expect(Math.min(...lengths)).toBeGreaterThanOrEqual(200)
    expect(Math.max(...lengths)).toBeLessThanOrEqual(699)
    expect(lengths[0]).toBe(200)
    expect(lengths[1]).toBe(237)
    expect(new Set(lengths).size).toBe(150)
  })

  it('gives each text its own characters, so two texts break their lines at different places', () => {
    const texts = widthSensitiveParagraphs('Tall', 150).split('\n\n').map(paragraph => paragraph.split(' ')[1]!)

    expect(new Set(texts.map(text => text.slice(0, 40))).size).toBeGreaterThan(50)
    // Each text mixes narrow and wide glyphs: no text repeats one character.
    for (const text of texts)
      expect(new Set(text).size).toBeGreaterThan(20)
  })

  it('returns the same text for the same arguments, and an empty text for no paragraph', () => {
    expect(widthSensitiveParagraphs('Tall', 7)).toBe(widthSensitiveParagraphs('Tall', 7))
    expect(widthSensitiveParagraphs('Tall', 0)).toBe('')
  })
})

describe('startPremeasureRecordIn', () => {
  const KEY = 'testPremeasureCommits'

  /** Start a record under KEY, and remove its state after the test. Its listener stays, and writes to no state then. */
  function startRecord(): void {
    startPremeasureRecordIn(KEY)
    onTestFinished(() => {
      Reflect.deleteProperty(window, KEY)
    })
  }

  /** Dispatch the event that the app sends for one step of a hidden premeasure. */
  function dispatchPremeasure(detail: unknown): void {
    window.dispatchEvent(new CustomEvent('leapmux:chat-premeasure', { detail }))
  }

  it('records the height and the height key of each accepted commit by the sequence number of its row', () => {
    startRecord()
    dispatchPremeasure({
      phase: 'commit',
      accepted: true,
      seq: '12',
      height: 4321.5,
      heightKey: 'k|376',
    })
    dispatchPremeasure({
      phase: 'commit',
      accepted: true,
      seq: '13',
      height: 0,
      heightKey: '',
    })

    expect(premeasureCommitIn({ key: KEY, seq: '12' })).toEqual({ height: 4321.5, heightKey: 'k|376' })
    expect(premeasureCommitIn({ key: KEY, seq: '13' })).toEqual({ height: 0, heightKey: '' })
    expect(premeasureCommitIn({ key: KEY, seq: '14' })).toBeNull()
  })

  it('keeps the last accepted commit of one row', () => {
    startRecord()
    dispatchPremeasure({
      phase: 'commit',
      accepted: true,
      seq: '12',
      height: 100,
      heightKey: 'k|424',
    })
    dispatchPremeasure({
      phase: 'commit',
      accepted: true,
      seq: '12',
      height: 180,
      heightKey: 'k|376',
    })

    expect(premeasureCommitIn({ key: KEY, seq: '12' })).toEqual({ height: 180, heightKey: 'k|376' })
  })

  it('ignores a read, a commit that the virtualizer refused, and a detail of another shape', () => {
    startRecord()
    dispatchPremeasure({
      phase: 'read',
      accepted: true,
      seq: '1',
      height: 100,
      heightKey: 'k',
    })
    dispatchPremeasure({
      phase: 'commit',
      accepted: false,
      seq: '2',
      height: 100,
      heightKey: 'k',
    })
    dispatchPremeasure({
      phase: 'commit',
      accepted: true,
      seq: null,
      height: 100,
      heightKey: 'k',
    })
    dispatchPremeasure({
      phase: 'commit',
      accepted: true,
      seq: '4',
      height: 'Infinity',
      heightKey: 'k',
    })
    dispatchPremeasure({
      phase: 'commit',
      accepted: true,
      seq: '5',
      height: 100,
      heightKey: null,
    })
    dispatchPremeasure(null)
    dispatchPremeasure('commit')
    window.dispatchEvent(new Event('leapmux:chat-premeasure'))

    for (const seq of ['1', '2', '4', '5'])
      expect(premeasureCommitIn({ key: KEY, seq })).toBeNull()
  })
})

describe('premeasureCommitIn', () => {
  it('returns null when no record runs under the key', () => {
    expect(premeasureCommitIn({ key: 'noPremeasureRecord', seq: '1' })).toBeNull()
  })
})

describe('paragraphMarker', () => {
  it('matches the marker at the start of a paragraph only', () => {
    const marker = paragraphMarker('S1P')

    expect(marker.exec('S1P12: text')?.[0]).toBe('S1P12:')
    expect(marker.test('text S1P12:')).toBe(false)
    expect(marker.test('S2P12: text')).toBe(false)
  })

  it('matches a label with a regular expression metacharacter literally', () => {
    expect(paragraphMarker('a.b').test('aXb1:')).toBe(false)
    expect(paragraphMarker('a.b').test('a.b1:')).toBe(true)
    expect(paragraphMarker('C++').test('C++3:')).toBe(true)
    expect(paragraphMarker('(P)').exec('(P)12: text')?.[0]).toBe('(P)12:')
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

describe('framesPassIn', () => {
  /** Install manual frames as the frames of the page, and remove them after the test. */
  function installFrames() {
    const frames = createManualFrames()
    vi.stubGlobal('requestAnimationFrame', frames.requestFrame)
    onTestFinished(() => {
      vi.unstubAllGlobals()
    })
    return frames
  }

  /** Whether `wait` resolved, read after the microtasks that follow its last frame. */
  async function resolved(wait: Promise<void>): Promise<boolean> {
    let done = false
    void wait.then(() => {
      done = true
    })
    await Promise.resolve()
    return done
  }

  it('resolves after the stated number of frames, and not one frame before', async () => {
    const frames = installFrames()
    const wait = framesPassIn(3)

    frames.runFrames()
    frames.runFrames()
    expect(await resolved(wait)).toBe(false)

    frames.runFrames()
    expect(await resolved(wait)).toBe(true)
    // It asks for no frame after the last one.
    expect(frames.pendingFrames()).toBe(0)
  })

  it.each([
    0,
    -2,
  ])('waits for one frame when the count is %s', async (count) => {
    const frames = installFrames()
    const wait = framesPassIn(count)
    expect(await resolved(wait)).toBe(false)

    frames.runFrames()

    expect(await resolved(wait)).toBe(true)
  })
})

describe('rowStatesIn', () => {
  it('returns the distance and the height of each row, in document order', () => {
    const container = containerWith([
      { seq: '4', top: 60, height: 80 },
      { seq: '5', top: 160, height: 26 },
    ])

    expect(rowStatesIn(container)).toEqual([
      { seq: '4', distance: -40, height: 80 },
      { seq: '5', distance: 60, height: 26 },
    ])
  })

  it('returns no row for a container with no row', () => {
    expect(rowStatesIn(containerWith([]))).toEqual([])
  })
})

describe('mountedRowSeqsIn', () => {
  it('returns the sequence numbers of the mounted rows in document order', () => {
    const container = containerWith([
      { seq: '7', top: 100, height: 40 },
      { seq: '8', top: 150, height: 40 },
      { seq: '9', top: 200, height: 40 },
    ])

    expect(mountedRowSeqsIn(container)).toEqual([7, 8, 9])
  })

  it('keeps the document order when the sequence numbers are not ascending', () => {
    const container = containerWith([
      { seq: '12', top: 100, height: 40 },
      { seq: '4', top: 150, height: 40 },
    ])

    expect(mountedRowSeqsIn(container)).toEqual([12, 4])
  })

  it('returns no sequence number for a container with no row', () => {
    expect(mountedRowSeqsIn(containerWith([]))).toEqual([])
  })
})

describe('installReplyHoldIn', () => {
  const KEY = '__testReplyHold'

  /** A stand-in for the Worker of a browser: its `onmessage` is an accessor of the prototype, as in a browser. */
  class FakeWorker {
    #handler: unknown = null

    get onmessage(): unknown {
      return this.#handler
    }

    set onmessage(handler: unknown) {
      this.#handler = handler
    }

    /** Deliver a message with `data` to the handler, as the browser does for a reply of the worker. */
    reply(data: unknown): void {
      const handler = this.onmessage
      if (typeof handler === 'function')
        handler.call(this, { data })
    }
  }

  /** A FakeWorker class of its own for one test, as the global `Worker`, with the hold of `marker` installed. */
  function installOn(marker: string): typeof FakeWorker {
    const WorkerOfTest = class extends FakeWorker {}
    // The hold reads the accessor from the prototype of `Worker` itself.
    Object.defineProperty(WorkerOfTest.prototype, 'onmessage', Object.getOwnPropertyDescriptor(FakeWorker.prototype, 'onmessage')!)
    vi.stubGlobal('Worker', WorkerOfTest)
    onTestFinished(() => {
      vi.unstubAllGlobals()
      Reflect.deleteProperty(window, KEY)
    })
    installReplyHoldIn({ key: KEY, marker })
    return WorkerOfTest
  }

  /** A worker of `WorkerOfTest` whose handler records the `html` of each reply that reaches it. */
  function workerRecordingInto(WorkerOfTest: typeof FakeWorker, delivered: string[]): FakeWorker {
    const worker = new WorkerOfTest()
    worker.onmessage = (event: { data: { html: string } }) => {
      delivered.push(event.data.html)
    }
    return worker
  }

  it('holds back a reply whose HTML contains the marker, and delivers every other reply', () => {
    const delivered: string[] = []
    const worker = workerRecordingInto(installOn('Late0:'), delivered)

    worker.reply({ html: '<p>Answer 1.</p>' })
    worker.reply({ html: '<p>Late0: text</p>' })
    worker.reply({ html: '<p>Answer 2.</p>' })

    expect(delivered).toEqual(['<p>Answer 1.</p>', '<p>Answer 2.</p>'])
    expect(heldReplyCountIn(KEY)).toBe(1)
  })

  it('delivers a message with no HTML text', () => {
    const seen: unknown[] = []
    const WorkerOfTest = installOn('Late0:')
    const worker = new WorkerOfTest()
    worker.onmessage = (event: { data: unknown }) => {
      seen.push(event.data)
    }

    worker.reply(null)
    worker.reply('Late0: a plain text')
    worker.reply({ html: 7 })
    worker.reply({ text: 'Late0: a request text' })

    expect(seen).toEqual([
      null,
      'Late0: a plain text',
      { html: 7 },
      { text: 'Late0: a request text' },
    ])
    expect(heldReplyCountIn(KEY)).toBe(0)
  })

  it('calls the handler with the worker as `this`, for a reply that it delivers and for one that it held', () => {
    const receivers: unknown[] = []
    const WorkerOfTest = installOn('Late0:')
    const worker = new WorkerOfTest()
    worker.onmessage = function (this: unknown) {
      receivers.push(this)
    }

    worker.reply({ html: 'Answer' })
    worker.reply({ html: 'Late0:' })
    releaseHeldRepliesIn(KEY)

    expect(receivers).toEqual([worker, worker])
  })

  it('passes a handler that is no function on unchanged', () => {
    const WorkerOfTest = installOn('Late0:')
    const worker = new WorkerOfTest()

    worker.onmessage = null

    expect(worker.onmessage).toBeNull()
    // A reply to a worker with no handler reaches nothing, and holds nothing.
    worker.reply({ html: 'Late0:' })
    expect(heldReplyCountIn(KEY)).toBe(0)
  })

  it('fails when `onmessage` is no accessor of the Worker prototype', () => {
    vi.stubGlobal('Worker', class {})
    onTestFinished(() => {
      vi.unstubAllGlobals()
    })

    expect(() => installReplyHoldIn({ key: KEY, marker: 'Late0:' })).toThrow('Worker.prototype.onmessage is not an accessor')
  })

  describe('releaseHeldRepliesIn', () => {
    it('delivers the held replies in the order of their arrival, and holds no later reply', () => {
      const delivered: string[] = []
      const WorkerOfTest = installOn('Late')
      const first = workerRecordingInto(WorkerOfTest, delivered)
      const second = workerRecordingInto(WorkerOfTest, delivered)
      first.reply({ html: 'Late A' })
      second.reply({ html: 'Late B' })
      first.reply({ html: 'Late C' })
      expect(delivered).toEqual([])
      expect(heldReplyCountIn(KEY)).toBe(3)

      releaseHeldRepliesIn(KEY)

      expect(delivered).toEqual(['Late A', 'Late B', 'Late C'])
      expect(heldReplyCountIn(KEY)).toBe(0)
      // The hold ended.
      first.reply({ html: 'Late D' })
      expect(delivered).toEqual(['Late A', 'Late B', 'Late C', 'Late D'])
    })

    it('does nothing when no hold exists under the key', () => {
      expect(() => releaseHeldRepliesIn('__noSuchHold')).not.toThrow()
    })
  })

  describe('heldReplyCountIn', () => {
    it('returns 0 when no hold exists under the key', () => {
      expect(heldReplyCountIn('__noSuchHold')).toBe(0)
    })
  })
})

describe('largeBodyFiller', () => {
  it('is larger than the size at which the app renders a Markdown body in a worker of its own', () => {
    expect(LARGE_BODY_FILLER_CHARS).toBeGreaterThan(LARGE_MARKDOWN_BODY_CHARS)
    expect(largeBodyFiller().length).toBeGreaterThan(LARGE_MARKDOWN_BODY_CHARS)
  })

  it('is one link definition on one line', () => {
    const filler = largeBodyFiller()

    expect(filler).toMatch(/^\[large-body-filler\]: https:\/\/example\.invalid\/f+$/)
    expect(filler).not.toContain('\n')
  })
})

/** A fresh IndexedDB as the global one, with the database `name` and its object stores `stores`. */
async function databaseWith(name: string, stores: readonly string[]): Promise<void> {
  vi.stubGlobal('indexedDB', new IDBFactory())
  onTestFinished(() => {
    vi.unstubAllGlobals()
  })
  await new Promise<void>((resolve, reject) => {
    const open = indexedDB.open(name, 1)
    open.onupgradeneeded = () => {
      for (const store of stores)
        open.result.createObjectStore(store)
    }
    open.onerror = () => reject(open.error)
    open.onsuccess = () => {
      open.result.close()
      resolve()
    }
  })
}

/** Run `work` in a transaction of `mode` on the store `store` of the database `name`, and return its result. */
function inStore<T>(name: string, store: string, mode: IDBTransactionMode, work: (store: IDBObjectStore) => IDBRequest<T>): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const open = indexedDB.open(name)
    open.onerror = () => reject(open.error)
    open.onsuccess = () => {
      const transaction = open.result.transaction(store, mode)
      const request = work(transaction.objectStore(store))
      transaction.oncomplete = () => {
        open.result.close()
        resolve(request.result)
      }
      transaction.onerror = () => reject(transaction.error)
    }
  })
}

describe('clearObjectStoreIn', () => {
  it('deletes every entry of the store, and keeps the entries of another store', async () => {
    await databaseWith('renders', ['artifacts', 'other'])
    await inStore('renders', 'artifacts', 'readwrite', store => store.put('<p>a</p>', 'md:a'))
    await inStore('renders', 'artifacts', 'readwrite', store => store.put('<p>b</p>', 'md:b'))
    await inStore('renders', 'other', 'readwrite', store => store.put('kept', 'key'))

    await clearObjectStoreIn({ database: 'renders', store: 'artifacts' })

    expect(await inStore('renders', 'artifacts', 'readonly', store => store.count())).toBe(0)
    expect(await inStore('renders', 'other', 'readonly', store => store.count())).toBe(1)
  })

  it('resolves for a store with no entry', async () => {
    await databaseWith('renders', ['artifacts'])

    await expect(clearObjectStoreIn({ database: 'renders', store: 'artifacts' })).resolves.toBeUndefined()
  })

  it('rejects when the database has no such store', async () => {
    await databaseWith('renders', ['other'])

    await expect(clearObjectStoreIn({ database: 'renders', store: 'artifacts' }))
      .rejects
      .toThrow('the database renders has no object store artifacts')
  })
})

describe('clearStoredRowHeights', () => {
  const heights = 'leapmux:u:alice:chat-row-heights:agent-1'
  const otherAccountHeights = 'leapmux:u:bob:chat-row-heights:agent-3'
  const deviceHeights = 'leapmux:chat-row-heights:agent-2'
  const draft = 'leapmux:u:alice:editor-draft:agent-1'
  const blank = 'http://127.0.0.1:4321/__e2e-blank'

  /** A page whose `evaluate` runs the function here, against the fake IndexedDB of the test. */
  const direct = {
    evaluate: async (body: (arg: unknown) => unknown, arg: unknown) => body(arg),
  } as unknown as Page

  beforeEach(() => {
    vi.stubGlobal('indexedDB', new IDBFactory())
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  /** Store one row at each key of `keys`. */
  async function storeRows(keys: readonly string[]): Promise<void> {
    for (const key of keys)
      await writeEntry(direct, key, 'value', Number.MAX_SAFE_INTEGER)
  }

  /**
   * A page of the app that records each `evaluate`, route, and navigation in `events`. It runs `afterFirstEvaluate`
   * after its first `evaluate`, and `onGoto` when the helper leaves the app.
   */
  function appPage(hooks: { afterFirstEvaluate?: () => Promise<void>, onGoto?: () => Promise<void> } = {}) {
    const events: string[] = []
    const page = {
      url: () => 'http://127.0.0.1:4321/workspace/agent-1',
      route: async (url: string) => {
        events.push(`route ${url}`)
      },
      unroute: async (url: string) => {
        events.push(`unroute ${url}`)
      },
      goto: async (url: string) => {
        events.push(`goto ${url}`)
        await hooks.onGoto?.()
      },
      evaluate: async (body: (arg: unknown) => unknown, arg: unknown) => {
        const result = await body(arg)
        events.push('evaluate')
        if (events.filter(event => event === 'evaluate').length === 1)
          await hooks.afterFirstEvaluate?.()
        return result
      },
    } as unknown as Page
    return { page, events }
  }

  it('deletes the row heights of each account and of the device on an empty page, and keeps every other entry', async () => {
    await storeRows([
      heights,
      otherAccountHeights,
      deviceHeights,
      draft,
    ])
    let storedWhenLeaving: string[] = []
    const { page, events } = appPage({
      onGoto: async () => {
        storedWhenLeaving = await storageKeys(direct)
      },
    })

    await clearStoredRowHeights(page)

    expect(events).toEqual([
      'evaluate',
      `route ${blank}`,
      `goto ${blank}`,
      `unroute ${blank}`,
      'evaluate',
      'evaluate',
    ])
    // The app writes the heights that it still owes when it leaves, so the helper deletes nothing before that.
    expect(storedWhenLeaving).toEqual([
      heights,
      otherAccountHeights,
      deviceHeights,
      draft,
    ].sort())
    expect(await storageKeys(direct)).toEqual([draft])
  })

  it('waits until the app saved the row heights before it leaves the app', async () => {
    await storeRows([draft])
    const { page, events } = appPage({
      afterFirstEvaluate: () => storeRows([heights]),
    })

    await clearStoredRowHeights(page)

    expect(events.slice(0, 3)).toEqual([
      'evaluate',
      'evaluate',
      `route ${blank}`,
    ])
    expect(await storageKeys(direct)).toEqual([draft])
  })

  it('fails in the app when no stored key holds row heights by the layout of the app', async () => {
    const others = [
      draft,
      // An account whose id is the prefix of the row heights.
      'leapmux:u:chat-row-heights:browser-prefs',
      // A name that holds the prefix, but does not start with it.
      'leapmux:u:alice:draft:chat-row-heights:agent-1',
      // A key of another family.
      'other:chat-row-heights:agent-1',
    ]
    await storeRows(others)
    const { page, events } = appPage()

    await expect(clearStoredRowHeights(page)).rejects.toThrow('the app stores the row heights of the chat')

    expect(events.filter(event => event !== 'evaluate')).toEqual([])
    expect(await storageKeys(direct)).toEqual([...others].sort())
  })

  it('fails when it deletes no entry on the empty page', async () => {
    await storeRows([heights, draft])
    const { page } = appPage({
      onGoto: async () => {
        await deleteEntries(direct, [heights])
      },
    })

    await expect(clearStoredRowHeights(page)).rejects.toThrow('the helper deletes the stored row heights')
  })
})

describe('frameJumps', () => {
  /**
   * A frame of rows, from [seq, top, height] triples. Each row shows, and keeps one element per seq, unless a fourth
   * member gives its element and a fifth whether it shows.
   */
  const frame = (...rows: ([string, number, number] | [string, number, number, number, boolean?])[]): WatchedFrame =>
    rows.map(([seq, top, height, element = Number(seq), shown = true]) => ({ seq, top, height, element, shown }))

  it('finds no jump when every row moves by one distance and keeps its height', () => {
    expect(frameJumps([
      frame(['1', 0, 100], ['2', 120, 40]),
      frame(['1', 37.5, 100], ['2', 157.5, 40]),
      frame(['1', 37.5, 100], ['2', 157.5, 40]),
    ])).toEqual([])
  })

  // The defect that the frame watch exists for: a row in view lost its header, and the rows below it moved up while the
  // rows above it held.
  it('reports a row that changes its height, and the rows that it moves against the others', () => {
    expect(frameJumps([
      frame(['5406', 141.8, 297.94], ['5407', 447.8, 210.56], ['5408', 678.3, 21]),
      frame(['5406', 141.8, 297.94], ['5407', 447.8, 40.39], ['5408', 508.1, 21]),
    ])).toEqual([
      { frame: 1, kind: 'height', detail: 'the row of seq 5407 changed its height from 210.56px to 40.39px' },
      { frame: 1, kind: 'shear', detail: 'the row of seq 5408 moved -170.2px and the row of seq 5406 moved 0px' },
    ])
  })

  it('reports rows that move by different distances while every height holds', () => {
    expect(frameJumps([
      frame(['1', 0, 100], ['2', 120, 40]),
      frame(['1', 10, 100], ['2', 120, 40]),
    ])).toEqual([
      { frame: 1, kind: 'shear', detail: 'the row of seq 2 moved 0px and the row of seq 1 moved 10px' },
    ])
  })

  it('counts a row that only one of the two frames shows for nothing', () => {
    expect(frameJumps([
      frame(['1', 0, 100]),
      frame(['1', 20, 100], ['2', 140, 300]),
      frame(['2', 50, 300]),
    ])).toEqual([])
  })

  it('accepts a change of 1px and reports a larger one', () => {
    expect(frameJumps([
      frame(['1', 0, 100], ['2', 120, 40]),
      frame(['1', 1, 101], ['2', 120, 40]),
    ])).toEqual([])
    expect(frameJumps([
      frame(['1', 0, 100], ['2', 120, 40]),
      frame(['1', 0, 101.5], ['2', 121.5, 40]),
    ])).toEqual([
      { frame: 1, kind: 'height', detail: 'the row of seq 1 changed its height from 100px to 101.5px' },
      { frame: 1, kind: 'shear', detail: 'the row of seq 1 moved 0px and the row of seq 2 moved 1.5px' },
    ])
  })

  it('finds no jump in no frame, in one frame, and in frames of no row', () => {
    expect(frameJumps([])).toEqual([])
    expect(frameJumps([
      frame(['1', 0, 100]),
    ])).toEqual([])
    expect(frameJumps([
      frame(),
      frame(),
    ])).toEqual([])
  })

  // The defect that the element check exists for: an older page arrived during a fling, and every row in the view
  // unmounted and mounted again in one task, as a skeleton, at the same place.
  it('reports each row in the view that has a new element in the next frame', () => {
    expect(frameJumps([
      frame(['3929', 0, 120, 10], ['3930', 140, 60, 11]),
      frame(['3929', -20, 120, 12], ['3930', 120, 60, 13]),
    ])).toEqual([
      { frame: 1, kind: 'remount', detail: 'the row of seq 3929 mounted again 1 frame(s) after its last sample' },
      { frame: 1, kind: 'remount', detail: 'the row of seq 3930 mounted again 1 frame(s) after its last sample' },
    ])
  })

  it('reports a row that mounts again after frames with no row, up to REMOUNT_WINDOW_FRAMES frames apart', () => {
    const gap = (frames: number) => Array.from({ length: frames }, () => frame())
    expect(frameJumps([
      frame(['1', 0, 100, 1]),
      ...gap(REMOUNT_WINDOW_FRAMES - 1),
      frame(['1', 0, 100, 2]),
    ])).toEqual([
      { frame: REMOUNT_WINDOW_FRAMES, kind: 'remount', detail: `the row of seq 1 mounted again ${REMOUNT_WINDOW_FRAMES} frame(s) after its last sample` },
    ])
  })

  it('counts a new element for nothing when the row left the view for longer than REMOUNT_WINDOW_FRAMES frames', () => {
    // A row that scrolls out of the rendered rows and back unmounts on the way, and comes back as another element.
    expect(frameJumps([
      frame(['1', 0, 100, 1]),
      ...Array.from({ length: REMOUNT_WINDOW_FRAMES }, () => frame()),
      frame(['1', 0, 100, 2]),
    ])).toEqual([])
  })

  it('reports a row that mounts again while it waits for its content, and checks no height or move of such a row', () => {
    expect(frameJumps([
      frame(['1', 0, 100, 1, true], ['2', 120, 40, 2, true]),
      // The row mounts again hidden, with no content yet, so it has no height of its own and stays where it was.
      frame(['1', 10, 0, 3, false], ['2', 130, 40, 2, true]),
      frame(['1', 20, 100, 3, true], ['2', 140, 40, 2, true]),
    ])).toEqual([
      { frame: 1, kind: 'remount', detail: 'the row of seq 1 mounted again 1 frame(s) after its last sample' },
    ])
  })

  it('reports no remount for a row that keeps its element while it waits for its content', () => {
    expect(frameJumps([
      frame(['1', 0, 100, 1, false]),
      frame(['1', 10, 100, 1, true]),
      frame(['1', 20, 100, 1, false]),
    ])).toEqual([])
  })
})

describe('startFrameWatchIn', () => {
  const KEY = 'testFrameWatch'

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

  /** The container of `containerWith`, with the client height of its box: jsdom lays nothing out. */
  function viewWith(rows: { seq: string, top: number, height: number }[]): HTMLElement {
    const container = containerWith(rows)
    Object.defineProperty(container, 'clientHeight', { value: 500 })
    return container
  }

  it('samples each row that reaches into the view, with its element and whether it shows, until the watch stops', () => {
    const frames = installFrames()
    const container = viewWith([
      // Ends exactly at the top of the view.
      { seq: '1', top: 0, height: 100 },
      { seq: '2', top: 90, height: 40 },
      { seq: '3', top: 300, height: 60 },
      // Starts exactly at the bottom of the view.
      { seq: '4', top: 600, height: 60 },
    ])
    const waiting = container.querySelector<HTMLElement>('[data-seq="3"]')!

    startFrameWatchIn(container, KEY)
    frames.runFrames()
    waiting.style.visibility = 'hidden'
    frames.runFrames()
    const watched = stopFrameWatchIn(KEY)
    frames.runFrames()

    expect(watched).toEqual([
      [
        {
          seq: '2',
          top: -10,
          height: 40,
          element: 1,
          shown: true,
        },
        {
          seq: '3',
          top: 200,
          height: 60,
          element: 2,
          shown: true,
        },
      ],
      [
        {
          seq: '2',
          top: -10,
          height: 40,
          element: 1,
          shown: true,
        },
        {
          seq: '3',
          top: 200,
          height: 60,
          element: 2,
          shown: false,
        },
      ],
    ])
    // The stopped watch samples no more frames and leaves no state behind.
    expect(Reflect.get(window, KEY)).toBeUndefined()
  })

  it('gives a row that mounts again a new element, and a row that stays its element', () => {
    const frames = installFrames()
    const container = viewWith([
      { seq: '1', top: 100, height: 100 },
      { seq: '2', top: 220, height: 40 },
    ])
    const remounted = container.querySelector<HTMLElement>('[data-seq="2"]')!

    startFrameWatchIn(container, KEY)
    frames.runFrames()
    const replacement = placeAt(document.createElement('div'), 220, 40)
    replacement.dataset.seq = '2'
    remounted.replaceWith(replacement)
    frames.runFrames()
    const watched = stopFrameWatchIn(KEY)

    expect(watched?.map(rows => rows.map(row => `${row.seq}:${row.element}`))).toEqual([
      ['1:1', '2:2'],
      ['1:1', '2:3'],
    ])
    expect(frameJumps(watched!)).toEqual([
      { frame: 1, kind: 'remount', detail: 'the row of seq 2 mounted again 1 frame(s) after its last sample' },
    ])
  })

  it('returns null when no watch runs under the key', () => {
    expect(stopFrameWatchIn('noFrameWatch')).toBeNull()
  })
})

describe('startRowHeightRecordIn', () => {
  const KEY = 'testRowHeightChanges'

  /** Start a record under KEY, and remove its state after the test. Its listener stays, and writes to no state then. */
  function startRecord(): void {
    startRowHeightRecordIn(KEY)
    onTestFinished(() => {
      Reflect.deleteProperty(window, KEY)
    })
  }

  /** Dispatch the event that the app sends for one commit that changed the height of a row. */
  function dispatchHeight(detail: unknown): void {
    window.dispatchEvent(new CustomEvent('leapmux:chat-row-height', { detail }))
  }

  it('records each change in the order of its arrival, with the fields that the app states', () => {
    startRecord()
    dispatchHeight({
      id: 'a',
      seq: '12',
      source: 'premeasure',
      mounted: false,
      previousHeight: null,
      height: 40.5,
      previousKey: null,
      heightKey: 'k1',
    })
    dispatchHeight({
      id: 'a',
      seq: '12',
      source: 'visible',
      mounted: true,
      previousHeight: 40.5,
      height: 210,
      previousKey: 'k1',
      heightKey: 'k2',
    })

    expect(rowHeightChangesIn(KEY)).toEqual([
      {
        seq: '12',
        source: 'premeasure',
        mounted: false,
        previousHeight: null,
        height: 40.5,
        previousKey: null,
        heightKey: 'k1',
      },
      {
        seq: '12',
        source: 'visible',
        mounted: true,
        previousHeight: 40.5,
        height: 210,
        previousKey: 'k1',
        heightKey: 'k2',
      },
    ])
  })

  it('reads an absent or a malformed optional field as null, and a mounted flag that is not true as false', () => {
    startRecord()
    dispatchHeight({
      source: 'visible',
      height: 0,
      mounted: 'yes',
      previousHeight: '40',
      seq: 12,
    })

    expect(rowHeightChangesIn(KEY)).toEqual([
      {
        seq: null,
        source: 'visible',
        mounted: false,
        previousHeight: null,
        height: 0,
        previousKey: null,
        heightKey: null,
      },
    ])
  })

  it('ignores an event with no detail, an unknown source, or no height', () => {
    startRecord()
    window.dispatchEvent(new Event('leapmux:chat-row-height'))
    dispatchHeight(null)
    dispatchHeight({ source: 'storage', height: 10 })
    dispatchHeight({ source: 'visible', height: '10' })

    expect(rowHeightChangesIn(KEY)).toEqual([])
  })

  it('returns no change when no record runs under the key', () => {
    expect(rowHeightChangesIn('noRowHeightRecord')).toEqual([])
  })
})
