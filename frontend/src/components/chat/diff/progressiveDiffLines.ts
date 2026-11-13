import type { Accessor, JSX } from 'solid-js'
import type { SliceBudget, SliceScheduler } from '~/lib/idleSlices'
import { createComputed, createEffect, createMemo, createRoot, createSignal, getOwner, on, onCleanup, untrack } from 'solid-js'
import { sliceScheduler } from '~/lib/idleSlices'
import { monotonicNow } from '~/lib/monotonicNow'
import { groupByHunk } from './diffBuilder'
import { diffLineGroup } from './diffStyles.css'

// ---------------------------------------------------------------------------
// The lines of a diff view, mounted and updated in slices
//
// A chat row draws up to DIFF_DRAWN_LINE_LIMIT lines of a diff, and the file
// viewer draws every line. One task that builds them all holds the main thread
// for more than 50 ms at about 500 lines. So a diff view mounts its lines in
// slices: the first slice in the task that starts the work, and each later one
// in an idle period of the browser (see idleSliceScheduler). The lines that a
// gap reveals mount the same way.
//
// The marks of a line (its syntax tokens and its word highlights) often land
// after its text. A change of the marks does no work for any line in the task
// where it lands: it only marks the drawn lines stale. The slices then read the
// marks of each line and apply them in place. The line elements stay, and a
// line that no slice has reached yet keeps its old marks. Marks change no
// height.
//
// Each line mounts in a root of its own, not through `<For>`, and a slice puts
// the nodes of its lines into their group element itself (see diffLineGroup).
// So a slice touches only the lines that it mounts: a `<For>` would build its
// new items after the update that grows its list, which the budget could not
// see, and the container of a `<For>` reconciles every node that it draws.
//
// The roots of the lines are disposed in slices too, after the lines leave the
// document: a view drops all of its lines at once, such as the old layout when
// a switch between unified and split swaps in the new one, and the disposal of
// thousands of roots in one task would hold the main thread as long as their
// mount.
//
// A line costs more to style and lay out than to build, and the browser does
// that work after the slice: at the next forced layout (the row measurer reads
// the row at once), or in the next frame. So each step that changes the
// document defers its estimated style and layout time to the budget of its
// slice, and each idle slice lays out its lines before it ends and learns the
// estimate from the time that it took.
// ---------------------------------------------------------------------------

/**
 * The style and layout time that one step leaves to the browser, in
 * milliseconds, before any slice measured it. Chromium on a desktop machine
 * takes about 0.045 ms for a line of plain text, four times the time to build
 * it. Syntax tokens cost more, and the slices learn that.
 */
const INITIAL_LAYOUT_MS_PER_STEP = 0.05

/**
 * How many steps the initial estimate counts as. The first slices then move
 * the estimate only part of the way, so one slice cannot set it alone.
 */
const INITIAL_LAYOUT_STEPS = 100

/**
 * After how many measured steps a measurement counts half. A slice decays the
 * earlier measurements by its own steps, so a slice of few steps that also lays
 * out other changes in the document moves the estimate little. The history
 * then holds about 1,500 steps, and the estimate moves most of the way to a
 * lasting change of the cost within about 2,000 steps.
 */
const LAYOUT_HISTORY_HALF_LIFE_STEPS = 1000

/** The style and layout time that one step leaves to the browser, as the slices measure it. */
export interface LayoutEstimate {
  /** The estimated style and layout time of one step, in milliseconds. */
  msPerStep: () => number
  /** Learn from a slice whose `steps` took `ms` milliseconds to style and lay out. */
  learn: (steps: number, ms: number) => void
}

/**
 * Create an estimate from no measurement. It keeps the sums of the measured
 * time and steps, and each slice decays the earlier sums by its own steps (see
 * LAYOUT_HISTORY_HALF_LIFE_STEPS) before it adds its own.
 */
