import { fireEvent, render, screen, within } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { FilterableListbox } from './FilterableListbox'

describe('filterableListbox', () => {
  // Guards the highlighted-index clamp: the worker re-emits a shorter catalog on an optimistic
  // model switch, shrinking props.items under the listbox. Without clamping, highlightedIndex
  // keeps pointing past the end and Enter selects nothing (the index resolves to undefined).
  it('clamps the highlighted index when props.items shrinks underneath it', async () => {
    // jsdom has no scrollIntoView; the ArrowDown handler calls it via requestAnimationFrame.
    HTMLElement.prototype.scrollIntoView = vi.fn()
    const big = Array.from({ length: 9 }, (_, i) => ({ label: `Model ${i}`, value: `m${i}` }))
    const [items, setItems] = createSignal(big)
    const onSelect = vi.fn()
    render(() => FilterableListbox({
      get items() { return items() },
      testIdPrefix: 'm',
      onSelect,
    }))

    const input = screen.getByTestId('m-filter')
    // Highlight starts at 0; ArrowDown 8 times moves it to index 8 (the last of 9).
    for (let i = 0; i < 8; i++)
      await fireEvent.keyDown(input, { key: 'ArrowDown' })

    // The list shrinks to 3 items; index 8 is now out of range.
    setItems(big.slice(0, 3))

    // Enter selects the clamped, in-range row (index 2 = 'm2'), not an undefined item.
    await fireEvent.keyDown(input, { key: 'Enter' })
    expect(onSelect).toHaveBeenCalledWith('m2')
  })

  it('wraps a row in the hover-tooltip span only when the row has tooltip text', () => {
    render(() => FilterableListbox({
      items: [
        { label: 'With', value: 'w', tooltip: 'has tip' },
        { label: 'Without', value: 'wo' },
      ],
      testIdPrefix: 'f',
      onSelect: () => {},
    }))

    // Tooltip wraps its child in a display:contents span. A tooltip-less row is
    // rendered directly into the listbox -- no idle Tooltip instance per row (the
    // language picker has 235 tooltip-less rows, so this is the bulk of its cost).
    const withTip = screen.getByTestId('f-w').parentElement!
    expect(withTip.tagName).toBe('SPAN')
    expect((withTip as HTMLElement).style.display).toBe('contents')
    expect(screen.getByTestId('f-wo').parentElement!.tagName).not.toBe('SPAN')
  })

  it('styles its own rows, so no caller can omit the classes', () => {
    // These were six pass-through class props. Every caller passed the same
    // five and every caller forgot the sixth, which is how the code-language
    // picker's ids lost their right-aligned muted styling.
    render(() => FilterableListbox({
      items: [
        { label: 'JavaScript', value: 'javascript', secondary: 'js' },
        { label: 'TypeScript', value: 'typescript', secondary: 'ts' },
      ],
      current: 'javascript',
      testIdPrefix: 'f',
      onSelect: () => {},
    }))

    const selected = screen.getByTestId('f-javascript')
    const unselected = screen.getByTestId('f-typescript')
    expect(selected.className).not.toBe('')
    // The selected row carries a marker the unselected one does not: a row with
    // secondary text shows no check icon, so weight is the only marker left.
    expect(selected.className).not.toBe(unselected.className)

    // The secondary text is styled, not a bare span.
    const secondary = within(selected).getByText('js')
    expect(secondary.tagName).toBe('SPAN')
    expect(secondary.className).not.toBe('')
  })

  it('uses a controlled filter when filter/setFilter props are provided', async () => {
    const [filter, setFilter] = createSignal('')
    render(() => FilterableListbox({
      items: [
        { label: 'Apple', value: 'apple' },
        { label: 'Banana', value: 'banana' },
      ],
      testIdPrefix: 'f',
      onSelect: () => {},
      filter,
      setFilter,
    }))

    // Typing routes through the caller's setter (so the caller can reset it across
    // reuse, e.g. the always-mounted code-language popover resets it on open).
    const input = screen.getByTestId('f-filter')
    await fireEvent.input(input, { target: { value: 'ban' } })
    // eslint-disable-next-line solid/reactivity -- an assertion reads the value once; there is nothing to re-run.
    expect(filter()).toBe('ban')

    // ...and the controlled value drives which rows render.
    setFilter('apple')
    expect(screen.queryByTestId('f-apple')).not.toBeNull()
    expect(screen.queryByTestId('f-banana')).toBeNull()
  })

  it('resets the keyboard highlight to the first row when resetKey changes', async () => {
    // The always-mounted code-language popover passes its open signal as resetKey: a
    // reopen must start at the top row, not a stale highlight left from a prior session
    // (which Enter would mis-select). Drive it behaviorally via ArrowDown + Enter.
    const [resetKey, setResetKey] = createSignal(0)
    const onSelect = vi.fn()
    render(() => FilterableListbox({
      items: [
        { label: 'Alpha', value: 'alpha' },
        { label: 'Bravo', value: 'bravo' },
        { label: 'Charlie', value: 'charlie' },
      ],
      testIdPrefix: 'f',
      onSelect,
      resetKey,
    }))

    const input = screen.getByTestId('f-filter')
    // Move the highlight down to the third row, then confirm Enter selects it.
    await fireEvent.keyDown(input, { key: 'ArrowDown' })
    await fireEvent.keyDown(input, { key: 'ArrowDown' })
    await fireEvent.keyDown(input, { key: 'Enter' })
    expect(onSelect).toHaveBeenLastCalledWith('charlie')

    // Changing resetKey (a reopen) snaps the highlight back to the first row.
    setResetKey(1)
    await fireEvent.keyDown(input, { key: 'Enter' })
    expect(onSelect).toHaveBeenLastCalledWith('alpha')
  })

  it('clears the filter text when resetKey changes', async () => {
    // A popover keeps its children mounted across a close, so without this the
    // narrowed list the user left behind is still applied on the next open --
    // and the option they came back for is missing.
    const [resetKey, setResetKey] = createSignal(0)
    render(() => FilterableListbox({
      items: [
        { label: 'Alpha', value: 'alpha' },
        { label: 'Bravo', value: 'bravo' },
      ],
      testIdPrefix: 'f',
      onSelect: vi.fn(),
      resetKey,
    }))

    const input = screen.getByTestId('f-filter') as HTMLInputElement
    await fireEvent.input(input, { target: { value: 'alph' } })
    expect(screen.queryByTestId('f-bravo')).toBeNull()

    setResetKey(1)
    expect(input.value).toBe('')
    expect(screen.getByTestId('f-bravo')).toBeInTheDocument()
  })

  it('opens on the SELECTED row rather than the top one', async () => {
    // This list is the only place a large group shows which value is active, so
    // opening at the top hides the answer whenever the selection is below the
    // fold. It also stops Enter on a fresh open from silently switching the
    // setting to the first option.
    const [resetKey, setResetKey] = createSignal(0)
    const onSelect = vi.fn()
    render(() => FilterableListbox({
      items: [
        { label: 'Alpha', value: 'alpha' },
        { label: 'Bravo', value: 'bravo' },
        { label: 'Charlie', value: 'charlie' },
      ],
      current: 'charlie',
      testIdPrefix: 'f',
      onSelect,
      resetKey,
    }))

    setResetKey(1)
    await fireEvent.keyDown(screen.getByTestId('f-filter'), { key: 'Enter' })
    expect(onSelect).toHaveBeenLastCalledWith('charlie')
  })

  it('falls back to the first row when the selection is not in the list', async () => {
    const [resetKey, setResetKey] = createSignal(0)
    const onSelect = vi.fn()
    render(() => FilterableListbox({
      items: [
        { label: 'Alpha', value: 'alpha' },
        { label: 'Bravo', value: 'bravo' },
      ],
      current: 'gone',
      testIdPrefix: 'f',
      onSelect,
      resetKey,
    }))

    setResetKey(1)
    await fireEvent.keyDown(screen.getByTestId('f-filter'), { key: 'Enter' })
    expect(onSelect).toHaveBeenLastCalledWith('alpha')
  })
})
