import { render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { LoadingMenu } from '~/components/common/LoadingMenu'
import { menuOptionValues, menuTrigger, pickMenuValue } from './menu'

const MENU = 'probe-menu'

function renderMenu(options: { disabled?: boolean } = {}) {
  const onChange = vi.fn()
  const onOpen = vi.fn()
  render(() => (
    <LoadingMenu
      ariaLabel="Thing"
      value=""
      onChange={onChange}
      onOpen={onOpen}
      emptyLabel="No things"
      options={[
        { value: 'a', label: 'Alpha' },
        { value: 'b', label: 'Beta' },
      ]}
      data-testid={MENU}
      {...options}
    />
  ))
  return { onChange, onOpen }
}

describe('menuOptionValues', () => {
  // A reader opens a menu only while it holds no row, so a second read of a
  // closed menu whose rows stay mounted does not run its `onOpen` again.
  it('opens a menu that never mounted its rows, and does not open it again', () => {
    const { onOpen } = renderMenu()

    expect(menuOptionValues(MENU)).toEqual(['a', 'b'])
    screen.getByTestId(MENU).hidePopover()
    expect(menuOptionValues(MENU)).toEqual(['a', 'b'])

    expect(onOpen).toHaveBeenCalledTimes(1)
    expect(menuTrigger(MENU)).toHaveAttribute('aria-expanded', 'false')
  })

  it('reads a menu whose trigger is disabled as empty, as a user finds it', () => {
    const { onOpen } = renderMenu({ disabled: true })

    expect(menuOptionValues(MENU)).toEqual([])
    expect(onOpen).not.toHaveBeenCalled()
  })
})

describe('pickMenuValue', () => {
  it('activates the option of the value', () => {
    const { onChange } = renderMenu()

    pickMenuValue(MENU, 'b')

    expect(onChange).toHaveBeenCalledExactlyOnceWith('b')
  })

  it('fails for a value that the menu does not offer, and activates nothing', () => {
    const { onChange } = renderMenu()

    expect(() => pickMenuValue(MENU, 'c')).toThrow('The menu probe-menu has no option with the value "c".')
    expect(onChange).not.toHaveBeenCalled()
  })

  it('fails for a menu whose trigger is disabled, which offers no option', () => {
    const { onChange } = renderMenu({ disabled: true })

    expect(() => pickMenuValue(MENU, 'a')).toThrow('The menu probe-menu has no option with the value "a".')
    expect(onChange).not.toHaveBeenCalled()
  })
})