export function createLayoutEstimate(): LayoutEstimate {
  let ms = INITIAL_LAYOUT_STEPS * INITIAL_LAYOUT_MS_PER_STEP
  let steps = INITIAL_LAYOUT_STEPS
  return {
    // `steps` never drops to 0: it starts above 0, and a slice keeps a part of it.
    msPerStep: () => ms / steps,
    learn(sliceSteps, sliceMs) {
      if (sliceSteps <= 0)
        return
      const keep = 0.5 ** (sliceSteps / LAYOUT_HISTORY_HALF_LIFE_STEPS)
      ms = ms * keep + Math.max(0, sliceMs)
      steps = steps * keep + sliceSteps
    },
  }
}

/** The estimate of the app. Every diff view shares it: their lines cost alike. */
let layoutEstimate = createLayoutEstimate()

/** Forget what the slices measured, so that a test starts from the initial estimate. */
export function resetLayoutEstimateForTests(): void {
  layoutEstimate = createLayoutEstimate()
}

/**
 * Lay out the document now, and learn from the time that it takes how much
 * style and layout time one step leaves. An idle slice calls it after its
 * `steps`, so its own budget pays for that time, and the next frame finds the
 * work done.
 */
function settleLayout(steps: number): void {
  if (steps === 0)
    return
  const start = monotonicNow()
  document.documentElement.getBoundingClientRect()
  layoutEstimate.learn(steps, monotonicNow() - start)
}

/**
 * Create an element that holds lines that mount in slices: the lines of one
 * hunk, or the lines that one side of a gap reveals. It draws no box (see
 * diffLineGroup).
 */
export function createLineGroupElement(): HTMLElement {
  const element = document.createElement('div')
  element.className = diffLineGroup
  return element
}

/**
 * The DOM nodes of a line that `render` drew. A line keeps its nodes for its
 * whole life, and the slices insert them themselves. So a result that is not
 * DOM nodes is a programming error: text, or the reactive expression that a
 * component such as `<Show>` returns, which could replace its nodes later.
 */
function lineNodes(element: JSX.Element): Node[] {
  const nodes: Node[] = []
  const collect = (value: JSX.Element): void => {
    if (value instanceof Node) {
      nodes.push(value)
      return
    }
    if (Array.isArray(value)) {
      for (const item of value)
        collect(item)
      return
    }
    throw new TypeError(`A line that mounts in slices must render DOM nodes, not ${typeof value}.`)
  }
  collect(element)
  return nodes
}

export interface ProgressiveLinesOptions<M> {
  /**
   * How many lines to draw. When it grows, the new lines mount in slices, the
   * first slice in the task that grows it. When it shrinks, the lines past it
   * leave the document at once.
   */
  count: Accessor<number>
  /**
   * The marks of each line, by its index. Reading the accessor tracks the
   * sources of the marks: when they change, any drawn line may draw other
   * marks, and the slices read and apply them line by line. The function that
   * it returns reads no reactive source.
   */
  marks: Accessor<(index: number) => M>
  /** Whether two marks draw the same. */
  sameMarks: (a: M, b: M) => boolean
  /**
   * Draw line `index` as DOM nodes, whose marks change in place. The nodes
   * stay for the life of the line (see lineNodes).
   */
  render: (index: number, marks: Accessor<M>) => JSX.Element
  /** Put the nodes of line `index`, which has just mounted, into the document. */
  place: (index: number, nodes: readonly Node[]) => void
  /**
   * While true, a drawn line keeps its marks, because the display must not
   * change. A line that mounts meanwhile draws the latest marks, and the drawn
   * lines take theirs after the hold ends.
   */
  holdMarks: Accessor<boolean>
}

export interface ProgressiveLines {
  /** How many lines have mounted, as of the last slice. */
  mountedCount: Accessor<number>
  /** True until every line of `count` has mounted. */
  mounting: Accessor<boolean>
}

/** One mounted line. */
interface MountedLine<M> {
  marks: Accessor<M>
  setMarks: (marks: M) => void
  nodes: readonly Node[]
  dispose: () => void
}

/**
 * Dispose the roots of `lines`, which have left the document, in slices that
 * `scheduler` requests. None is disposed in the caller's task. A line that waits
 * for its slice draws nothing: no one sets its marks again.
 */
