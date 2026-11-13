import type { Accessor, JSX } from 'solid-js'
import type { ProgressiveHunkLines, ProgressiveLines } from './progressiveDiffLines'
import { createRenderEffect, createRoot, createSignal, onCleanup } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { installControlledSlices } from '~/test-support/sliceSchedulers'
import { createLayoutEstimate, createProgressiveHunkLines, createProgressiveLines } from './progressiveDiffLines'

/** Lines that draw `<index>:<mark>` into paragraphs of one parent element. */
function mountParagraphs(options: {
  count: Accessor<number>
  marks?: Accessor<(index: number) => string>
  holdMarks?: Accessor<boolean>
}) {
  const parent = document.createElement('div')
  const disposedLines: number[] = []
  let lines!: ProgressiveLines
  const dispose = createRoot((disposeRoot) => {
    lines = createProgressiveLines<string>({
      count: options.count,
      marks: options.marks ?? (() => () => 'plain'),
      sameMarks: (a, b) => a === b,
      render(index, mark) {
        const paragraph = document.createElement('p')
        createRenderEffect(() => {
          paragraph.textContent = `${index}:${mark()}`
        })
        onCleanup(() => disposedLines.push(index))
        return paragraph
      },
      place: (_, nodes) => parent.append(...nodes),
      holdMarks: options.holdMarks ?? (() => false),
    })
    return disposeRoot
  })
  return {
    lines,
    dispose,
    disposedLines,
    paragraphs: () => [...parent.children],
    texts: () => [...parent.children].map(paragraph => paragraph.textContent),
  }
}

