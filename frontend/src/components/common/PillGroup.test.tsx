import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { installControllableResizeObserver, triggerResizeObserversSync } from '~/test-support/resizeObserverStub'
import { PillGroup } from './PillGroup'
import * as styles from './PillGroup.css'

/**
 * These controls contain one required choice. Radio semantics give the group
 * its accessible name, selected state, and position information.
 *
 * The old controls used identical stateless buttons. `aria-pressed` would
 * describe toggle buttons, which promise that the reader can clear a choice.
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
        selected={value => value === current}
        onSelect={onSelect}
      />
    ))
    return onSelect
  }

  it('exposes a group with an accessible name', () => {
    renderGroup('dark')
    // A visible heading does not associate itself with this group. The label
    // prop supplies the name that assistive technology announces.
    expect(screen.getByRole('radiogroup', { name: 'Theme' })).toBeTruthy()
    expect(screen.getAllByRole('radio')).toHaveLength(3)
  })

  it('selects without submitting its form', () => {
    // A button in a form submits by default. The old Passkey option started an
    // incomplete login request and left the submit button unavailable.
    const onSelect = vi.fn()
    const onSubmit = vi.fn((event: Event) => event.preventDefault())
    render(() => (
      <form onSubmit={onSubmit}>
        <PillGroup
          label="Theme"
          options={options}
          selected={value => value === null}
          onSelect={onSelect}
        />
        <button type="submit">Go</button>
      </form>
    ))

    fireEvent.click(screen.getByRole('radio', { name: 'Dark' }))

    expect(onSelect).toHaveBeenCalledWith('dark')
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('marks exactly one option as checked', () => {
    renderGroup('dark')
    expect(screen.getByRole('radio', { name: 'Dark' })).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByRole('radio', { name: 'Light' })).toHaveAttribute('aria-checked', 'false')
    expect(screen.getByRole('radio', { name: 'Use account default' })).toHaveAttribute('aria-checked', 'false')
  })

  it('puts only the checked option in the tab order', () => {
    // Tab enters the group once. Arrow keys then move within the group.
    renderGroup('dark')
    expect(screen.getByRole('radio', { name: 'Dark' })).toHaveAttribute('tabindex', '0')
    expect(screen.getByRole('radio', { name: 'Light' })).toHaveAttribute('tabindex', '-1')
  })

  it('moves the selection with arrow keys and wraps at each end', () => {
    const onSelect = renderGroup('dark')
    const group = screen.getByRole('radiogroup', { name: 'Theme' })

    fireEvent.keyDown(group, { key: 'ArrowRight' })
    expect(onSelect).toHaveBeenLastCalledWith('light')

    fireEvent.keyDown(group, { key: 'ArrowLeft' })
    expect(onSelect).toHaveBeenLastCalledWith(null)
  })

  it('moves to each end with Home and End', () => {
    const onSelect = renderGroup('dark')
    const group = screen.getByRole('radiogroup', { name: 'Theme' })

    fireEvent.keyDown(group, { key: 'Home' })
    expect(onSelect).toHaveBeenLastCalledWith(null)

    fireEvent.keyDown(group, { key: 'End' })
    expect(onSelect).toHaveBeenLastCalledWith('light')
  })

  it('ignores keys that the group does not own', () => {
    const onSelect = renderGroup('dark')
    fireEvent.keyDown(screen.getByRole('radiogroup', { name: 'Theme' }), { key: 'a' })
    expect(onSelect).not.toHaveBeenCalled()
  })

  it('wraps forward from the last option', () => {
    const onSelect = renderGroup('light')
    fireEvent.keyDown(screen.getByRole('radiogroup', { name: 'Theme' }), { key: 'ArrowRight' })
    expect(onSelect).toHaveBeenLastCalledWith(null)
  })

  it('does not draw an empty group', () => {
    const result = render(() => (
      <PillGroup
        label="Empty choice"
        options={[]}
        selected={() => false}
        onSelect={vi.fn()}
      />
    ))
    expect(screen.queryByRole('radiogroup', { name: 'Empty choice' })).toBeNull()
    expect(result.container.childElementCount).toBe(0)
  })
})

/**
 * A stale stored value can match no current option.
 *
 * Browser storage uses an unchecked cast. A removed enum value can therefore
 * survive a schema change. The group must remain reachable in this state.
 */