function disposeLinesInSlices(scheduler: SliceScheduler, lines: ReadonlyArray<Pick<MountedLine<unknown>, 'dispose'>>): void {
  let next = 0
  const runSlice = (budget: SliceBudget): void => {
    while (next < lines.length) {
      lines[next++]!.dispose()
      if (budget.used())
        break
    }
    if (next < lines.length)
      scheduler.requestSlice(runSlice)
  }
  if (lines.length > 0)
    scheduler.requestSlice(runSlice)
}

/**
 * Mount `options.count` lines in slices, and apply each later change of their
 * marks in slices. Call it while the view sets up: the slices end when its
 * owner is disposed, and later slices dispose the lines (see
 * disposeLinesInSlices). The nodes of the lines stay in the elements that
 * `place` put them in, which the owner removes from the document.
 */
export function createProgressiveLines<M>(options: ProgressiveLinesOptions<M>): ProgressiveLines {
  const scheduler = sliceScheduler()
  const owner = getOwner()
  const mounted: Array<MountedLine<M>> = []
  const [mountedCount, setMountedCount] = createSignal(0)
  let disposed = false
  let cancelSlice: (() => void) | undefined
  // The mounted lines from `staleFrom` to `staleEnd` may draw older marks than
  // the latest. A line that mounts draws the latest marks.
  let staleFrom = 0
  let staleEnd = 0

  const mountNextLine = (marksOf: (index: number) => M): void => {
    const index = mounted.length
    createRoot((dispose) => {
      const [marks, setMarks] = createSignal(marksOf(index))
      const nodes = lineNodes(options.render(index, marks))
      mounted.push({
        marks,
        setMarks: next => setMarks(() => next),
        nodes,
        dispose,
      })
      options.place(index, nodes)
    }, owner)
  }

  // The lines past `count` leave the document at once, and their roots later.
  const unmountLinesFrom = (count: number): void => {
    const removed = mounted.splice(count)
    for (const line of removed) {
      for (const node of line.nodes)
        node.parentNode?.removeChild(node)
    }
    disposeLinesInSlices(scheduler, removed)
    staleEnd = Math.min(staleEnd, mounted.length)
    staleFrom = Math.min(staleFrom, staleEnd)
    setMountedCount(mounted.length)
  }

  /**
   * Take steps until the budget is used or no work is left. A step applies the
   * marks of one stale line, or mounts the next line. The marks come first: a
   * chat row that is still hidden then reveals its lines with their final
   * marks. The first slice runs inside a reactive update, which applies the
   * marks that it sets only after the slice ends, outside its budget, so it
   * only mounts. Return how many steps changed the document.
   */
  const takeSteps = (budget: SliceBudget, applyMarks: boolean): number => {
    const marksOf = untrack(options.marks)
    const count = untrack(options.count)
    const held = !applyMarks || untrack(options.holdMarks)
    let changes = 0
    for (;;) {
      let changed: boolean
      if (!held && staleFrom < staleEnd) {
        const index = staleFrom++
        const line = mounted[index]!
        const next = marksOf(index)
        changed = !options.sameMarks(untrack(line.marks), next)
        if (changed)
          line.setMarks(next)
      }
      else if (mounted.length < count) {
        mountNextLine(marksOf)
        changed = true
      }
      else {
        break
      }
      // A line whose marks did not change leaves no style or layout work.
      if (changed) {
        changes += 1
        budget.defer(layoutEstimate.msPerStep())
      }
      // The budget is checked after each step, so every slice advances the work.
      if (budget.used())
        break
    }
    setMountedCount(mounted.length)
    return changes
  }

  const hasWork = (): boolean => mounted.length < untrack(options.count)
    || (staleFrom < staleEnd && !untrack(options.holdMarks))

  const runSlice = (budget: SliceBudget): void => {
    cancelSlice = undefined
    if (disposed)
      return
    settleLayout(takeSteps(budget, true))
    requestSliceIfWork()
  }

  function requestSliceIfWork(): void {
    if (disposed || cancelSlice !== undefined || !hasWork())
      return
    cancelSlice = scheduler.requestSlice(runSlice)
  }

  createComputed(on(options.count, (count) => {
    if (count < mounted.length)
      unmountLinesFrom(count)
    else if (count > mounted.length)
      takeSteps(scheduler.startSlice(), false)
    requestSliceIfWork()
  }, { defer: true }))

  createComputed(on(options.marks, () => {
    staleFrom = 0
    staleEnd = mounted.length
    requestSliceIfWork()
  }, { defer: true }))

  createEffect(on(options.holdMarks, (held) => {
    if (!held)
      requestSliceIfWork()
  }, { defer: true }))

  untrack(() => takeSteps(scheduler.startSlice(), false))
  requestSliceIfWork()

  onCleanup(() => {
    disposed = true
    cancelSlice?.()
    cancelSlice = undefined
    disposeLinesInSlices(scheduler, mounted.splice(0))
  })

  return {
    mountedCount,
    mounting: () => mountedCount() < options.count(),
  }
}

