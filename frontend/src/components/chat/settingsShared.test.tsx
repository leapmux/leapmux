import type { AvailableOptionGroup } from '~/generated/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import { fireEvent, render, screen, within } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { AvailableOptionGroupSchema, AvailableOptionSchema } from '~/generated/leapmux/v1/agent_pb'
import { buildPlanMode, currentModeFor, currentValueOrDefault, effectiveCurrent, mergeStableOptionGroupRefs, optionGroupDefaultValue, resolvedCurrent, valueValidForGroup } from './settingsGroups'
import { FilterableListbox, OptionGroupMenuItems } from './settingsShared'

/**
 * Build a minimal effort option group fixture. Model/effort are ordinary option
 * groups now (the standalone AvailableModel/AvailableEffort messages were
 * removed), so validity is checked against the group's own options rather than
 * per-model.
 */
function effortGroup(effortIds: string[], defaultValue = 'auto'): AvailableOptionGroup {
  return {
    id: 'effort',
    label: 'Effort',
    options: effortIds.map(id => ({ id })),
    defaultValue,
  } as unknown as AvailableOptionGroup
}

const groups = [effortGroup(['auto', 'max', 'high', 'medium', 'low'])]

describe('valueValidForGroup', () => {
  it('returns true when the effort is one of the group tiers', () => {
    expect(valueValidForGroup(groups, 'effort', 'max')).toBe(true)
    expect(valueValidForGroup(groups, 'effort', 'high')).toBe(true)
  })

  it('returns false when the effort is not offered by the group', () => {
    // "ultracode"/"xhigh" left over after switching to a model that drops them.
    expect(valueValidForGroup(groups, 'effort', 'ultracode')).toBe(false)
    expect(valueValidForGroup(groups, 'effort', 'xhigh')).toBe(false)
  })

  it('returns false for a group that offers no efforts', () => {
    expect(valueValidForGroup([effortGroup([])], 'effort', 'auto')).toBe(false)
  })

  it('returns false for an unknown group id', () => {
    expect(valueValidForGroup(groups, 'not-a-group', 'auto')).toBe(false)
  })

  it('returns false when the group list is empty or undefined', () => {
    expect(valueValidForGroup(undefined, 'effort', 'auto')).toBe(false)
    expect(valueValidForGroup([], 'effort', 'auto')).toBe(false)
  })
})

describe('currentValueOrDefault', () => {
  it('returns the current effort when the group still offers it', () => {
    expect(currentValueOrDefault(groups, 'effort', 'max')).toBe('max')
    expect(currentValueOrDefault(groups, 'effort', 'high')).toBe('high')
  })

  it('falls back to the group default when the effort is not offered', () => {
    // The exact optimistic-switch case: "ultracode"/"xhigh" left over from a
    // previous model must not stay selected in the menu items.
    expect(currentValueOrDefault(groups, 'effort', 'ultracode')).toBe('auto')
    expect(currentValueOrDefault(groups, 'effort', 'xhigh')).toBe('auto')
  })

  it('falls back to the default for an unknown group or empty list', () => {
    expect(currentValueOrDefault(groups, 'not-a-group', 'high')).toBe('')
    expect(currentValueOrDefault(undefined, 'effort', 'high')).toBe('')
    expect(currentValueOrDefault([], 'effort', 'high')).toBe('')
  })

  // S6: a transient empty default on the effort group falls back to EFFORT_AUTO so the select
  // never renders blank during a first-handshake catalog -- but only when the group offers auto.
  it('falls back to EFFORT_AUTO when the effort default is empty but auto is offered', () => {
    const g = [effortGroup(['auto', 'high', 'low'], '')] // empty default (transient handshake)
    expect(currentValueOrDefault(g, 'effort', 'xhigh')).toBe('auto')
  })

  it('returns empty for an effort group with an empty default that does NOT offer auto', () => {
    const g = [effortGroup(['high', 'low'], '')] // no auto tier, empty default
    expect(currentValueOrDefault(g, 'effort', 'xhigh')).toBe('')
  })

  it('does not apply the auto fallback to a non-effort group', () => {
    const g = [{ id: 'reasoning', options: [{ id: 'auto' }, { id: 'on' }], defaultValue: '' } as unknown as AvailableOptionGroup]
    expect(currentValueOrDefault(g, 'reasoning', 'off')).toBe('')
  })

  // [V6] The MODEL select must never render blank: a dynamic-model ACP provider
  // (Copilot/OpenCode/Goose) can report a model list before any current/default resolves.
  // Unlike effort tiers (sorted strongest-first), the first model is the catalog's
  // most-preferred, so it is a safe fallback over rendering nothing.
  const modelGroup = (defaultValue = '') => [{ id: 'model', options: [{ id: 'gpt-5' }, { id: 'gpt-4' }], defaultValue } as unknown as AvailableOptionGroup]

  it('falls back to the first model when the model group has no default and the value is invalid', () => {
    expect(currentValueOrDefault(modelGroup(), 'model', '')).toBe('gpt-5')
    expect(currentValueOrDefault(modelGroup(), 'model', 'not-a-model')).toBe('gpt-5')
  })

  it('prefers a valid current and a declared default over the model first-option fallback', () => {
    expect(currentValueOrDefault(modelGroup('gpt-4'), 'model', 'gpt-5')).toBe('gpt-5') // valid current wins
    expect(currentValueOrDefault(modelGroup('gpt-4'), 'model', 'bogus')).toBe('gpt-4') // declared default beats options[0]
  })

  it('returns empty for a model group with no options', () => {
    const empty = [{ id: 'model', options: [], defaultValue: '' } as unknown as AvailableOptionGroup]
    expect(currentValueOrDefault(empty, 'model', '')).toBe('')
  })
})