describe('createProgressiveLines', () => {
  it('mounts one slice of lines in the task that grows the count, and the rest as the scheduler runs', () => {
    const slices = installControlledSlices(3)
    const [count, setCount] = createSignal(0)
    const view = mountParagraphs({ count })
    expect(view.texts()).toEqual([])
    expect(view.lines.mounting()).toBe(false)
    expect(slices.waiting()).toBe(0)

    setCount(7)

    expect(view.texts()).toEqual(['0:plain', '1:plain', '2:plain'])
    expect(view.lines.mounting()).toBe(true)
    slices.runSlice()
    expect(view.lines.mountedCount()).toBe(6)
    expect(slices.runAllSlices()).toBe(1)
    expect(view.texts()).toHaveLength(7)
    expect(view.lines.mounting()).toBe(false)
    view.dispose()
  })

  it('removes the lines past a count that shrinks at once, disposes them in slices, and mounts them again when it grows', () => {
    const slices = installControlledSlices(3)
    const [count, setCount] = createSignal(5)
    const view = mountParagraphs({ count })
    slices.runAllSlices()
    const kept = view.paragraphs().slice(0, 2)

    setCount(2)

    expect(view.texts()).toEqual(['0:plain', '1:plain'])
    expect(view.paragraphs()[0]).toBe(kept[0])
    expect(view.paragraphs()[1]).toBe(kept[1])
    expect(view.disposedLines).toEqual([])
    expect(view.lines.mounting()).toBe(false)
    // The one slice that waits disposes the three lines, and mounts nothing.
    expect(slices.runAllSlices()).toBe(1)
    expect(view.disposedLines.toSorted((a, b) => a - b)).toEqual([2, 3, 4])
    expect(view.texts()).toEqual(['0:plain', '1:plain'])

    setCount(4)

    expect(view.texts()).toEqual([
      '0:plain',
      '1:plain',
      '2:plain',
      '3:plain',
    ])
    view.dispose()
  })

  it('reads the marks of no line when they change, and applies them in slices to the same elements', () => {
    const slices = installControlledSlices(2)
    const [mark, setMark] = createSignal('plain')
    let reads = 0
    const view = mountParagraphs({
      count: () => 5,
      marks: () => {
        const current = mark()
        return () => {
          reads += 1
          return current
        }
      },
    })
    slices.runAllSlices()
    const drawn = view.paragraphs()
    reads = 0

    setMark('bold')

    expect(reads).toBe(0)
    slices.runSlice()
    expect(reads).toBe(2)
    expect(view.texts()).toEqual([
      '0:bold',
      '1:bold',
      '2:plain',
      '3:plain',
      '4:plain',
    ])
    slices.runAllSlices()
    expect(view.texts()).toEqual([
      '0:bold',
      '1:bold',
      '2:bold',
      '3:bold',
      '4:bold',
    ])
    for (const [i, paragraph] of view.paragraphs().entries())
      expect(paragraph).toBe(drawn[i])
    view.dispose()
  })

  it('keeps the marks of its drawn lines while the display holds, draws the latest marks on a line that mounts meanwhile, and applies them after', () => {
    const slices = installControlledSlices(2)
    const [mark, setMark] = createSignal('plain')
    const [held, setHeld] = createSignal(false)
    const [count, setCount] = createSignal(2)
    const view = mountParagraphs({
      count,
      marks: () => {
        const current = mark()
        return () => current
      },
      holdMarks: held,
    })
    setHeld(true)

    setMark('bold')
    setCount(3)
    slices.runAllSlices()

    expect(view.texts()).toEqual(['0:plain', '1:plain', '2:bold'])
    setHeld(false)
    slices.runAllSlices()
    expect(view.texts()).toEqual(['0:bold', '1:bold', '2:bold'])
    view.dispose()
  })

  it('cancels its waiting slice when its owner is disposed, and disposes its lines in a slice of their own', () => {
    const slices = installControlledSlices(2)
    const view = mountParagraphs({
      count: () => 5,
    })
    expect(slices.waiting()).toBe(1)

    view.dispose()

    expect(view.disposedLines).toEqual([])
    // The slice that waits disposes the two lines that mounted, and mounts no other.
    expect(slices.runAllSlices()).toBe(1)
    expect(view.disposedLines.toSorted((a, b) => a - b)).toEqual([0, 1])
    expect(view.texts()).toEqual(['0:plain', '1:plain'])
  })

  it('disposes no line in the task that disposes its owner, and every line as the slices run, a slice\'s budget at a time', () => {
    const slices = installControlledSlices(3)
    const view = mountParagraphs({
      count: () => 7,
    })
    slices.runAllSlices()

    view.dispose()

    expect(view.disposedLines).toEqual([])
    slices.runSlice()
    expect(view.disposedLines).toHaveLength(3)
    slices.runSlice()
    expect(view.disposedLines).toHaveLength(6)
    expect(slices.runAllSlices()).toBe(1)
    expect(view.disposedLines.toSorted((a, b) => a - b)).toEqual([0, 1, 2, 3, 4, 5, 6])
  })

  it('requests no slice to dispose an owner that mounted no line', () => {
    const slices = installControlledSlices(3)
    const view = mountParagraphs({
      count: () => 0,
    })

    view.dispose()

    expect(slices.waiting()).toBe(0)
  })

  it('refuses a line that renders text in place of DOM nodes', () => {
    installControlledSlices(2)

    expect(() => createRoot((dispose) => {
      createProgressiveLines({
        count: () => 1,
        marks: () => () => 'plain',
        sameMarks: (a, b) => a === b,
        render: () => 'a line of text',
        place: () => {},
        holdMarks: () => false,
      })
      return dispose
    })).toThrow(TypeError)
  })

  it('refuses a line that renders a reactive expression, which could replace its nodes later', () => {
    installControlledSlices(2)
    const paragraph = document.createElement('p')
    // What a component such as `<Show>` returns at run time, which its type
    // states as a JSX element.
    const reactiveExpression = (() => paragraph) as unknown as JSX.Element

    expect(() => createRoot((dispose) => {
      createProgressiveLines({
        count: () => 1,
        marks: () => () => 'plain',
        sameMarks: (a, b) => a === b,
        render: () => reactiveExpression,
        place: () => {},
        holdMarks: () => false,
      })
      return dispose
    })).toThrow('A line that mounts in slices must render DOM nodes, not function.')
  })

  it('places every node of a line that renders several, from nested arrays, in order', () => {
    installControlledSlices(2)
    const parent = document.createElement('div')
    const node = (text: string): HTMLElement => {
      const span = document.createElement('span')
      span.textContent = text
      return span
    }

    const dispose = createRoot((disposeRoot) => {
      createProgressiveLines({
        count: () => 2,
        marks: () => () => 'plain',
        sameMarks: (a, b) => a === b,
        render: index => [
          node(`${index}a`),
          [
            node(`${index}b`),
            node(`${index}c`),
          ],
        ],
        place: (_, nodes) => parent.append(...nodes),
        holdMarks: () => false,
      })
      return disposeRoot
    })

    expect([...parent.children].map(child => child.textContent)).toEqual(['0a', '0b', '0c', '1a', '1b', '1c'])
    dispose()
  })

  it('applies the new marks only to the lines that stay, when the count shrinks while their marks wait', () => {
    const slices = installControlledSlices(2)
    const [mark, setMark] = createSignal('plain')
    const [count, setCount] = createSignal(5)
    const view = mountParagraphs({
      count,
      marks: () => {
        const current = mark()
        return () => current
      },
    })
    slices.runAllSlices()

    // Every drawn line waits for its new marks, and then three of them leave.
    setMark('bold')
    setCount(2)

    expect(() => slices.runAllSlices()).not.toThrow()
    expect(view.texts()).toEqual(['0:bold', '1:bold'])
    expect(view.lines.mountedCount()).toBe(2)
    view.dispose()
  })

  it('keeps one waiting slice when the count grows again before the slice runs', () => {
    const slices = installControlledSlices(2)
    const [count, setCount] = createSignal(0)
    const view = mountParagraphs({ count })

    setCount(5)
    expect(slices.waiting()).toBe(1)
    setCount(9)

    expect(slices.waiting()).toBe(1)
    // The first task of each growth mounted one slice of two lines.
    expect(view.lines.mountedCount()).toBe(4)
    slices.runAllSlices()
    expect(view.texts()).toHaveLength(9)
    view.dispose()
  })
})