describe('pillGroup with no selected option', () => {
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
        selected={value => value === 'gone-from-the-schema'}
        onSelect={onSelect}
      />
    ))
    return onSelect
  }

  it('keeps one option in the tab order', () => {
    renderUnmatched()
    const tabIndexes = screen.getAllByRole('radio').map(element => element.getAttribute('tabindex'))
    expect(tabIndexes).toEqual(['0', '-1', '-1'])
  })

  it('does not check an option', () => {
    // The radio pattern permits an unchecked radio to own the tab stop. The
    // group must not claim a selection that the caller does not have.
    renderUnmatched()
    expect(screen.getAllByRole('radio').map(element => element.getAttribute('aria-checked')))
      .toEqual(['false', 'false', 'false'])
  })

  it('moves from the roving option when an arrow key selects', () => {
    // Focus starts on the first option. ArrowRight must therefore select the
    // second option instead of restarting at the first option.
    const onSelect = renderUnmatched()
    const group = screen.getByRole('radiogroup', { name: 'Enter key' })

    fireEvent.keyDown(group, { key: 'ArrowRight' })
    expect(onSelect).toHaveBeenLastCalledWith('newline')

    fireEvent.keyDown(group, { key: 'Home' })
    expect(onSelect).toHaveBeenLastCalledWith('send')
  })
})

/**
 * Another control governs this group and prevents all changes.
 *
 * The terminal theme uses this state while it matches the user interface. Its
 * visible selection shows the mode that the governing control produced.
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
        selected={value => value === current}
        onSelect={onSelect}
      />
    ))
    return onSelect
  }

  it('reports its selected option', () => {
    renderDisabled('dark')
    expect(screen.getByRole('radio', { name: 'Dark' })).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByRole('radio', { name: 'System' })).toHaveAttribute('aria-checked', 'false')
  })

  it('removes the group from the tab order', () => {
    // A disabled group cannot leave a tab stop on a control that refuses every
    // keyboard input.
    renderDisabled('dark')
    const radios = screen.getAllByRole('radio')
    expect(radios.every(radio => radio.getAttribute('tabindex') === '-1')).toBe(true)
    for (const radio of radios)
      expect(radio).toBeDisabled()
  })

  it('refuses a click', () => {
    const onSelect = renderDisabled('dark')
    fireEvent.click(screen.getByRole('radio', { name: 'Light' }))
    expect(onSelect).not.toHaveBeenCalled()
  })

  it('refuses the keys that the group handles', () => {
    // The native disabled attribute stops a button click. The group still
    // receives its own keyboard events, so it must refuse them separately.
    const onSelect = renderDisabled('dark')
    const group = screen.getByRole('radiogroup', { name: 'Terminal theme mode' })
    for (const key of ['ArrowRight', 'ArrowLeft', 'Home', 'End'])
      fireEvent.keyDown(group, { key })
    expect(onSelect).not.toHaveBeenCalled()
  })

  it('dims the full group without dimming each option twice', () => {
    renderDisabled('dark')
    const group = screen.getByRole('radiogroup', { name: 'Terminal theme mode' })
    expect(group).toHaveClass(styles.pillGroupDisabled)
    for (const radio of screen.getAllByRole('radio'))
      expect(radio).not.toHaveClass(styles.pillOptionDimmed)
  })
})

/**
 * One option can refuse input without disabling the full group.
 *
 * A passkey option on an insecure page is restorable. Keep it visible so the
 * reader can learn why it is unavailable.
 */