describe('effectiveCurrent', () => {
  const group = { id: 'effort', currentValue: 'high' } as unknown as AvailableOptionGroup

  it('prefers the optimistic optionValues entry over the catalog currentValue', () => {
    expect(effectiveCurrent({ effort: 'low' }, group)).toBe('low')
  })

  it('falls back to the catalog currentValue when no optimistic value is set', () => {
    expect(effectiveCurrent({}, group)).toBe('high')
    expect(effectiveCurrent(undefined, group)).toBe('high')
  })

  it('returns empty string when neither is set or the group is missing', () => {
    expect(effectiveCurrent(undefined, undefined)).toBe('')
    expect(effectiveCurrent({}, { id: 'effort', currentValue: '' } as unknown as AvailableOptionGroup)).toBe('')
  })

  // S7: a stored empty string violates the invariant (setOptionValue deletes on empty); the
  // read warns and still falls through to the catalog current rather than masking it silently.
  it('warns and falls through to the catalog current when a stored value is empty', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    expect(effectiveCurrent({ effort: '' }, group)).toBe('high')
    expect(warn).toHaveBeenCalledOnce()
    warn.mockRestore()
  })

  it('does not warn when the option key is simply absent', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    expect(effectiveCurrent({}, group)).toBe('high')
    expect(warn).not.toHaveBeenCalled()
    warn.mockRestore()
  })
})

describe('currentModeFor', () => {
  const read = currentModeFor('permissionMode', 'default')

  it('reads the agent optionValues entry for the group key', () => {
    expect(read({ optionValues: { permissionMode: 'plan' } })).toBe('plan')
  })

  it('falls back to the default when the key is absent or optionValues is undefined', () => {
    expect(read({ optionValues: {} })).toBe('default')
    expect(read({})).toBe('default')
  })
})

describe('buildPlanMode', () => {
  it('assembles the 4-field planMode shape with currentMode derived from the same key/default', () => {
    const pm = buildPlanMode('collaboration_mode', 'plan', 'collaborate')
    expect(pm.groupKey).toBe('collaboration_mode')
    expect(pm.planValue).toBe('plan')
    expect(pm.defaultValue).toBe('collaborate')
    // currentMode reads the SAME group key the field reports, and falls back to the SAME default --
    // so the closure can't drift from the sibling fields (the drift an inline literal allowed).
    expect(pm.currentMode({ optionValues: { collaboration_mode: 'plan' } })).toBe('plan')
    expect(pm.currentMode({ optionValues: {} })).toBe('collaborate')
    expect(pm.currentMode({})).toBe('collaborate')
  })
})

describe('optionGroupDefaultValue', () => {
  it('returns the backend-declared default', () => {
    expect(optionGroupDefaultValue(groups, 'effort')).toBe('auto')
  })

  it('returns empty (not the strongest tier) when the backend reports no default', () => {
    // effort/thought_level groups are sorted strongest-first, so options[0] is the most
    // aggressive tier. A blank backend default must NOT silently preselect it; an empty
    // result lets the select render unselected rather than wrong.
    const noDefault = effortGroup(['xhigh', 'high', 'medium', 'low'], '')
    expect(optionGroupDefaultValue([noDefault], 'effort')).toBe('')
    // currentValueOrDefault with an invalid current then also yields empty, not "xhigh".
    expect(currentValueOrDefault([noDefault], 'effort', 'ultracode')).toBe('')
  })

  it('returns empty for an unknown group or no options', () => {
    expect(optionGroupDefaultValue(groups, 'not-a-group')).toBe('')
    expect(optionGroupDefaultValue([effortGroup([])], 'effort')).toBe('')
  })
})

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
    expect(withTip.getAttribute('style')).toContain('display:contents')
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