/** The lines of one hunk, which the view draws as one group. */
export interface HunkLineGroup {
  hunkIndex: number
  /** The element that holds the lines of the hunk (see createLineGroupElement). */
  element: HTMLElement
}

/** The lines of a diff, mounted into one group element per hunk. */
export interface ProgressiveHunkLines {
  /**
   * The groups whose first line has mounted, in order. A group keeps its
   * identity, and its element, for the life of the lines.
   */
  groups: Accessor<readonly HunkLineGroup[]>
  /** How many groups the lines make, mounted or not. */
  groupCount: number
  /** True until the last line mounts. */
  mounting: Accessor<boolean>
}

export interface ProgressiveHunkLinesOptions<L extends { hunkIndex: number }, M> {
  /** The lines of the diff, in order. A diff of other lines needs new progressive lines. */
  lines: readonly L[]
  /** The marks of a line from the latest sources (see ProgressiveLinesOptions.marks). */
  marks: Accessor<(line: L) => M>
  sameMarks: (a: M, b: M) => boolean
  render: (line: L, marks: Accessor<M>) => JSX.Element
  holdMarks: Accessor<boolean>
}

/**
 * Mount the lines of a diff in slices (see createProgressiveLines), each into
 * the group element of its hunk. Call it while the view sets up.
 */
export function createProgressiveHunkLines<L extends { hunkIndex: number }, M>(
  options: ProgressiveHunkLinesOptions<L, M>,
): ProgressiveHunkLines {
  const { lines } = options
  const groups: HunkLineGroup[] = []
  /** The index of the first line of each group. */
  const starts: number[] = []
  let start = 0
  for (const hunkLines of groupByHunk(lines)) {
    const hunkIndex = hunkLines[0]!.hunkIndex
    const element = createLineGroupElement()
    element.dataset.diffHunk = String(hunkIndex)
    groups.push({ hunkIndex, element })
    starts.push(start)
    start += hunkLines.length
  }

  // The lines mount in order, so the group that takes the next line only moves forward.
  let placing = 0
  const progressive = createProgressiveLines<M>({
    count: () => lines.length,
    marks: () => {
      const marksOf = options.marks()
      return index => marksOf(lines[index]!)
    },
    sameMarks: options.sameMarks,
    render: (index, marks) => options.render(lines[index]!, marks),
    place(index, nodes) {
      while (placing + 1 < starts.length && starts[placing + 1]! <= index)
        placing += 1
      groups[placing]!.element.append(...nodes)
    },
    holdMarks: options.holdMarks,
  })

  /** How many groups have mounted their first line. */
  const startedGroups = createMemo(() => {
    const mountedLines = progressive.mountedCount()
    let low = 0
    let high = starts.length
    while (low < high) {
      const middle = (low + high) >> 1
      if (starts[middle]! < mountedLines)
        low = middle + 1
      else
        high = middle
    }
    return low
  })
  return {
    groups: createMemo(() => groups.slice(0, startedGroups())),
    groupCount: groups.length,
    mounting: progressive.mounting,
  }
}
