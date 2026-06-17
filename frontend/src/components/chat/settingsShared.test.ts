import type { AvailableOptionGroup } from '~/generated/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { AvailableOptionGroupSchema, AvailableOptionSchema } from '~/generated/leapmux/v1/agent_pb'
import { buildPlanMode, currentModeFor, currentValueOrDefault, effectiveCurrent, mergeStableOptionGroupRefs, optionGroupDefaultValue, valueValidForGroup } from './settingsGroups'
import { FilterableListbox, RadioGroup, SearchableSelect } from './settingsShared'

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
    // previous model must not stay selected in the RadioGroup.
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

const radioItems = [
  { label: 'Low', value: 'low' },
  { label: 'High', value: 'high' },
]

describe('radioGroup', () => {
  it('is writable by default: no data-disabled, enabled inputs, click fires onChange', async () => {
    const onChange = vi.fn()
    const { container } = render(() => RadioGroup({
      label: 'Effort',
      items: radioItems,
      testIdPrefix: 'tl',
      name: 'tl',
      current: 'low',
      onChange,
    }))

    const group = container.querySelector('[role="group"]')!
    expect(group.hasAttribute('data-disabled')).toBe(false)
    expect(group.hasAttribute('aria-disabled')).toBe(false)

    const highInput = screen.getByTestId('tl-high').querySelector('input')!
    expect(highInput).not.toBeDisabled()
    await fireEvent.click(highInput)
    expect(onChange).toHaveBeenCalledWith('high')
  })

  it('when disabled: marks the group, disables inputs, keeps the current value checked, and ignores clicks', async () => {
    const onChange = vi.fn()
    render(() => RadioGroup({
      label: 'Effort',
      items: radioItems,
      testIdPrefix: 'tl',
      name: 'tl',
      current: 'high',
      onChange,
      disabled: true,
    }))

    const group = screen.getByRole('group')
    expect(group).toHaveAttribute('data-disabled')
    expect(group).toHaveAttribute('aria-disabled', 'true')

    const highInput = screen.getByTestId('tl-high').querySelector('input')!
    const lowInput = screen.getByTestId('tl-low').querySelector('input')!
    expect(highInput).toBeDisabled()
    expect(lowInput).toBeDisabled()
    expect(highInput).toBeChecked() // current value stays selected

    await fireEvent.click(lowInput)
    expect(onChange).not.toHaveBeenCalled()
  })

  it('wraps the group in a tooltip when disabledReason is set', () => {
    const { container } = render(() => RadioGroup({
      label: 'Effort',
      items: radioItems,
      testIdPrefix: 'tl',
      name: 'tl',
      current: 'high',
      onChange: () => {},
      disabled: true,
      disabledReason: 'Controlled by the agent',
    }))

    // The Tooltip wraps its children in a display:contents span; the disabled group
    // sits directly inside it, confirming the reason is wired (not silently dropped).
    const group = container.querySelector('[role="group"]')!
    const wrapper = group.parentElement!
    expect(wrapper.tagName).toBe('SPAN')
    expect(wrapper.getAttribute('style')).toContain('display:contents')
  })
})

describe('searchableSelect', () => {
  // Past the searchable threshold, the model list renders as a filterable listbox.
  const manyItems = Array.from({ length: 9 }, (_, i) => ({ label: `Model ${i}`, value: `m${i}`, tooltip: `desc ${i}` }))

  it('renders each row with a keyboard-nav marker, wraps it for the hover tooltip, and fires onChange on click', async () => {
    const onChange = vi.fn()
    const { container } = render(() => SearchableSelect({
      label: 'Model',
      items: manyItems,
      testIdPrefix: 'm',
      current: 'm0',
      onChange,
    }))

    // Every row carries the keyboard-nav marker, so scrollIntoView can find it even
    // though each row sits inside the Tooltip's display:contents span.
    const rows = container.querySelectorAll('[data-listbox-item]')
    expect(rows.length).toBe(manyItems.length)
    const wrapper = rows[0].parentElement!
    expect(wrapper.tagName).toBe('SPAN')
    expect(wrapper.getAttribute('style')).toContain('display:contents')

    await fireEvent.click(screen.getByTestId('m-m3'))
    expect(onChange).toHaveBeenCalledWith('m3')
  })

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