describe('mergeStableOptionGroupRefs', () => {
  function group(id: string, currentValue: string): AvailableOptionGroup {
    return create(AvailableOptionGroupSchema, {
      id,
      label: id,
      order: 10,
      currentValue,
      options: [create(AvailableOptionSchema, { id: 'x', name: 'x' })],
    })
  }

  it('returns the previous array when every group is unchanged', () => {
    const prev = [group('model', 'sonnet'), group('effort', 'high')]
    // Distinct objects, identical content (mirrors a re-decoded broadcast).
    const next = [group('model', 'sonnet'), group('effort', 'high')]
    expect(mergeStableOptionGroupRefs(next, prev)).toBe(prev)
  })

  it('reuses an unchanged group reference when a sibling changes', () => {
    const prev = [group('model', 'sonnet'), group('effort', 'high')]
    const next = [group('model', 'sonnet'), group('effort', 'xhigh')]
    const out = mergeStableOptionGroupRefs(next, prev)
    expect(out).not.toBe(prev)
    expect(out[0]).toBe(prev[0]) // unchanged model group keeps its reference
    expect(out[1]).toBe(next[1]) // changed effort group is the fresh one
    expect(out[1].currentValue).toBe('xhigh')
  })

  it('keeps the new reference for an added group', () => {
    const prev = [group('model', 'sonnet')]
    const next = [group('model', 'sonnet'), group('effort', 'high')]
    const out = mergeStableOptionGroupRefs(next, prev)
    expect(out[0]).toBe(prev[0])
    expect(out[1]).toBe(next[1])
  })
})

describe('resolvedCurrent', () => {
  const catalog = [effortGroup(['auto', 'max', 'high', 'low'], 'high')]

  it('prefers the optimistic value over the catalog current value', () => {
    const groups = [{ ...catalog[0]!, currentValue: 'low' } as AvailableOptionGroup]
    expect(resolvedCurrent(groups, { effort: 'max' }, 'effort')).toBe('max')
  })

  it('falls back to the catalog current value when nothing is optimistic', () => {
    const groups = [{ ...catalog[0]!, currentValue: 'low' } as AvailableOptionGroup]
    expect(resolvedCurrent(groups, {}, 'effort')).toBe('low')
  })

  it('clamps a value the group no longer offers to the group default', () => {
    // The optimistic-switch case: "xhigh" left over from the previous model.
    // The chip label, the checked radio, and an action's already-applied test
    // all read this, so they cannot disagree about the selection.
    expect(resolvedCurrent(catalog, { effort: 'xhigh' }, 'effort')).toBe('high')
  })

  it('returns an empty string for an unknown group', () => {
    expect(resolvedCurrent(catalog, { effort: 'max' }, 'not-a-group')).toBe('')
  })

  it('returns an empty string for an undefined catalog', () => {
    expect(resolvedCurrent(undefined, {}, 'effort')).toBe('')
  })
})

