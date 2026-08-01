import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { PillGroupForTest } from './AppearanceSettings'

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
      <PillGroupForTest
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