describe('pillGroup with one refused option', () => {
  const reason = 'A browser runs a passkey only on a secure page.'

  function renderGroup(current: string, onSelect = vi.fn()) {
    render(() => (
      <PillGroup
        label="Sign-in method"
        options={[
          { value: 'password', label: 'Password' },
          { value: 'passkey', label: 'Passkey', disabledReason: reason },
        ]}
        selected={value => value === current}
        onSelect={onSelect}
      />
    ))
    return onSelect
  }

  it('keeps the option, its accessible name, and its reason', () => {
    renderGroup('password')
    // The reason describes the button. It must not replace the button name.
    const passkey = screen.getByRole('radio', { name: 'Passkey' })
    expect(passkey).toBeDisabled()
    expect(passkey).not.toHaveAttribute('title')
    const describedBy = passkey.getAttribute('aria-describedby')
    expect(describedBy).toBeTruthy()
    expect(document.getElementById(describedBy!)?.textContent).toBe(reason)
  })

  it('keeps the other options available', () => {
    const onSelect = renderGroup('passkey')
    const password = screen.getByRole('radio', { name: 'Password' })
    expect(password).toBeEnabled()
    fireEvent.click(password)
    expect(onSelect).toHaveBeenCalledWith('password')
  })

  it('skips the refused option with arrow keys', () => {
    // A refused option has no tab stop. Moving focus to it would leave the
    // group without a valid keyboard origin.
    const onSelect = renderGroup('password')
    fireEvent.keyDown(screen.getByRole('radiogroup', { name: 'Sign-in method' }), { key: 'ArrowRight' })
    expect(onSelect).not.toHaveBeenCalledWith('passkey')
  })

  it('moves the tab stop from a refused selection', () => {
    renderGroup('passkey')
    expect(screen.getByRole('radio', { name: 'Password' })).toHaveAttribute('tabindex', '0')
    expect(screen.getByRole('radio', { name: 'Passkey' })).toHaveAttribute('tabindex', '-1')
    // The stored value still selects Passkey. The radio pattern permits another
    // unchecked radio to own the tab stop.
    expect(screen.getByRole('radio', { name: 'Passkey' })).toHaveAttribute('aria-checked', 'true')
    expect(getIndicator(screen.getByRole('radiogroup', { name: 'Sign-in method' })))
      .toHaveClass(styles.selectionIndicatorDimmed)
  })
})

interface StubGeometry {
  left: number
  width: number
}

function stubGeometry(element: HTMLElement, geometry: StubGeometry): void {
  Object.defineProperties(element, {
    offsetLeft: { configurable: true, get: () => geometry.left },
    offsetWidth: { configurable: true, get: () => geometry.width },
  })
}

function getIndicator(group: HTMLElement): HTMLElement | null {
  return group.querySelector(':scope > [aria-hidden="true"]')
}

