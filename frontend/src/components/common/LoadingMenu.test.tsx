import { cleanup, fireEvent, render, screen, within } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { menuOptions, menuTrigger, menuTriggerText, pickMenuValue } from '~/test-support/menu'
import { LoadingMenu } from './LoadingMenu'

afterEach(() => {
  cleanup()
})

const MENU = 'test-menu'

function renderMenu(overrides: Partial<Parameters<typeof LoadingMenu>[0]> = {}) {
  const onChange = vi.fn()
  const props = {
    'ariaLabel': 'Thing',
    'value': '',
    onChange,
    'isEmpty': false,
    'emptyLabel': 'No things',
    'options': [
      { value: 'a', label: 'Alpha' },
      { value: 'b', label: 'Beta' },
    ],
    'data-testid': MENU,
    ...overrides,
  }
  const result = render(() => <LoadingMenu {...props} />)
  return { onChange, unmount: result.unmount }
}

// The component that replaced `LoadingSelect`. Its predecessor needed an effect
// to re-apply `value` whenever the option children were swapped, because a
// `<select>` keeps its selection in `selectedIndex` -- browser state that the
// caller could not see. These cases pin that the replacement has no such state
// to fall out of step.
describe('the one-of-N menu (LoadingMenu)', () => {
  it('checks the option the value names, and only it', () => {
    renderMenu({ value: 'b' })
    const items = within(screen.getByTestId(MENU)).getAllByRole('menuitemradio', { hidden: true })
    expect(items.map(el => el.getAttribute('aria-checked'))).toEqual(['false', 'true'])
    expect(menuTriggerText(MENU)).toContain('Beta')
  })

  it('follows the value across an option-list swap, with no effect to do it', () => {
    // The bug `LoadingSelect`'s createEffect existed for: a list that arrives
    // AFTER the caller seeded `value` left a `<select>` showing option zero.
    const { unmount } = renderMenu({ value: 'b', options: [] })
    expect(menuTriggerText(MENU)).toContain('No things')
    unmount()

    renderMenu({ value: 'b' })
    expect(menuTriggerText(MENU)).toContain('Beta')
  })

  it('shows the loading label and refuses interaction while loading', () => {
    renderMenu({ loadingLabel: 'Loading things...' })
    expect(menuTriggerText(MENU)).toContain('Loading things...')
    expect(menuTrigger(MENU)).toBeDisabled()
  })

  // The empty state is DERIVED from the options, so a caller cannot declare a
  // list empty while handing over entries, nor hand over none and claim
  // otherwise. `BranchSelect` did the first: it injected its own prompt row.
  it('shows the empty label and refuses interaction with nothing to pick', () => {
    renderMenu({ options: [] })
    expect(menuTriggerText(MENU)).toContain('No things')
    expect(menuTrigger(MENU)).toBeDisabled()
  })

  it('combines the caller disable with its own', () => {
    // The correlation callers used to repeat; the component owns it so no
    // caller can forget the loading half.
    renderMenu({ disabled: true })
    expect(menuTrigger(MENU)).toBeDisabled()
  })

  it('shows the placeholder when no choice has been made yet', () => {
    // A menu has no empty `<option>` to hold a prompt, so it lives on the
    // trigger. Without this a fresh dialog reads as though nothing exists.
    renderMenu({ value: '', placeholder: 'Pick a thing...' })
    expect(menuTriggerText(MENU)).toContain('Pick a thing...')
  })

  it('falls back to the empty label when the value is empty and no placeholder is given', () => {
    renderMenu({ value: '' })
    expect(menuTriggerText(MENU)).toContain('No things')
  })

  it('shows the raw value when it matches no option, never the empty label', () => {
    // A value outside the list is its own state and is NOT an empty list. The
    // empty label states the opposite of what the user can see -- "No things"
    // above a populated menu -- and the raw value is what they need in order to
    // repair the setting. This is reachable whenever a stored choice outlives
    // its option: a worker that goes offline, a shell that is uninstalled, an
    // enum whose schema dropped a member.
    renderMenu({ value: 'gone' })
    expect(menuTriggerText(MENU)).toContain('gone')
    expect(menuTriggerText(MENU)).not.toContain('No things')
  })

  // The predecessor gated its children behind a `<Switch>`, so a stale list was
  // never mounted while loading. Two regression tests covered it and both went
  // with the deleted component.
  //
  // This matters because the guard is not the trigger's `disabled`: a fetcher
  // keeps the previous entries until its next success, `DropdownMenu` renders
  // children eagerly, and the item rows take no `disabled` of their own -- so a
  // refresh fired while the popover is already OPEN (F5 and $mod+r carry no
  // `when` clause) left the user picking a branch from the repository that was
  // replaced. Disabling the trigger cannot help once the popover is showing.
  //
  // `menuOptions` queries with `hidden: true`, so it would see a stale item.
  it('does NOT render options while loading, even when the list is non-empty', () => {
    renderMenu({ loadingLabel: 'Loading things...' })
    expect(menuOptions(MENU)).toEqual([])
    expect(menuTriggerText(MENU)).toContain('Loading things...')
  })

  it('does NOT render options while the list is empty', () => {
    renderMenu({ options: [] })
    expect(menuOptions(MENU)).toEqual([])
  })

  // A filter that matches nothing is not an empty menu. Disabling the trigger
  // under the user's own query would trap them with no way to clear it.
  it('stays enabled when the FILTER empties the list, not the options', () => {
    renderMenu({ filter: true, value: 'b' })
    fireEvent.input(screen.getByTestId('loading-menu-filter'), { target: { value: 'no-such-option' } })
    expect(menuOptions(MENU)).toEqual([])
    expect(menuTrigger(MENU)).not.toBeDisabled()
    expect(menuTriggerText(MENU)).toContain('Beta')
  })

  it('renders the options again once loading finishes', () => {
    // The teardown must be temporary: the list returns when the fetch lands.
    renderMenu({})
    expect(menuOptions(MENU)).toEqual(['Alpha', 'Beta'])
  })

  // Presence of the label IS the loading state, so the two mistakes the old
  // `loading` + `loadingLabel` pair allowed -- a flag with no label (an empty
  // trigger) and a label behind a literal `false` flag (dead text) -- are no
  // longer writable.
  it('is loading exactly while a loading label is given', () => {
    const { unmount } = renderMenu({ loadingLabel: 'Fetching...' })
    expect(menuTriggerText(MENU)).toContain('Fetching...')
    expect(menuTrigger(MENU)).toBeDisabled()
    expect(menuOptions(MENU)).toEqual([])
    unmount()

    renderMenu({})
    expect(menuOptions(MENU)).toEqual(['Alpha', 'Beta'])
    expect(menuTrigger(MENU)).not.toBeDisabled()
  })

  it('commits the picked value', () => {
    const { onChange } = renderMenu({ value: 'a' })
    pickMenuValue(MENU, 'b')
    expect(onChange).toHaveBeenCalledWith('b')
  })

  it('heads a group whenever it changes, and only then', () => {
    // `<optgroup label>` became a heading drawn on the boundary, so a run of
    // entries in one group is headed once rather than per item.
    renderMenu({
      options: [
        { value: 'a', label: 'Alpha', group: 'Local' },
        { value: 'b', label: 'Beta', group: 'Local' },
        { value: 'c', label: 'Gamma', group: 'Remote' },
      ],
    })
    const popover = screen.getByTestId(MENU)
    expect(within(popover).getAllByText('Local')).toHaveLength(1)
    expect(within(popover).getAllByText('Remote')).toHaveLength(1)
    expect(menuOptions(MENU)).toEqual(['Alpha', 'Beta', 'Gamma'])
  })

  describe('filter', () => {
    const many = [
      { value: 'a', label: 'Alpha' },
      { value: 'b', label: 'Beta' },
      { value: 'ab', label: 'Alphabet' },
    ]

    it('is absent unless asked for', () => {
      // A bounded list gets no filter: five entries do not need one, and an
      // unasked-for text box in a menu is noise.
      renderMenu({ options: many })
      expect(screen.queryByTestId('loading-menu-filter')).toBeNull()
    })

    it('narrows the list, matching without regard to case', () => {
      renderMenu({ options: many, filter: true })
      fireEvent.input(screen.getByTestId('loading-menu-filter'), { target: { value: 'ALPHA' } })
      expect(menuOptions(MENU)).toEqual(['Alpha', 'Alphabet'])
    })

    it('says so rather than showing an empty menu', () => {
      renderMenu({ options: many, filter: true })
      fireEvent.input(screen.getByTestId('loading-menu-filter'), { target: { value: 'zzz' } })
      expect(menuOptions(MENU)).toEqual([])
      expect(screen.getByText('No matches')).toBeInTheDocument()
    })

    it('commits a value picked out of a filtered list', () => {
      const { onChange } = renderMenu({ options: many, filter: true })
      fireEvent.input(screen.getByTestId('loading-menu-filter'), { target: { value: 'bet' } })
      pickMenuValue(MENU, 'ab')
      expect(onChange).toHaveBeenCalledWith('ab')
    })
  })

  it('warms its caller through onOpen, the seam focus used to be', () => {
    // `WorkerSelector` prefetches the metadata for the workers it is about to
    // list. The `<select>` hung that on `focus`; a menu has only the open.
    const onOpen = vi.fn()
    renderMenu({ onOpen })
    expect(onOpen).not.toHaveBeenCalled()
    fireEvent.click(menuTrigger(MENU))
    expect(onOpen).toHaveBeenCalledTimes(1)
  })
})

