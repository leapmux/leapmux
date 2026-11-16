import { IDBFactory } from 'fake-indexeddb'
import { afterEach, describe, expect, it, onTestFinished, vi } from 'vitest'
import { PREFIX_CHAT_ROW_HEIGHTS } from '~/lib/browserStorage'
import { LARGE_MARKDOWN_BODY_CHARS } from '~/lib/markdownWorkerClient'
import { createManualFrames } from '~/test-support/manualFrames'
import {
  clearObjectStoreIn,
  contentBoxWidthIn,
  deleteStoredNamesIn,
  heldReplyCountIn,
  installReplyHoldIn,
  LARGE_BODY_FILLER_CHARS,
  largeBodyFiller,
  markedParagraphs,
  mountedRowSeqsIn,
  paragraphMarker,
  premeasureCommitIn,
  releaseHeldRepliesIn,
  rowBoxIn,
  rowDistanceIn,
  rowSeqWithTextIn,
  rowStatesIn,
  shortTurn,
  startPremeasureRecordIn,
  startRowWatchIn,
  stopRowWatchIn,
  STORED_ROW_HEIGHTS_NAME_PREFIX,
  topParagraphIn,
  topRowIn,
  widthSensitiveParagraphs,
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

/**
 * A fresh IndexedDB as the global one, with the database `name` and its object stores `stores`. Each store takes
 * `options`, such as the key path of its entries.
 */
async function databaseWith(name: string, stores: readonly string[], options?: IDBObjectStoreParameters): Promise<void> {
  vi.stubGlobal('indexedDB', new IDBFactory())
  onTestFinished(() => {
    vi.unstubAllGlobals()
  })
  await new Promise<void>((resolve, reject) => {
    const open = indexedDB.open(name, 1)
    open.onupgradeneeded = () => {
      for (const store of stores)
        open.result.createObjectStore(store, options)
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

describe('deleteStoredNamesIn', () => {
  /**
   * A fresh IndexedDB as the global one, with the object store `entries` of the database `kv`, keyed by `k`, and one
   * entry for each key of `rows`.
   */
  async function storageWith(rows: readonly string[]): Promise<void> {
    await databaseWith('kv', ['entries'], { keyPath: 'k' })
    for (const k of rows)
      await inStore('kv', 'entries', 'readwrite', store => store.put({ k, v: 'value', e: 0 }))
  }

  /** The keys that the object store `entries` holds, in order. */
  async function storedKeys(): Promise<string[]> {
    return (await inStore('kv', 'entries', 'readonly', store => store.getAllKeys())).map(String)
  }

  it('deletes each entry whose logical name starts with the prefix, for an account and for the device', async () => {
    await storageWith([
      'leapmux:u:alice:chat-row-heights:agent-1',
      'leapmux:u:bob:chat-row-heights:agent-2',
      'leapmux:chat-row-heights:agent-3',
    ])

    await expect(deleteStoredNamesIn({ database: 'kv', store: 'entries', namePrefix: 'chat-row-heights:' })).resolves.toBe(3)

    expect(await storedKeys()).toEqual([])
  })

  it('keeps every other entry', async () => {
    const kept = [
      // Another name of the same account.
      'leapmux:u:alice:browser-prefs',
      // A name that holds the prefix, but does not start with it.
      'leapmux:u:alice:draft:chat-row-heights:agent-1',
      // An account whose id is the prefix is no name.
      'leapmux:u:chat-row-heights:browser-prefs',
      // A key of another family.
      'other:chat-row-heights:agent-1',
    ]
    await storageWith([
      ...kept,
      'leapmux:u:alice:chat-row-heights:agent-1',
    ])

    await expect(deleteStoredNamesIn({ database: 'kv', store: 'entries', namePrefix: 'chat-row-heights:' })).resolves.toBe(1)

    expect(await storedKeys()).toEqual([...kept].sort())
  })

  it('resolves 0 for a store with no entry', async () => {
    await storageWith([])

    await expect(deleteStoredNamesIn({ database: 'kv', store: 'entries', namePrefix: 'chat-row-heights:' })).resolves.toBe(0)
  })

  it('rejects when the database has no such store', async () => {
    await storageWith([])

    await expect(deleteStoredNamesIn({ database: 'kv', store: 'renamed', namePrefix: 'chat-row-heights:' }))
      .rejects
      .toThrow('the database kv has no object store renamed')
  })
})

describe('STORED_ROW_HEIGHTS_NAME_PREFIX', () => {
  it('equals the prefix under which the app stores the row heights', () => {
    expect(STORED_ROW_HEIGHTS_NAME_PREFIX).toBe(PREFIX_CHAT_ROW_HEIGHTS)
  })
})