describe('createProgressiveHunkLines', () => {
  /** Lines of three hunks: three lines, then one, then two. */
  const LINES = [0, 0, 0, 1, 2, 2].map((hunkIndex, i) => ({ hunkIndex, text: `line ${i}` }))

  function mountHunks() {
    let hunkLines!: ProgressiveHunkLines
    const dispose = createRoot((disposeRoot) => {
      hunkLines = createProgressiveHunkLines<{ hunkIndex: number, text: string }, string>({
        lines: LINES,
        marks: () => () => 'plain',
        sameMarks: (a, b) => a === b,
        render: (line) => {
          const paragraph = document.createElement('p')
          paragraph.textContent = line.text
          return paragraph
        },
        holdMarks: () => false,
      })
      return disposeRoot
    })
    const groupTexts = () => hunkLines.groups().map(group => [
      group.hunkIndex,
      [...group.element.children].map(child => child.textContent),
    ])
    return { hunkLines, dispose, groupTexts }
  }

  it('puts each line into the element of its hunk, and lists a group only once its first line has mounted', () => {
    const slices = installControlledSlices(3)
    const { hunkLines, dispose, groupTexts } = mountHunks()

    expect(hunkLines.groupCount).toBe(3)
    // The first slice fills the first hunk, and the second hunk has no line yet.
    expect(groupTexts()).toEqual([
      [
        0,
        ['line 0', 'line 1', 'line 2'],
      ],
    ])
    expect(hunkLines.mounting()).toBe(true)

    slices.runSlice()

    expect(groupTexts()).toEqual([
      [
        0,
        ['line 0', 'line 1', 'line 2'],
      ],
      [
        1,
        ['line 3'],
      ],
      [
        2,
        ['line 4', 'line 5'],
      ],
    ])
    expect(hunkLines.mounting()).toBe(false)
    dispose()
  })

  it('keeps the identity and the element of each group as later lines mount', () => {
    const slices = installControlledSlices(4)
    const { hunkLines, dispose } = mountHunks()
    const [first, second] = hunkLines.groups()

    slices.runAllSlices()

    expect(hunkLines.groups()[0]).toBe(first)
    expect(hunkLines.groups()[1]).toBe(second)
    expect(second?.element.dataset.diffHunk).toBe('1')
    dispose()
  })
})

/** The style and layout time of one line of plain text in Chromium, in milliseconds. */
const PLAIN_LINE_MS = 0.05

describe('createLayoutEstimate', () => {
  /** An estimate that has learned from many slices of 160 lines at `msPerStep` each. */
  function steadyEstimate(msPerStep: number) {
    const estimate = createLayoutEstimate()
    for (let i = 0; i < 50; i++)
      estimate.learn(160, 160 * msPerStep)
    return estimate
  }

  it('settles on the time per step that the slices measure', () => {
    // Four times the initial estimate, so that an estimate that ignores what it
    // learns fails. After 8,000 steps, the initial estimate still counts for
    // about 0.4 of a step, so the estimate is within 0.0005 ms of the measure.
    const estimate = steadyEstimate(4 * PLAIN_LINE_MS)

    expect(estimate.msPerStep()).toBeCloseTo(4 * PLAIN_LINE_MS, 3)
  })

  it('moves little when a slice of two steps also pays 10 ms of layout that other changes left', () => {
    const estimate = steadyEstimate(PLAIN_LINE_MS)
    const steady = estimate.msPerStep()

    estimate.learn(2, 2 * PLAIN_LINE_MS + 10)

    expect(estimate.msPerStep() / steady).toBeLessThan(1.25)
  })

  it('follows lines that cost more within a few thousand steps', () => {
    const estimate = steadyEstimate(PLAIN_LINE_MS)

    for (let i = 0; i < 20; i++)
      estimate.learn(160, 160 * 3 * PLAIN_LINE_MS)

    expect(estimate.msPerStep()).toBeGreaterThan(2.5 * PLAIN_LINE_MS)
  })

  it('learns nothing from a slice of no step', () => {
    const estimate = steadyEstimate(PLAIN_LINE_MS)
    const steady = estimate.msPerStep()

    estimate.learn(0, 10)

    expect(estimate.msPerStep()).toBe(steady)
  })

  it('learns nothing from a slice of a negative step count', () => {
    const estimate = steadyEstimate(PLAIN_LINE_MS)
    const steady = estimate.msPerStep()

    estimate.learn(-160, 10)

    expect(estimate.msPerStep()).toBe(steady)
  })

  it('learns a negative measured time as no time, and never estimates below zero', () => {
    const negative = steadyEstimate(PLAIN_LINE_MS)
    const zero = steadyEstimate(PLAIN_LINE_MS)

    negative.learn(160, -1_000)
    zero.learn(160, 0)

    expect(negative.msPerStep()).toBe(zero.msPerStep())
    expect(negative.msPerStep()).toBeGreaterThan(0)
  })

  it('starts at an estimate that the first slice cannot set alone', () => {
    const estimate = createLayoutEstimate()
    expect(estimate.msPerStep()).toBeGreaterThan(0)

    estimate.learn(1, 10)

    // The slice measured 10 ms for its one step, and the estimate moves a small part of the way.
    expect(estimate.msPerStep()).toBeLessThan(0.2)
  })
})
