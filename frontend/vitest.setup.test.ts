import { afterEach, describe, expect, it, vi } from 'vitest'

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
