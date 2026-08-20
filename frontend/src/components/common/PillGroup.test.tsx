import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { PillGroup } from './PillGroup'

/**
 * The preference pills are one-of-N groups, so they carry radiogroup/radio
 * semantics rather than aria-pressed.
 *
 * They shipped as four identical stateless buttons; aria-pressed fixed the
 * "stateless" half and still described the wrong widget — a toggle button
 * announces "pressed", promises it can be un-pressed, and carries no group name
 * and no set position. These pin what a screen reader can actually reach.
 */
describe('pillGroup', () => {
  const options = [
    { value: null, label: 'Use account default' },
    { value: 'dark', label: 'Dark' },
    { value: 'light', label: 'Light' },
  ]

  function renderGroup(current: string | null, onSelect = vi.fn()) {
    render(() => (
      <PillGroup
        label="Theme"
        options={options}
        selected={v => v === current}
        onSelect={onSelect}
      />
    ))
    return onSelect
  }

  it('exposes a named group, not a row of anonymous buttons', () => {
    renderGroup('dark')
    // The visible <h3> is not associated with the group in the a11y tree, so
    // the aria-label is the only name AT ever announces on entry.
    expect(screen.getByRole('radiogroup', { name: 'Theme' })).toBeTruthy()
    expect(screen.getAllByRole('radio')).toHaveLength(3)
  })

  it('marks exactly one option checked', () => {
    renderGroup('dark')
    expect(screen.getByRole('radio', { name: 'Dark' }).getAttribute('aria-checked')).toBe('true')
    expect(screen.getByRole('radio', { name: 'Light' }).getAttribute('aria-checked')).toBe('false')
    expect(screen.getByRole('radio', { name: 'Use account default' }).getAttribute('aria-checked')).toBe('false')
  })

  it('puts only the checked option in the tab order', () => {
    // Roving tabindex is what the role requires: Tab reaches the GROUP, the
    // arrows move within it. Without it every pill is its own tab stop.
    renderGroup('dark')
    expect(screen.getByRole('radio', { name: 'Dark' }).getAttribute('tabindex')).toBe('0')
    expect(screen.getByRole('radio', { name: 'Light' }).getAttribute('tabindex')).toBe('-1')
  })

  it('moves the selection with the arrow keys, wrapping at the ends', () => {
    const onSelect = renderGroup('dark')
    const group = screen.getByRole('radiogroup', { name: 'Theme' })

    fireEvent.keyDown(group, { key: 'ArrowRight' })
    expect(onSelect).toHaveBeenLastCalledWith('light')

    fireEvent.keyDown(group, { key: 'ArrowLeft' })
    expect(onSelect).toHaveBeenLastCalledWith(null)
  })

  it('jumps to the ends with Home and End', () => {
    const onSelect = renderGroup('dark')
    const group = screen.getByRole('radiogroup', { name: 'Theme' })

    fireEvent.keyDown(group, { key: 'Home' })
    expect(onSelect).toHaveBeenLastCalledWith(null)

    fireEvent.keyDown(group, { key: 'End' })
    expect(onSelect).toHaveBeenLastCalledWith('light')
  })

  it('ignores keys it does not own', () => {
    const onSelect = renderGroup('dark')
    fireEvent.keyDown(screen.getByRole('radiogroup', { name: 'Theme' }), { key: 'a' })
    expect(onSelect).not.toHaveBeenCalled()
  })

  it('wraps from the last option forward', () => {
    const onSelect = renderGroup('light')
    fireEvent.keyDown(screen.getByRole('radiogroup', { name: 'Theme' }), { key: 'ArrowRight' })
    expect(onSelect).toHaveBeenLastCalledWith(null)
  })
})

/**
 * A stored browser preference can hold a value that matches no option: the
 * value is read back through an unchecked cast, so a renamed or retired
 * enum value survives in storage. Anchoring the tab stop on the selection
 * left EVERY pill at -1 there, Tab skipped the whole radiogroup, and the
 * arrow keys the group handles reached nothing.
 */