/** The moving fill follows reactive selection and layout changes. */
describe('pillGroup selection indicator', () => {
  const first = { value: 'first', label: 'First' }
  const second = { value: 'second', label: 'A wider second option' }

  beforeEach(() => installControllableResizeObserver())

  it('mounts and removes the control when its option list changes', () => {
    const [options, setOptions] = createSignal<(typeof first)[]>([])
    render(() => (
      <PillGroup
        label="Loaded options"
        options={options()}
        selected={value => value === first.value}
        onSelect={vi.fn()}
      />
    ))
    expect(screen.queryByRole('radiogroup', { name: 'Loaded options' })).toBeNull()

    setOptions([first])
    const group = screen.getByRole('radiogroup', { name: 'Loaded options' })
    stubGeometry(screen.getByRole('radio', { name: first.label }), { left: 0, width: 70 })
    triggerResizeObserversSync()
    expect(getIndicator(group)).toHaveStyle({ transform: 'translateX(0px)', width: '70px' })

    setOptions([])
    expect(screen.queryByRole('radiogroup', { name: 'Loaded options' })).toBeNull()
  })

  it('starts at the selected segment and moves to a segment with another width', () => {
    const [current, setCurrent] = createSignal(first.value)
    render(() => (
      <PillGroup
        label="Measured options"
        options={[first, second]}
        selected={value => value === current()}
        onSelect={setCurrent}
      />
    ))

    const group = screen.getByRole('radiogroup', { name: 'Measured options' })
    const firstRadio = screen.getByRole('radio', { name: first.label })
    const secondRadio = screen.getByRole('radio', { name: second.label })
    stubGeometry(firstRadio, { left: 0, width: 70 })
    stubGeometry(secondRadio, { left: 70, width: 150 })
    triggerResizeObserversSync()

    const indicator = getIndicator(group)
    expect(indicator).toHaveAttribute('aria-hidden', 'true')
    expect(indicator).toHaveStyle({ transform: 'translateX(0px)', width: '70px' })
    expect(indicator).not.toHaveClass(styles.selectionIndicatorMoves)

    fireEvent.click(secondRadio)

    expect(getIndicator(group)).toHaveStyle({ transform: 'translateX(70px)', width: '150px' })
    expect(getIndicator(group)).toHaveClass(styles.selectionIndicatorMoves)
  })

  it('adds and removes the indicator without entrance motion', () => {
    const [current, setCurrent] = createSignal('missing')
    render(() => (
      <PillGroup
        label="Optional selection"
        options={[first, second]}
        selected={value => value === current()}
        onSelect={setCurrent}
      />
    ))

    const group = screen.getByRole('radiogroup', { name: 'Optional selection' })
    const firstRadio = screen.getByRole('radio', { name: first.label })
    stubGeometry(firstRadio, { left: 0, width: 70 })
    expect(getIndicator(group)).toBeNull()

    setCurrent(first.value)

    expect(getIndicator(group)).toHaveStyle({ transform: 'translateX(0px)', width: '70px' })
    expect(getIndicator(group)).not.toHaveClass(styles.selectionIndicatorMoves)

    setCurrent('missing')
    expect(getIndicator(group)).toBeNull()
  })

  it('corrects resized geometry without selection motion', () => {
    const geometry = { left: 70, width: 150 }
    render(() => (
      <PillGroup
        label="Resized options"
        options={[first, second]}
        selected={value => value === second.value}
        onSelect={vi.fn()}
      />
    ))

    const group = screen.getByRole('radiogroup', { name: 'Resized options' })
    stubGeometry(screen.getByRole('radio', { name: first.label }), { left: 0, width: 70 })
    stubGeometry(screen.getByRole('radio', { name: second.label }), geometry)
    triggerResizeObserversSync()
    expect(getIndicator(group)).toHaveStyle({ transform: 'translateX(70px)', width: '150px' })

    geometry.left = 90
    geometry.width = 180
    triggerResizeObserversSync()

    expect(getIndicator(group)).toHaveStyle({ transform: 'translateX(90px)', width: '180px' })
    expect(getIndicator(group)).not.toHaveClass(styles.selectionIndicatorMoves)
  })

  it('corrects a reorder without treating it as a selection change', () => {
    const [options, setOptions] = createSignal([first, second])
    const firstGeometry = { left: 0, width: 70 }
    const secondGeometry = { left: 70, width: 150 }
    render(() => (
      <PillGroup
        label="Reordered options"
        options={options()}
        selected={value => value === second.value}
        onSelect={vi.fn()}
      />
    ))

    const group = screen.getByRole('radiogroup', { name: 'Reordered options' })
    stubGeometry(screen.getByRole('radio', { name: first.label }), firstGeometry)
    stubGeometry(screen.getByRole('radio', { name: second.label }), secondGeometry)
    triggerResizeObserversSync()
    expect(getIndicator(group)).toHaveStyle({ transform: 'translateX(70px)', width: '150px' })

    firstGeometry.left = 150
    secondGeometry.left = 0
    setOptions([second, first])

    expect(getIndicator(group)).toHaveStyle({ transform: 'translateX(0px)', width: '150px' })
    expect(getIndicator(group)).not.toHaveClass(styles.selectionIndicatorMoves)
  })
})
