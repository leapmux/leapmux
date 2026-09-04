import type { PillOptions } from './PillGroup'
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
describe('pill group (PillGroup)', () => {
  const options = [
    { key: null, label: 'Use account default' },
    { key: 'dark', label: 'Dark' },
    { key: 'light', label: 'Light' },
  ] as const

  function renderGroup(current: string | null, onSelect = vi.fn()) {
    render(() => (
      <PillGroup
        label="Theme"
        options={options}
        selectedKey={current}
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
          selectedKey={null}
          onSelect={onSelect}
        />
        <button type="submit">Go</button>
      </form>
    ))

    fireEvent.click(screen.getByRole('radio', { name: 'Dark' }))

    expect(onSelect).toHaveBeenCalledWith('dark')
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('does not select the current option again', () => {
    const onSelect = renderGroup('dark')

    fireEvent.click(screen.getByRole('radio', { name: 'Dark' }))

    expect(onSelect).not.toHaveBeenCalled()
  })

  it('does not select the current option again with Home', () => {
    const onSelect = renderGroup(null)

    fireEvent.keyDown(screen.getByRole('radiogroup', { name: 'Theme' }), { key: 'Home' })

    expect(onSelect).not.toHaveBeenCalled()
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

  it('moves right and then returns from the focused option', () => {
    const onSelect = renderGroup('dark')
    const group = screen.getByRole('radiogroup', { name: 'Theme' })

    fireEvent.keyDown(group, { key: 'ArrowRight' })
    expect(onSelect).toHaveBeenLastCalledWith('light')

    fireEvent.keyDown(group, { key: 'ArrowLeft' })
    expect(document.activeElement).toBe(screen.getByRole('radio', { name: 'Dark' }))
    expect(onSelect).toHaveBeenCalledTimes(1)
  })

  it('continues from the focused option while selection remains pending', () => {
    const onSelect = renderGroup('dark')
    const group = screen.getByRole('radiogroup', { name: 'Theme' })

    screen.getByRole('radio', { name: 'Dark' }).focus()
    fireEvent.keyDown(group, { key: 'ArrowRight' })
    expect(document.activeElement).toBe(screen.getByRole('radio', { name: 'Light' }))

    fireEvent.keyDown(group, { key: 'ArrowLeft' })
    expect(document.activeElement).toBe(screen.getByRole('radio', { name: 'Dark' }))
    expect(onSelect).toHaveBeenCalledTimes(1)
  })

  it('leaves modified navigation keys to the browser', () => {
    const onSelect = renderGroup('dark')
    const group = screen.getByRole('radiogroup', { name: 'Theme' })
    const event = new KeyboardEvent('keydown', {
      key: 'ArrowLeft',
      altKey: true,
      bubbles: true,
      cancelable: true,
    })

    group.dispatchEvent(event)

    expect(event.defaultPrevented).toBe(false)
    expect(onSelect).not.toHaveBeenCalled()
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
})

/**
 * A stale stored value can match no current option.
 *
 * Browser storage uses an unchecked cast. A removed enum value can therefore
 * survive a schema change. The group must remain reachable in this state.
 */
describe('pill group with no selected option', () => {
  const options = [
    { key: 'send', label: 'Send' },
    { key: 'newline', label: 'Newline' },
    { key: 'smart', label: 'Smart' },
  ] as const

  function renderUnmatched(onSelect = vi.fn()) {
    render(() => (
      <PillGroup
        label="Enter key"
        options={options}
        selectedKey="gone-from-the-schema"
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

describe('pill group option identity', () => {
  it.each([
    ['no', []],
    ['five', [
      { key: 'one', label: 'One' },
      { key: 'two', label: 'Two' },
      { key: 'three', label: 'Three' },
      { key: 'four', label: 'Four' },
      { key: 'five', label: 'Five' },
    ]],
  ])('rejects %s options', (_description, options) => {
    expect(() => render(() => (
      <PillGroup
        label="Invalid count"
        options={options as unknown as PillOptions<string>}
        selectedKey="one"
        onSelect={vi.fn()}
      />
    ))).toThrow(/one through four options/i)
  })

  it('keeps the focused radio when fresh records carry the same values', () => {
    const [options, setOptions] = createSignal<PillOptions<string>>([
      { key: 'password', label: 'Password' },
      { key: 'passkey', label: 'Passkey' },
    ])
    render(() => (
      <PillGroup
        label="Sign-in method"
        options={options()}
        selectedKey="password"
        onSelect={vi.fn()}
      />
    ))
    const password = screen.getByRole('radio', { name: 'Password' })
    password.focus()

    setOptions([
      { key: 'password', label: 'Password' },
      { key: 'passkey', label: 'Passkey' },
    ])

    expect(screen.getByRole('radio', { name: 'Password' })).toBe(password)
    expect(document.activeElement).toBe(password)
  })

  it('moves focus when the focused option leaves the group', async () => {
    const password = { key: 'password', label: 'Password' }
    const passkey = { key: 'passkey', label: 'Passkey' }
    const [options, setOptions] = createSignal<PillOptions<string>>([password, passkey])
    render(() => (
      <PillGroup
        label="Sign-in method"
        options={options()}
        selectedKey="passkey"
        onSelect={vi.fn()}
      />
    ))
    screen.getByRole('radio', { name: 'Passkey' }).focus()

    setOptions([password])

    await vi.waitFor(() => {
      expect(document.activeElement).toBe(screen.getByRole('radio', { name: 'Password' }))
    })
  })

  it('rejects duplicate option keys', () => {
    expect(() => render(() => (
      <PillGroup
        label="Duplicate values"
        options={[
          { key: 'same', label: 'First' },
          { key: 'same', label: 'Second' },
        ]}
        selectedKey="same"
        onSelect={vi.fn()}
      />
    ))).toThrow(/unique option keys/i)
  })

  it('rejects an empty disabled reason', () => {
    expect(() => render(() => (
      <PillGroup
        label="Empty reason"
        options={[{ key: 'blocked', label: 'Blocked', disabledReason: '' }]}
        selectedKey="blocked"
        onSelect={vi.fn()}
      />
    ))).toThrow(/non-empty disabled reason/i)
  })

  it('selects undefined and NaN values with arrow keys', () => {
    const onUndefined = vi.fn()
    const { unmount } = render(() => (
      <PillGroup
        label="Undefined value"
        options={[
          { key: 'defined', label: 'Defined' },
          { key: undefined, label: 'Undefined' },
        ]}
        selectedKey="defined"
        onSelect={onUndefined}
      />
    ))
    fireEvent.keyDown(screen.getByRole('radiogroup', { name: 'Undefined value' }), { key: 'ArrowRight' })
    expect(onUndefined).toHaveBeenCalledWith(undefined)
    unmount()

    const onNaN = vi.fn()
    render(() => (
      <PillGroup
        label="NaN value"
        options={[
          { key: 0, label: 'Zero' },
          { key: Number.NaN, label: 'Not a number' },
        ]}
        selectedKey={0}
        onSelect={onNaN}
      />
    ))
    fireEvent.keyDown(screen.getByRole('radiogroup', { name: 'NaN value' }), { key: 'ArrowRight' })
    expect(onNaN).toHaveBeenCalledWith(Number.NaN)
  })
})

/**
 * Another control governs this group and prevents all changes.
 *
 * The terminal theme uses this state while it matches the user interface. Its
 * visible selection shows the mode that the governing control produced.
 */
describe('pill group disabled', () => {
  const options = [
    { key: 'system', label: 'System' },
    { key: 'light', label: 'Light' },
    { key: 'dark', label: 'Dark' },
  ] as const

  function renderDisabled(current: string, onSelect = vi.fn()) {
    render(() => (
      <PillGroup
        label="Terminal theme mode"
        options={options}
        disabled
        selectedKey={current}
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
describe('pill group with one refused option', () => {
  const reason = 'A browser runs a passkey only on a secure page.'

  function renderGroup(current: string, onSelect = vi.fn()) {
    render(() => (
      <PillGroup
        label="Sign-in method"
        options={[
          { key: 'password', label: 'Password' },
          { key: 'passkey', label: 'Passkey', disabledReason: reason },
        ]}
        selectedKey={current}
        onSelect={onSelect}
      />
    ))
    return onSelect
  }

  it('keeps the option, its accessible name, and its reason', () => {
    renderGroup('password')
    // The reason describes the button. It must not replace the button name.
    const passkey = screen.getByRole('radio', { name: 'Passkey' })
    expect(passkey).toHaveAttribute('aria-disabled', 'true')
    expect(passkey).not.toBeDisabled()
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

  it('focuses the refused option without selecting it', () => {
    // Keyboard focus lets a sighted user open the explanation. Activation
    // still leaves the selection unchanged.
    const onSelect = renderGroup('password')
    const password = screen.getByRole('radio', { name: 'Password' })
    const passkey = screen.getByRole('radio', { name: 'Passkey' })
    password.focus()

    fireEvent.keyDown(password, { key: 'ArrowRight' })

    expect(document.activeElement).toBe(passkey)
    expect(onSelect).not.toHaveBeenCalled()
    fireEvent.click(passkey)
    expect(onSelect).not.toHaveBeenCalled()
  })

  it('moves the tab stop from a refused selection', () => {
    renderGroup('passkey')
    expect(screen.getByRole('radio', { name: 'Password' })).toHaveAttribute('tabindex', '-1')
    expect(screen.getByRole('radio', { name: 'Passkey' })).toHaveAttribute('tabindex', '0')
    expect(screen.getByRole('radio', { name: 'Passkey' })).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByRole('radio', { name: 'Passkey' })).not.toHaveClass(styles.pillOptionDimmed)
  })

  it('keeps an all-refused group keyboard-reachable', () => {
    const onSelect = vi.fn()
    render(() => (
      <PillGroup
        label="Unavailable methods"
        options={[
          { key: 'first', label: 'First', disabledReason: 'First is unavailable.' },
          { key: 'second', label: 'Second', disabledReason: 'Second is unavailable.' },
        ]}
        selectedKey="first"
        onSelect={onSelect}
      />
    ))
    const first = screen.getByRole('radio', { name: 'First' })
    const second = screen.getByRole('radio', { name: 'Second' })
    expect(first).toHaveAttribute('tabindex', '0')

    first.focus()
    fireEvent.keyDown(first, { key: 'ArrowRight' })

    expect(document.activeElement).toBe(second)
    expect(onSelect).not.toHaveBeenCalled()
  })
})

function stubSelectionGeometry(
  group: HTMLElement,
  radio: HTMLElement,
  geometry: { groupWidth: number, left: number, width: number },
): void {
  Object.defineProperties(group, {
    clientWidth: { configurable: true, get: () => geometry.groupWidth },
  })
  Object.defineProperties(radio, {
    offsetLeft: { configurable: true, get: () => geometry.left },
    offsetWidth: { configurable: true, get: () => geometry.width },
  })
}

describe('pill group sliding selection', () => {
  const first = { key: 'first', label: 'First' }
  const second = { key: 'second', label: 'A wider second option' }

  beforeEach(() => installControllableResizeObserver())

  it('keeps the selected button as a fallback until geometry is valid', () => {
    render(() => (
      <PillGroup
        label="Measured options"
        options={[first, second]}
        selectedKey="first"
        onSelect={vi.fn()}
      />
    ))
    const group = screen.getByRole('radiogroup', { name: 'Measured options' })
    const firstRadio = screen.getByRole('radio', { name: first.label })
    expect(firstRadio).toHaveClass(styles.pillOptionActive)
    expect(group.querySelector('[data-pill-selection-fill]')).toBeNull()

    stubSelectionGeometry(group, firstRadio, { groupWidth: 220, left: 0, width: 70 })
    triggerResizeObserversSync()

    const fill = group.querySelector<HTMLElement>('[data-pill-selection-fill]')
    expect(fill).not.toBeNull()
    expect(fill!.style.getPropertyValue('--pill-selection-left')).toBe('0px')
    expect(fill!.style.getPropertyValue('--pill-selection-right')).toBe('150px')
    expect(firstRadio).toHaveClass(styles.pillOptionSelectedTarget)
    expect(firstRadio).not.toHaveClass(styles.pillOptionActive)
  })

  it('moves the paired fill and label layers when selection changes', () => {
    const [selected, setSelected] = createSignal(first.key)
    render(() => (
      <PillGroup
        label="Sliding options"
        options={[first, second]}
        selectedKey={selected()}
        onSelect={setSelected}
      />
    ))
    const group = screen.getByRole('radiogroup', { name: 'Sliding options' })
    const firstRadio = screen.getByRole('radio', { name: first.label })
    const secondRadio = screen.getByRole('radio', { name: second.label })
    stubSelectionGeometry(group, firstRadio, { groupWidth: 220, left: 0, width: 70 })
    stubSelectionGeometry(group, secondRadio, { groupWidth: 220, left: 70, width: 150 })
    triggerResizeObserversSync()

    fireEvent.click(secondRadio)

    const fill = group.querySelector<HTMLElement>('[data-pill-selection-fill]')
    const labels = group.querySelector<HTMLElement>('[data-pill-selection-labels]')
    expect(fill).toHaveClass(styles.selectionWindowMoves)
    expect(labels).toHaveClass(styles.selectionWindowMoves)
    expect(fill!.style.getPropertyValue('--pill-selection-left')).toBe('70px')
    expect(fill!.style.getPropertyValue('--pill-selection-right')).toBe('0px')
    expect([...labels!.querySelectorAll('[data-label]')].map(label => label.getAttribute('data-label')))
      .toEqual(['First', 'A wider second option'])
    expect(secondRadio).toHaveClass(styles.pillOptionSelectedTarget)
  })
})
