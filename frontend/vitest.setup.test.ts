import { afterEach, describe, expect, it, vi } from 'vitest'
import { sliceScheduler } from '~/lib/idleSlices'
import { holdWorkerReplies, recordWorkerRequests } from '~/test-support/fakeWorker'
import { installControlledSlices, unlimitedSliceScheduler } from '~/test-support/sliceSchedulers'

afterEach(() => {
  document.body.replaceChildren()
})

function appendPopoverInput() {
  const popover = document.createElement('div')
  popover.setAttribute('popover', 'auto')
  const input = document.createElement('input')
  popover.append(input)
  document.body.append(popover)
  return { popover, input }
}

describe('popover test stubs', () => {
  it('allows focus inside an open popover and refuses focus after it closes', () => {
    const { popover, input } = appendPopoverInput()

    input.focus()
    expect(document.activeElement).not.toBe(input)

    popover.showPopover()
    input.focus()
    expect(document.activeElement).toBe(input)

    input.blur()
    popover.hidePopover()
    input.focus()
    expect(document.activeElement).not.toBe(input)
  })

  it.each(['showPopover', 'togglePopover'] as const)('keeps a cancelled %s hidden and allows the next opening', (method) => {
    const { popover, input } = appendPopoverInput()
    const onToggle = vi.fn()
    popover.addEventListener('toggle', onToggle)
    popover.addEventListener('beforetoggle', event => event.preventDefault(), { once: true })

    popover[method]()
    expect(popover.matches(':popover-open')).toBe(false)
    expect(onToggle).not.toHaveBeenCalled()
    input.focus()
    expect(document.activeElement).not.toBe(input)

    popover.showPopover()
    expect(onToggle).toHaveBeenCalledExactlyOnceWith(expect.any(Event))
    input.focus()
    expect(document.activeElement).toBe(input)
  })
})

/** Every byte that a stream yields, in order. */
async function readAll(stream: ReadableStream<Uint8Array>): Promise<number[]> {
  const bytes: number[] = []
  const reader = stream.getReader()
  for (;;) {
    const { done, value } = await reader.read()
    if (done)
      return bytes
    bytes.push(...value)
  }
}

describe('Blob stream stand-in', () => {
  it('yields the bytes of the blob', async () => {
    const blob = new Blob([
      new Uint8Array([0, 1, 255]),
      'A',
    ])

    await expect(readAll(blob.stream())).resolves.toEqual([0, 1, 255, 65])
  })

  it('yields no byte for an empty blob, and then ends', async () => {
    await expect(readAll(new Blob([]).stream())).resolves.toEqual([])
  })
})

// The global hooks reset this state around each test. Each run below checks the
// state at its start, then leaves it changed, so the second run proves the reset
// after a test that changed it:
// - The app's own scheduler is the default of the module, so the first check
//   fails unless the setup installs the unlimited one.
// - A hold or a record that stays refuses a second one for its worker module.
describe('per-test state of the setup', () => {
  it.each([1, 2])('starts with the unlimited slice scheduler, and with no hold or record of a worker (run %i)', () => {
    expect(sliceScheduler()).toBe(unlimitedSliceScheduler)
    expect(() => holdWorkerReplies('components/chat/diff/diffWorker.ts')).not.toThrow()
    expect(() => recordWorkerRequests('lib/markdownWorker.ts')).not.toThrow()

    installControlledSlices(1)
    expect(sliceScheduler()).not.toBe(unlimitedSliceScheduler)
  })
})