describe('pillGroup with nothing selected', () => {
  const options = [
    { value: 'send', label: 'Send' },
    { value: 'newline', label: 'Newline' },
    { value: 'smart', label: 'Smart' },
  ]

  function renderUnmatched(onSelect = vi.fn()) {
    render(() => (
      <PillGroup
        label="Enter key"
        options={options}
        selected={v => v === 'gone-from-the-schema'}
        onSelect={onSelect}
      />
    ))
    return onSelect
  }

  it('keeps one pill in the tab order', () => {
    renderUnmatched()
    const tabIndexes = screen.getAllByRole('radio').map(el => el.getAttribute('tabindex'))
    expect(tabIndexes).toEqual(['0', '-1', '-1'])
  })

  it('checks none of them', () => {
    // APG allows an UNCHECKED radio to hold the tab stop; it does not allow
    // the group to claim a selection the caller does not have.
    renderUnmatched()
    expect(screen.getAllByRole('radio').map(el => el.getAttribute('aria-checked')))
      .toEqual(['false', 'false', 'false'])
  })

  it('moves to the SECOND option on ArrowRight, and reaches the first with Home', () => {
    // Focus sits on the pill that carries the tab stop, which is the first
    // one, so the arrow moves relative to it. Deriving the origin from the
    // (absent) selection made ArrowRight land on the first option instead,
    // which is where focus already was.
    const onSelect = renderUnmatched()
    const group = screen.getByRole('radiogroup', { name: 'Enter key' })

    fireEvent.keyDown(group, { key: 'ArrowRight' })
    expect(onSelect).toHaveBeenLastCalledWith('newline')

    fireEvent.keyDown(group, { key: 'Home' })
    expect(onSelect).toHaveBeenLastCalledWith('send')
  })
})

/**
 * A group another control governs: the theme chooser's mode pills while the
 * palette is "Match UI".
 *
 * It must keep SHOWING its selection -- that is what tells the user which mode
 * the governing control produced -- while refusing every way of changing it.
 */
describe('pillGroup disabled', () => {
  const options = [
    { value: 'system', label: 'System' },
    { value: 'light', label: 'Light' },
    { value: 'dark', label: 'Dark' },
  ]

  function renderDisabled(current: string, onSelect = vi.fn()) {
    render(() => (
      <PillGroup
        label="Terminal theme mode"
        options={options}
        disabled
        selected={v => v === current}
        onSelect={onSelect}
      />
    ))
    return onSelect
  }

  it('still reports which option is selected', () => {
    renderDisabled('dark')
    expect(screen.getByRole('radio', { name: 'Dark' })).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByRole('radio', { name: 'System' })).toHaveAttribute('aria-checked', 'false')
  })

  it('takes the whole group out of the tab order', () => {
    // The roving index normally parks a tab stop on the selected pill. Leaving
    // it there would put Tab on a control that refuses every key.
    renderDisabled('dark')
    const radios = screen.getAllByRole('radio')
    expect(radios.every(r => r.getAttribute('tabindex') === '-1')).toBe(true)
    for (const radio of radios)
      expect(radio).toBeDisabled()
  })

  it('refuses a click', () => {
    const onSelect = renderDisabled('dark')
    fireEvent.click(screen.getByRole('radio', { name: 'Light' }))
    expect(onSelect).not.toHaveBeenCalled()
  })

  it('refuses the arrow keys, which the GROUP handles rather than the pills', () => {
    // The native `disabled` attribute stops the click, but the keydown listener
    // sits on the radiogroup wrapper and would still fire.
    const onSelect = renderDisabled('dark')
    const group = screen.getByRole('radiogroup', { name: 'Terminal theme mode' })
    for (const key of ['ArrowRight', 'ArrowLeft', 'Home', 'End'])
      fireEvent.keyDown(group, { key })
    expect(onSelect).not.toHaveBeenCalled()
  })
})
