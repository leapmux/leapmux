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
      disabledReason={opts.disabled ? 'This subagent doesn\'t accept messages.' : undefined}
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
/**
 * The reason a disabled control carries, read the way a screen reader gets it.
 *
 * <Tooltip> leaves an offscreen description in `aria-describedby` for as long
 * as the control is disabled. It is NOT `title`: a reason long enough to be
 * worth reading becomes the control's accessible name on `title`, which is why
 * `title` on a DOM element is now a lint error.
 */
function reasonOf(el: Element): string {
  const describedBy = el.getAttribute('aria-describedby')
  expect(describedBy).toBeTruthy()
  return document.getElementById(describedBy!)?.textContent ?? ''
}

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
    expect(reasonOf(item)).toBe('This setting is controlled by the agent')
    await fireEvent.click(item)
    expect(onChange).not.toHaveBeenCalled()
  })

  it('does not dispatch when the composer itself is disabled, and shows the CALLER\'s reason', async () => {
    // A non-steerable subagent: the group is mutable, but the composer accepts
    // no input, so a settings RPC must not leave the client. The sentence is the
    // caller's, not this component's -- the `[+]` menu's attach item and the
    // editor's placeholder show the same one, and inventing a second wording here
    // made one open menu disagree with itself.
    const { onChange } = renderPopover({ disabled: true })
    const item = screen.getByTestId('model-sonnet')
    expect(item).toBeDisabled()
    expect(reasonOf(item)).toBe('This subagent doesn\'t accept messages.')
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

  it('holds the option list still while the popover is open', async () => {
    // A push can change the SET of options, not only their objects: the worker
    // inserts the CLI's resolved model at its canonical slot, and the live CLI
    // catalog replaces the static fallback wholesale shortly after an agent
    // starts. An insert ABOVE the row the user is aiming at slides that row down
    // by its own height between the hit test and the click, and the option that
    // took its place is what gets applied -- a click on "Opus (1M context)"
    // launched Fable 5, the row directly above it.
    const [groups, setGroups] = createSignal([
      group({ options: [{ id: 'opus[1m]', name: 'Opus (1M context)' }, { id: 'sonnet', name: 'Sonnet' }], currentValue: 'sonnet' }),
    ])
    const onChange = vi.fn()
    render(() => (
      <OptionGroupPopover
        groupId="model"
        optionGroups={groups()}
        optionValues={{}}
        onChange={onChange}
        trigger={triggerProps => <button data-testid="trigger" {...triggerProps} />}
      />
    ))

    await fireEvent.click(screen.getByTestId('trigger'))

    const rowIds = () => screen.getAllByRole('menuitemradio', { hidden: true })
      .map(el => el.getAttribute('data-testid'))
    expect(rowIds()).toEqual(['model-opus[1m]', 'model-sonnet'])

    // The push that moves the rows: a model lands ABOVE the two already listed.
    const withFable = [
      { id: 'fable[1m]', name: 'Fable 5' },
      { id: 'opus[1m]', name: 'Opus (1M context)' },
      { id: 'sonnet', name: 'Sonnet' },
    ]
    setGroups([group({ options: withFable, currentValue: 'sonnet' })])
    await Promise.resolve()

    // Still the list the user opened, in the order they saw it.
    expect(rowIds()).toEqual(['model-opus[1m]', 'model-sonnet'])
    // And the row still dispatches the value it shows.
    await fireEvent.click(screen.getByTestId('model-opus[1m]'))
    expect(onChange).toHaveBeenCalledWith({ sets: { model: 'opus[1m]' } })

    // Closing releases the freeze: the next open shows the new model.
    await fireEvent.click(screen.getByTestId('trigger'))
    await Promise.resolve()
    expect(rowIds()).toEqual(['model-fable[1m]', 'model-opus[1m]', 'model-sonnet'])
  })

  /**
   * The other half of the freeze. Holding the list still stops a row sliding
   * under the pointer, but the same push that ADDS an option can REMOVE one --
   * the live CLI catalog replaces the static fallback wholesale, so a fallback
   * model it does not carry disappears. The frozen row for it is still rendered
   * and still wired, and applying it would relaunch the agent on an id the
   * provider rejects: the failure the freeze exists to prevent, from the other
   * direction.
   */
  it('does not apply a frozen option the live catalog withdrew', async () => {
    const [groups, setGroups] = createSignal([
      group({ options: [{ id: 'legacy', name: 'Legacy' }, { id: 'sonnet', name: 'Sonnet' }], currentValue: 'sonnet' }),
    ])
    const onChange = vi.fn()
    render(() => (
      <OptionGroupPopover
        groupId="model"
        optionGroups={groups()}
        optionValues={{}}
        onChange={onChange}
        trigger={triggerProps => <button data-testid="trigger" {...triggerProps} />}
      />
    ))

    await fireEvent.click(screen.getByTestId('trigger'))
    expect(screen.getByTestId('model-legacy')).toBeInTheDocument()

    // The live catalog lands and does not carry `legacy`.
    setGroups([group({ options: [{ id: 'sonnet', name: 'Sonnet' }], currentValue: 'sonnet' })])
    await Promise.resolve()

    // The frozen row is still on screen -- that is what the freeze does.
    await fireEvent.click(screen.getByTestId('model-legacy'))
    expect(onChange, 'a withdrawn option relaunches nothing').not.toHaveBeenCalled()

    // ...while a row the catalog still offers applies as usual.
    await fireEvent.click(screen.getByTestId('model-sonnet'))
    expect(onChange).toHaveBeenCalledWith({ sets: { model: 'sonnet' } })
  })

  it('fills an empty list that opened before its catalog arrived', async () => {
    // The freeze holds a list STILL; it must not hold a list EMPTY. A group whose
    // options have not landed yet has nothing to keep in place, and freezing on
    // the empty list would leave the menu blank until the user closed it again.
    const [groups, setGroups] = createSignal([group({ options: [] })])
    render(() => (
      <OptionGroupPopover
        groupId="model"
        optionGroups={groups()}
        optionValues={{}}
        onChange={() => {}}
        trigger={triggerProps => <button data-testid="trigger" {...triggerProps} />}
      />
    ))

    await fireEvent.click(screen.getByTestId('trigger'))
    expect(screen.queryByRole('menuitemradio', { hidden: true })).toBeNull()

    setGroups([group({ options: [{ id: 'sonnet', name: 'Sonnet' }], currentValue: 'sonnet' })])
    await Promise.resolve()

    expect(screen.getByTestId('model-sonnet')).toBeInTheDocument()
  })

  it('keeps the trigger label live while the list is frozen', async () => {
    // Only the MENU freezes. The chip is what the user reads to see which model
    // is running, so a settled value that arrives while the menu is open has to
    // reach it -- freezing the label would leave the chip contradicting the
    // agent for as long as the menu stayed open.
    const [groups, setGroups] = createSignal([
      group({ options: [{ id: 'sonnet', name: 'Sonnet' }], currentValue: 'sonnet' }),
    ])
    render(() => (
      <OptionGroupPopover
        groupId="model"
        optionGroups={groups()}
        optionValues={{}}
        onChange={() => {}}
        trigger={(triggerProps, view) => (
          <button data-testid="trigger" {...triggerProps}>{view.currentLabel}</button>
        )}
      />
    ))

    await fireEvent.click(screen.getByTestId('trigger'))
    expect(screen.getByTestId('trigger')).toHaveTextContent('Sonnet')

    setGroups([group({
      options: [{ id: 'sonnet', name: 'Sonnet' }, { id: 'opus[1m]', name: 'Opus (1M context)' }],
      currentValue: 'opus[1m]',
    })])
    await Promise.resolve()

    expect(screen.getByTestId('trigger')).toHaveTextContent('Opus (1M context)')
    // ...and the frozen menu still shows the rows the user opened.
    expect(screen.queryByTestId('model-opus[1m]')).toBeNull()
  })
})
