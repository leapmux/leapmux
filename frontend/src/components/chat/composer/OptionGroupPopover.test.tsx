import type { AvailableOptionGroup } from '~/generated/leapmux/v1/agent_pb'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { OptionGroupPopover } from './OptionGroupPopover'

function group(overrides: Partial<Omit<AvailableOptionGroup, 'options'>> & { options: { id: string, name?: string }[] }): AvailableOptionGroup {
  return {
    id: 'model',
    label: 'Model',
    mutable: true,
    defaultValue: '',
    currentValue: '',
    order: 0,
    ...overrides,
  } as unknown as AvailableOptionGroup
}

function renderPopover(opts: {
  groups?: AvailableOptionGroup[]
  values?: Record<string, string>
  disabled?: boolean
  popoverTestId?: string
} = {}) {
  const onChange = vi.fn()
  render(() => (
    <OptionGroupPopover
      groupId="model"
      optionGroups={opts.groups ?? [group({ options: [{ id: 'opus', name: 'Opus' }, { id: 'sonnet', name: 'Sonnet' }], currentValue: 'opus' })]}
      optionValues={opts.values ?? {}}
      onChange={onChange}
      disabled={opts.disabled}
      popoverTestId={opts.popoverTestId}
      trigger={(triggerProps, view) => (
        <button data-testid="trigger" {...triggerProps}>
          {view.currentLabel || view.label}
          {view.mutable ? '' : ' (locked)'}
        </button>
      )}
    />
  ))
  return { onChange, trigger: screen.getByTestId('trigger') }
}

// The options live inside a `popover` <menu>, which is inaccessible until the
// popover opens — and jsdom stubs the Popover API, so it never does. Role
// queries therefore pass `hidden: true`; the roles themselves are the point of
// the assertions.
describe('optionGroupPopover', () => {
  it('shows the resolved current option in the trigger view', () => {
    const { trigger } = renderPopover()
    expect(trigger).toHaveTextContent('Opus')
  })

  it('prefers the optimistic value over the catalog current value', () => {
    const { trigger } = renderPopover({ values: { model: 'sonnet' } })
    expect(trigger).toHaveTextContent('Sonnet')
  })

  it('clamps a current value the group no longer offers to the group default', () => {
    // The optimistic-switch case: an effort tier left over from the previous
    // model. The trigger and the checked radio must agree on the clamped value.
    const { trigger } = renderPopover({
      groups: [group({ options: [{ id: 'opus', name: 'Opus' }], currentValue: 'gone', defaultValue: 'opus' })],
    })
    expect(trigger).toHaveTextContent('Opus')
    expect(screen.getByRole('menuitemradio', { name: /Opus/, hidden: true })).toHaveAttribute('aria-checked', 'true')
  })

  it('dispatches the selected value keyed by group id', async () => {
    const { onChange } = renderPopover()
    await fireEvent.click(screen.getByTestId('model-sonnet'))
    expect(onChange).toHaveBeenCalledWith({ sets: { model: 'sonnet' } })
  })

  it('does not dispatch for an agent-controlled group, and says so', async () => {
    const { onChange, trigger } = renderPopover({
      groups: [group({ mutable: false, options: [{ id: 'opus', name: 'Opus' }, { id: 'sonnet', name: 'Sonnet' }], currentValue: 'opus' })],
    })
    expect(trigger).toHaveTextContent('(locked)')
    const item = screen.getByTestId('model-sonnet')
    expect(item).toBeDisabled()
    expect(item).toHaveAttribute('title', 'This setting is controlled by the agent')
    await fireEvent.click(item)
    expect(onChange).not.toHaveBeenCalled()
  })

  it('does not dispatch when the composer itself is disabled', async () => {
    // A non-steerable subagent: the group is mutable, but the composer accepts
    // no input, so a settings RPC must not leave the client.
    const { onChange } = renderPopover({ disabled: true })
    const item = screen.getByTestId('model-sonnet')
    expect(item).toBeDisabled()
    expect(item).toHaveAttribute('title', 'This agent does not accept setting changes')
    await fireEvent.click(item)
    expect(onChange).not.toHaveBeenCalled()
  })

  it('renders nothing selectable, and an empty view, for an absent group', () => {
    const { trigger } = renderPopover({ groups: [] })
    expect(trigger).toHaveTextContent('model')
    expect(screen.queryByRole('menuitemradio', { hidden: true })).toBeNull()
  })

  it('switches to a filterable listbox past the searchable threshold', () => {
    const many = Array.from({ length: 8 }, (_, i) => ({ id: `m${i}`, name: `Model ${i}` }))
    renderPopover({ groups: [group({ options: many, currentValue: 'm0' })] })
    expect(screen.getByTestId('model-filter')).toBeInTheDocument()
    expect(screen.queryByRole('menuitemradio', { hidden: true })).toBeNull()
  })

  it('keeps radio items at exactly the threshold', () => {
    const seven = Array.from({ length: 7 }, (_, i) => ({ id: `m${i}`, name: `Model ${i}` }))
    renderPopover({ groups: [group({ options: seven, currentValue: 'm0' })] })
    expect(screen.queryByTestId('model-filter')).toBeNull()
    expect(screen.getAllByRole('menuitemradio', { hidden: true })).toHaveLength(7)
  })

  it('gives the popover the group name, so its items announce which axis they set', () => {
    // A bare <menu> of menuitemradio children carries no accessible name, and
    // the values alone ("default", "plan") do not say what they configure.
    renderPopover({ popoverTestId: 'model-popover' })
    expect(screen.getByTestId('model-popover')).toHaveAttribute('aria-label', 'Model')
  })

  it('keeps each option row\'s DOM stable across a catalog re-broadcast', async () => {
    // The worker re-broadcasts the WHOLE option catalog on every status push, so
    // the same group arrives as a fresh object with the same ids many times
    // during a settle. If a push rebuilds the rows, one lands between the user's
    // pointerdown and their click and the pick is lost -- the model picker goes
    // effectively unclickable under load.
    //
    // Two guards hold this today and either one alone is sufficient: `<Index>`
    // in OptionGroupMenuItems keys rows by position, and the `items` memo's
    // value comparator hands back the previous array. This test asserts the
    // OUTCOME, so it survives a change to either mechanism and fails once both
    // are gone -- which is the state the invariant actually cares about.
    const options = [{ id: 'opus', name: 'Opus' }, { id: 'sonnet', name: 'Sonnet' }]
    const [groups, setGroups] = createSignal([group({ options, currentValue: 'opus' })])
    render(() => (
      <OptionGroupPopover
        groupId="model"
        optionGroups={groups()}
        optionValues={{}}
        onChange={() => {}}
        trigger={triggerProps => <button data-testid="trigger" {...triggerProps} />}
      />
    ))

    const before = screen.getByTestId('model-sonnet')
    // Fresh objects, identical values -- exactly what a status push delivers.
    setGroups([group({ options: options.map(o => ({ ...o })), currentValue: 'opus' })])
    await Promise.resolve()

    expect(screen.getByTestId('model-sonnet')).toBe(before)
  })
})