describe('loadingMenu keyboard contract', () => {
  // What retiring the native `<select>` took away. A `<select>` gave arrow
  // keys, Home/End and type-ahead for free; a keyboard user was left Tabbing
  // through every option, which on a twelve-shell machine is twelve presses to
  // reach the last one.
  const LETTERS = [
    { value: 'a', label: 'Alpha' },
    { value: 'b', label: 'Beta' },
    { value: 'g', label: 'Gamma' },
    { value: 'd', label: 'Delta' },
  ]

  function openMenu(overrides: Partial<Parameters<typeof LoadingMenu>[0]> = {}) {
    const r = renderMenu({ options: LETTERS, ...overrides })
    const popover = screen.getByTestId(MENU)
    // jsdom has no popover engine, so drive the state the component reads.
    popover.setAttribute('data-open', 'true')
    Object.defineProperty(popover, 'matches', {
      configurable: true,
      value: (sel: string) => sel === ':popover-open',
    })
    return { ...r, popover }
  }

  const items = () =>
    [...screen.getByTestId(MENU).querySelectorAll<HTMLElement>('[role="menuitemradio"]')]

  const focusedLabel = () => (document.activeElement as HTMLElement | null)?.textContent?.trim()

  it('moves down and up with the arrow keys, and wraps at both ends', () => {
    const { popover } = openMenu()
    fireEvent.keyDown(popover, { key: 'ArrowDown' })
    expect(focusedLabel()).toBe('Alpha')
    fireEvent.keyDown(popover, { key: 'ArrowDown' })
    expect(focusedLabel()).toBe('Beta')
    fireEvent.keyDown(popover, { key: 'ArrowUp' })
    expect(focusedLabel()).toBe('Alpha')
    // Wrapping is what the native control does, and it is what puts the last
    // option one press away from the first.
    fireEvent.keyDown(popover, { key: 'ArrowUp' })
    expect(focusedLabel()).toBe('Delta')
  })

  it('jumps to the ends with Home and End', () => {
    const { popover } = openMenu()
    fireEvent.keyDown(popover, { key: 'End' })
    expect(focusedLabel()).toBe('Delta')
    fireEvent.keyDown(popover, { key: 'Home' })
    expect(focusedLabel()).toBe('Alpha')
  })

  it('jumps to an option by its first letter, without focusing the list first', () => {
    // The requirement type-ahead exists for: the user opens the menu and types.
    // No Tab, no arrow key, no click into the list.
    const { popover } = openMenu()
    fireEvent.keyDown(popover, { key: 'g' })
    expect(focusedLabel()).toBe('Gamma')
  })

  it('starts a new search once the buffer goes quiet', () => {
    // Two letters typed QUICKLY are one prefix -- `g` then `d` searches "gd"
    // and matches nothing, which is what a `<select>` does too. It is only
    // after the pause that `d` means "the next thing starting with d".
    vi.useFakeTimers()
    try {
      const { popover } = openMenu()
      fireEvent.keyDown(popover, { key: 'g' })
      expect(focusedLabel()).toBe('Gamma')

      // Still inside the window: the buffer is "gd", which matches nothing, so
      // focus does not move to a wrong answer.
      fireEvent.keyDown(popover, { key: 'd' })
      expect(focusedLabel()).toBe('Gamma')

      vi.advanceTimersByTime(1000)
      fireEvent.keyDown(popover, { key: 'd' })
      expect(focusedLabel()).toBe('Delta')
    }
    finally {
      vi.useRealTimers()
    }
  })

  it('matches a multi-character prefix, so `be` finds Beta over Bravo', () => {
    const { popover } = openMenu({
      options: [
        { value: 'br', label: 'Bravo' },
        { value: 'be', label: 'Beta' },
      ],
    })
    fireEvent.keyDown(popover, { key: 'b' })
    expect(focusedLabel()).toBe('Bravo')
    fireEvent.keyDown(popover, { key: 'e' })
    expect(focusedLabel()).toBe('Beta')
  })

  it('cycles through the options sharing a letter when that letter repeats', () => {
    const { popover } = openMenu({
      options: [
        { value: 'b1', label: 'Bravo' },
        { value: 'b2', label: 'Beta' },
      ],
    })
    fireEvent.keyDown(popover, { key: 'b' })
    expect(focusedLabel()).toBe('Bravo')
    fireEvent.keyDown(popover, { key: 'b' })
    expect(focusedLabel()).toBe('Beta')
    fireEvent.keyDown(popover, { key: 'b' })
    expect(focusedLabel()).toBe('Bravo')
  })

  it('skips a disabled option rather than stranding focus on it', () => {
    const { popover } = openMenu()
    items()[1]!.setAttribute('disabled', '')
    fireEvent.keyDown(popover, { key: 'ArrowDown' })
    expect(focusedLabel()).toBe('Alpha')
    fireEvent.keyDown(popover, { key: 'ArrowDown' })
    expect(focusedLabel()).toBe('Gamma')
  })

  it('leaves a typed character to the filter box when one has focus', () => {
    // The filter IS the type-ahead for a long menu, so the key must reach the
    // input rather than moving focus off it.
    const many = Array.from({ length: 15 }, (_, i) => ({ value: `v${i}`, label: `Option ${i}` }))
    const { popover } = openMenu({ options: many })
    const input = screen.getByTestId('loading-menu-filter') as HTMLInputElement
    input.focus()
    fireEvent.keyDown(popover, { key: 'o' })
    expect(document.activeElement).toBe(input)
  })
})

describe('loadingMenu filter threshold', () => {
  it('grows a filter box once the list is long enough to be work to scan', () => {
    const many = Array.from({ length: 12 }, (_, i) => ({ value: `v${i}`, label: `Option ${i}` }))
    renderMenu({ options: many })
    expect(screen.queryByTestId('loading-menu-filter')).not.toBeNull()
  })

  it('shows none for a list a reader takes in at a glance', () => {
    renderMenu({ options: [{ value: 'a', label: 'Alpha' }] })
    expect(screen.queryByTestId('loading-menu-filter')).toBeNull()
  })

  it('lets the caller override in both directions', () => {
    // `BranchSelect` states it although its repository may hold three branches
    // today; a caller that knows its list stays short can refuse it.
    const { unmount } = renderMenu({ options: [{ value: 'a', label: 'Alpha' }], filter: true })
    expect(screen.queryByTestId('loading-menu-filter')).not.toBeNull()
    unmount()

    const many = Array.from({ length: 30 }, (_, i) => ({ value: `v${i}`, label: `Option ${i}` }))
    renderMenu({ options: many, filter: false })
    expect(screen.queryByTestId('loading-menu-filter')).toBeNull()
  })
})
