import type { Locator, Page } from '@playwright/test'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fakeLocator } from '~/test-support/fakeLocator'
import { boxCenter, dragSidebarLeafTo, mouseDragOnto } from './drag'

vi.mock('./ui', () => ({
  boxOf: async (locator: { box: { x: number, y: number, width: number, height: number } }) => locator.box,
}))

afterEach(() => {
  vi.unstubAllGlobals()
})

/** A page whose mouse and frame waits append to `log`. */
function recordingPage(log: string[]): Page {
  return {
    mouse: {
      move: vi.fn(async (x: number, y: number, options?: { steps?: number }) => {
        log.push(options?.steps === undefined ? `move ${x},${y}` : `move ${x},${y} in ${options.steps}`)
      }),
      down: vi.fn(async () => {
        log.push('down')
      }),
      up: vi.fn(async () => {
        log.push('up')
      }),
    },
    // `settleFrames` waits for two frames through one evaluate.
    evaluate: vi.fn(async () => {
      log.push('frames')
    }),
  } as unknown as Page
}

/** A row whose class assertions append to `log` and answer from `dragging`. */
function draggedRow(log: string[], dragging: () => boolean): Locator {
  return fakeLocator((check) => {
    log.push(`${check.isNot ? 'not ' : ''}${check.expression}`)
    const matches = dragging()
    return { matches, received: matches ? 'dragging' : 'still' }
  })
}

describe('boxCenter', () => {
  it('returns the center of the box', async () => {
    const locator = { box: { x: 10, y: 20, width: 100, height: 40 } } as unknown as Locator
    await expect(boxCenter(locator)).resolves.toEqual({ x: 60, y: 40 })
  })
})

describe('mouseDragOnto', () => {
  it('presses, moves past the activation distance, moves to the target in steps, settles, and releases', async () => {
    const log: string[] = []
    await mouseDragOnto(recordingPage(log), { from: { x: 100, y: 50 }, to: { x: 400, y: 300 } })
    expect(log).toEqual(['move 100,50', 'down', 'move 108,70', 'move 400,300 in 12', 'frames', 'up'])
  })

  it('uses the step count of the caller', async () => {
    const log: string[] = []
    await mouseDragOnto(recordingPage(log), { from: { x: 0, y: 0 }, to: { x: 90, y: 5 }, steps: 15 })
    expect(log).toContain('move 90,5 in 15')
  })

  it.each([0, -1, 1.5, Number.NaN])('refuses %s steps before it presses the button', async (steps) => {
    const log: string[] = []
    await expect(mouseDragOnto(recordingPage(log), { from: { x: 0, y: 0 }, to: { x: 1, y: 1 }, steps })).rejects.toThrow(RangeError)
    expect(log).toEqual([])
  })

  it('requires the dragging class during the drag and its absence after the release', async () => {
    const log: string[] = []
    let dragging = false
    const page = recordingPage(log)
    vi.mocked(page.mouse.move).mockImplementation(async (x: number, y: number, options?: { steps?: number }) => {
      log.push(options?.steps === undefined ? `move ${x},${y}` : `move ${x},${y} in ${options.steps}`)
      if (log.includes('down'))
        dragging = true
    })
    vi.mocked(page.mouse.up).mockImplementation(async () => {
      log.push('up')
      dragging = false
    })
    // A negated class assertion passes while `matches` is false, so the row answers the drag state directly.
    await mouseDragOnto(page, {
      from: { x: 0, y: 0 },
      to: { x: 200, y: 0 },
      dragged: { row: draggedRow(log, () => dragging), draggingClass: /tabDragging/ },
    })
    expect(log).toEqual(['move 0,0', 'down', 'move 8,20', 'to.have.class', 'move 200,0 in 12', 'frames', 'up', 'not to.have.class'])
  })

  it('runs the lifted check after the drag starts and before the move to the target', async () => {
    const log: string[] = []
    await mouseDragOnto(recordingPage(log), {
      from: { x: 0, y: 0 },
      to: { x: 0, y: 200 },
      whileLifted: async () => {
        log.push('lifted check')
      },
    })
    expect(log).toEqual(['move 0,0', 'down', 'move 8,20', 'lifted check', 'move 0,200 in 12', 'frames', 'up'])
  })

  it('releases the button when the lifted check fails, and reports that failure', async () => {
    const log: string[] = []
    const failure = new Error('The lifted row widened the queue.')
    await expect(mouseDragOnto(recordingPage(log), {
      from: { x: 0, y: 0 },
      to: { x: 0, y: 200 },
      whileLifted: async () => {
        throw failure
      },
    })).rejects.toBe(failure)
    expect(log).toEqual(['move 0,0', 'down', 'move 8,20', 'up'])
  })

  it('releases the button when the press never starts a drag, and reports the failed press', async () => {
    const log: string[] = []
    const page = recordingPage(log)
    await expect(mouseDragOnto(page, {
      from: { x: 0, y: 0 },
      to: { x: 200, y: 0 },
      dragged: { row: draggedRow(log, () => false), draggingClass: /tabDragging/ },
    })).rejects.toThrow('the press started a drag')
    expect(log.at(-1)).toBe('up')
    expect(log).not.toContain('move 200,0 in 12')
  })
})

describe('dragSidebarLeafTo', () => {
  /** Run the in-page body of the drag against a jsdom element, and record each pointer event it dispatches. */
  async function runLeafDrag(target: { x: number, y: number }) {
    class FakePointerEvent extends MouseEvent {
      readonly pointerId: number
      readonly isPrimary: boolean
      constructor(type: string, init: PointerEventInit) {
        super(type, init)
        this.pointerId = init.pointerId ?? 0
        this.isPrimary = init.isPrimary ?? false
      }
    }
    vi.stubGlobal('PointerEvent', FakePointerEvent)
    vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => setTimeout(callback, 0, 0))
    const element = document.createElement('div')
    document.body.append(element)
    element.getBoundingClientRect = () => ({ x: 20, y: 100, width: 200, height: 30 }) as DOMRect
    const events: Array<{ type: string, on: string, x: number, y: number, buttons: number }> = []
    const record = (on: string) => (event: Event) => {
      const pointer = event as MouseEvent
      events.push({ type: event.type, on, x: pointer.clientX, y: pointer.clientY, buttons: pointer.buttons })
    }
    element.addEventListener('pointerdown', record('leaf'))
    document.addEventListener('pointermove', record('document'))
    document.addEventListener('pointerup', record('document'))
    const leaf = {
      evaluate: async (body: (element: Element, argument: unknown) => Promise<void>, argument: unknown) => body(element, argument),
    } as unknown as Locator
    try {
      await dragSidebarLeafTo(leaf, target)
    }
    finally {
      element.remove()
    }
    return events
  }

  it('presses the leaf, moves past the activation distance, moves to the target, and releases there', async () => {
    const events = await runLeafDrag({ x: 330, y: 315 })
    expect(events[0]).toEqual({ type: 'pointerdown', on: 'leaf', x: 30, y: 115, buttons: 1 })
    expect(events[1]).toEqual({ type: 'pointermove', on: 'document', x: 38, y: 135, buttons: 1 })
    const moves = events.filter(event => event.type === 'pointermove')
    expect(moves).toHaveLength(11)
    expect(moves.at(-1)).toMatchObject({ x: 330, y: 315, buttons: 1 })
    expect(events.at(-1)).toEqual({ type: 'pointerup', on: 'document', x: 330, y: 315, buttons: 0 })
  })
})