describe('optionGroupMenuItems', () => {
  const smallItems = [
    { label: 'Low', value: 'low' },
    { label: 'High', value: 'high' },
  ]

  it('renders radio menu items for ≤7 options and fires onChange on click', async () => {
    const onChange = vi.fn()
    render(() => (
      <OptionGroupMenuItems
        label="Effort"
        items={smallItems}
        testIdPrefix="effort"
        current="low"
        onChange={onChange}
      />
    ))

    // Each option is a menuitemradio button.
    const items = screen.getAllByRole('menuitemradio')
    expect(items).toHaveLength(2)

    // The current value is marked aria-checked.
    const lowItem = screen.getByTestId('effort-low')
    expect(lowItem).toHaveAttribute('aria-checked', 'true')
    const highItem = screen.getByTestId('effort-high')
    expect(highItem).toHaveAttribute('aria-checked', 'false')

    // Clicking an item fires onChange with its value.
    await fireEvent.click(highItem)
    expect(onChange).toHaveBeenCalledWith('high')
  })

  it('renders an unchecked radio input inside each item', () => {
    render(() => (
      <OptionGroupMenuItems
        label="Effort"
        items={smallItems}
        testIdPrefix="effort"
        current="low"
        onChange={() => {}}
      />
    ))

    // The current item's radio is checked.
    const lowRadio = screen.getByTestId('effort-low').querySelector('input[type="radio"]') as HTMLInputElement
    expect(lowRadio.checked).toBe(true)
    expect(lowRadio.disabled).toBe(true) // display-only

    const highRadio = screen.getByTestId('effort-high').querySelector('input[type="radio"]') as HTMLInputElement
    expect(highRadio.checked).toBe(false)
    expect(highRadio.disabled).toBe(true)
  })

  it('disables items and suppresses onChange when disabled=true', async () => {
    const onChange = vi.fn()
    render(() => (
      <OptionGroupMenuItems
        label="Effort"
        items={smallItems}
        testIdPrefix="effort"
        current="low"
        onChange={onChange}
        disabled
        disabledReason="Controlled by the agent"
      />
    ))

    const items = screen.getAllByRole('menuitemradio')
    for (const item of items) {
      expect(item).toBeDisabled()
      expect(item).toHaveAttribute('title', 'Controlled by the agent')
    }

    await fireEvent.click(items[1])
    expect(onChange).not.toHaveBeenCalled()
  })

  it('renders a FilterableListbox for >7 options', () => {
    const manyItems = Array.from({ length: 9 }, (_, i) => ({ label: `Model ${i}`, value: `m${i}` }))
    render(() => (
      <OptionGroupMenuItems
        label="Model"
        items={manyItems}
        testIdPrefix="model"
        current="m0"
        onChange={() => {}}
      />
    ))

    // FilterableListbox renders a filter input + rows with data-listbox-item.
    expect(screen.getByTestId('model-filter')).toBeInTheDocument()
    const rows = document.querySelectorAll('[data-listbox-item]')
    expect(rows).toHaveLength(9)
    // No menuitemradio buttons.
    expect(screen.queryByRole('menuitemradio')).toBeNull()
  })

  it('filterableListbox onSelect fires onChange', async () => {
    const manyItems = Array.from({ length: 9 }, (_, i) => ({ label: `Model ${i}`, value: `m${i}` }))
    const onChange = vi.fn()
    render(() => (
      <OptionGroupMenuItems
        label="Model"
        items={manyItems}
        testIdPrefix="model"
        current="m0"
        onChange={onChange}
      />
    ))

    await fireEvent.click(screen.getByTestId('model-m3'))
    expect(onChange).toHaveBeenCalledWith('m3')
  })

  it('wraps a radio item in a Tooltip only when the option has description text', () => {
    // Same reason the filterable rows do: a Tooltip mounts its wrapper and its
    // listeners even with nothing to show, and most option values carry no
    // description.
    render(() => (
      <OptionGroupMenuItems
        label="Effort"
        items={[
          { label: 'High', value: 'high', tooltip: 'Comprehensive' },
          { label: 'Low', value: 'low' },
        ]}
        testIdPrefix="effort"
        current="high"
        onChange={() => {}}
      />
    ))

    const withTip = screen.getByTestId('effort-high').parentElement!
    expect(withTip.tagName).toBe('SPAN')
    expect(withTip.getAttribute('style')).toContain('display:contents')
    expect(screen.getByTestId('effort-low').parentElement!.tagName).not.toBe('SPAN')
  })

  it('shows the current value, not the group label, when disabled with >7 options', () => {
    const manyItems = Array.from({ length: 9 }, (_, i) => ({ label: `Model ${i}`, value: `m${i}` }))
    render(() => (
      <OptionGroupMenuItems
        label="Model"
        items={manyItems}
        testIdPrefix="model"
        current="m3"
        onChange={() => {}}
        disabled
        disabledReason="Controlled by the agent"
      />
    ))

    // No filter input; a read-only span that shows the selection. Showing only the
    // group label ("Model") would leave the user no way to see which model the
    // agent is running, since the list itself is not offered.
    expect(screen.queryByTestId('model-filter')).toBeNull()
    const readOnly = screen.getByText('Model 3')
    expect(readOnly.tagName).toBe('SPAN')
    expect(readOnly).toHaveAttribute('title', 'Controlled by the agent')
  })

  it('falls back to the group label when the current value is not in the list', () => {
    const manyItems = Array.from({ length: 9 }, (_, i) => ({ label: `Model ${i}`, value: `m${i}` }))
    render(() => (
      <OptionGroupMenuItems
        label="Model"
        items={manyItems}
        testIdPrefix="model"
        current="gone"
        onChange={() => {}}
        disabled
        disabledReason="Controlled by the agent"
      />
    ))

    expect(screen.getByText('Model')).toHaveAttribute('title', 'Controlled by the agent')
  })
})
