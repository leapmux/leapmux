import type { AvailableOption } from '~/generated/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { AvailableOptionGroupSchema, AvailableOptionSchema } from '~/generated/leapmux/v1/agent_pb'
import { AgentSettingsPanel, AgentSettingsPanelTriggerLabel } from './AgentSettingsPanel'

function opt(id: string, name: string): AvailableOption {
  return create(AvailableOptionSchema, { id, name })
}

function grp(
  id: string,
  label: string,
  options: AvailableOption[],
  extra: { currentValue?: string, defaultValue?: string, mutable?: boolean, order?: number } = {},
) {
  return create(AvailableOptionGroupSchema, {
    id,
    label,
    options,
    currentValue: extra.currentValue ?? '',
    defaultValue: extra.defaultValue ?? '',
    mutable: extra.mutable ?? true,
    order: extra.order ?? 0,
  })
}

/** N options for a non-model group, used to exercise the searchable threshold. */
function manyOptions(n: number): AvailableOption[] {
  return Array.from({ length: n }, (_, i) => opt(`o${i}`, `Option ${i}`))
}

describe('genericSettingsPanel', () => {
  it('renders every option group in backend order, by group id test prefix', () => {
    const groups = [
      grp('permissionMode', 'Permission Mode', [opt('default', 'Default'), opt('plan', 'Plan')], { order: 90, defaultValue: 'default' }),
      grp('model', 'Model', [opt('opus', 'Opus'), opt('sonnet', 'Sonnet')], { order: 10, defaultValue: 'opus' }),
    ]
    render(() => <AgentSettingsPanel optionGroups={groups} optionValues={{ model: 'opus', permissionMode: 'plan' }} onChange={() => {}} />)

    // Both groups render their value items keyed by the group id.
    expect(screen.getByTestId('model-opus')).toBeInTheDocument()
    expect(screen.getByTestId('permissionMode-plan')).toBeInTheDocument()

    // Order: model (10) renders before permissionMode (90).
    const labels = screen.getAllByText(/Model|Permission Mode/).map(el => el.textContent)
    expect(labels.indexOf('Model')).toBeLessThan(labels.indexOf('Permission Mode'))
  })

  it('keeps each group\'s radio DOM stable across catalog re-broadcasts (new objects, same ids)', () => {
    // The worker re-broadcasts the whole catalog on every status push with FRESH
    // group objects. Keying the group list by id (not object reference) must keep
    // each group's radio DOM in place -- recreating it would flicker the radios and
    // race a click landing on one while the agent is still settling.
    const [groups, setGroups] = createSignal([
      grp('model', 'Model', [opt('opus', 'Opus'), opt('sonnet', 'Sonnet')], { order: 10, currentValue: 'sonnet', defaultValue: 'opus' }),
    ])
    const [model, setModel] = createSignal('sonnet')
    render(() => <AgentSettingsPanel optionGroups={groups()} optionValues={{ model: model() }} onChange={() => {}} />)

    const before = screen.getByTestId('model-opus') as HTMLElement & { __marker?: string }
    before.__marker = 'kept'

    // A later push: brand-new group objects (different references), same ids, a
    // resolved model/currentValue, and a newly-appeared effort group.
    setGroups([
      grp('model', 'Model', [opt('opus', 'Opus'), opt('sonnet', 'Sonnet')], { order: 10, currentValue: 'opus', defaultValue: 'opus' }),
      grp('effort', 'Effort', [opt('high', 'High'), opt('max', 'Max')], { order: 20, currentValue: 'high' }),
    ])
    setModel('opus')

    const after = screen.getByTestId('model-opus') as HTMLElement & { __marker?: string }
    expect(after).toBe(before) // same DOM node survived the re-broadcast
    expect(after.__marker).toBe('kept')
    // The new group still renders, and the selection reactively moved to Opus.
    expect(screen.getByTestId('effort-high')).toBeInTheDocument()
    expect((screen.getByTestId('model-opus').querySelector('input') as HTMLInputElement).checked).toBe(true)
  })

  it('dispatches a uniform single-axis {sets} change when an option is picked', () => {
    const onChange = vi.fn()
    const groups = [grp('model', 'Model', [opt('opus', 'Opus'), opt('sonnet', 'Sonnet')], { defaultValue: 'opus' })]
    render(() => <AgentSettingsPanel optionGroups={groups} optionValues={{ model: 'opus' }} onChange={onChange} />)

    fireEvent.click(screen.getByTestId('model-sonnet'))
    expect(onChange).toHaveBeenCalledWith({ sets: { model: 'sonnet' } })
  })

  it('renders an agent-controlled (mutable=false) group read-only with a reason', () => {
    const groups = [grp('thought_level', 'Thought Level', [opt('low', 'Low'), opt('high', 'High')], { currentValue: 'high', mutable: false })]
    render(() => <AgentSettingsPanel optionGroups={groups} optionValues={{ thought_level: 'high' }} onChange={() => {}} />)

    const group = screen.getByTestId('thought_level-high').closest('[role="group"]')
    expect(group?.getAttribute('data-disabled')).toBe('')
  })

  // The headline of the refactor: the searchable-list treatment now applies to
  // EVERY option group, not just model.
  describe('searchable threshold applies to all groups (not just model)', () => {
    it('renders a NON-model group with > threshold options as a searchable list', () => {
      const groups = [grp('sandbox_policy', 'Sandbox', manyOptions(8), { defaultValue: 'o0' })]
      render(() => <AgentSettingsPanel optionGroups={groups} optionValues={{ sandbox_policy: 'o0' }} onChange={() => {}} />)

      // Searchable path renders a filter input and a "current" summary.
      expect(screen.getByTestId('sandbox_policy-filter')).toBeInTheDocument()
      expect(screen.getByTestId('sandbox_policy-current')).toBeInTheDocument()
    })

    it('renders a NON-model group at the threshold as plain radios (no filter)', () => {
      const groups = [grp('sandbox_policy', 'Sandbox', manyOptions(7), { defaultValue: 'o0' })]
      render(() => <AgentSettingsPanel optionGroups={groups} optionValues={{ sandbox_policy: 'o0' }} onChange={() => {}} />)

      expect(screen.queryByTestId('sandbox_policy-filter')).not.toBeInTheDocument()
      expect(screen.getByTestId('sandbox_policy-o3')).toBeInTheDocument()
    })

    it('filters the searchable list and dispatches the picked value', () => {
      const onChange = vi.fn()
      const groups = [grp('sandbox_policy', 'Sandbox', manyOptions(8), { defaultValue: 'o0' })]
      render(() => <AgentSettingsPanel optionGroups={groups} optionValues={{ sandbox_policy: 'o0' }} onChange={onChange} />)

      fireEvent.input(screen.getByTestId('sandbox_policy-filter'), { target: { value: 'Option 5' } })
      // The matching item is still present; a non-match is filtered out.
      expect(screen.getByTestId('sandbox_policy-o5')).toBeInTheDocument()
      expect(screen.queryByTestId('sandbox_policy-o1')).not.toBeInTheDocument()

      fireEvent.click(screen.getByTestId('sandbox_policy-o5'))
      expect(onChange).toHaveBeenCalledWith({ sets: { sandbox_policy: 'o5' } })
    })
  })

  it('falls back to the group default when the current value is not a valid option', () => {
    // Mid optimistic model switch the effort can be a tier the new model lacks.
    const groups = [grp('effort', 'Effort', [opt('auto', 'Auto'), opt('high', 'High')], { defaultValue: 'auto' })]
    render(() => <AgentSettingsPanel optionGroups={groups} optionValues={{ effort: 'xhigh' }} onChange={() => {}} />)

    const autoInput = screen.getByTestId('effort-auto').querySelector('input') as HTMLInputElement
    expect(autoInput.checked).toBe(true)
  })

  it('renders provider actions that set several groups at once, disabled when already applied', () => {
    const onChange = vi.fn()
    const groups = [
      grp('network_access', 'Network', [opt('restricted', 'Restricted'), opt('enabled', 'Enabled')], { defaultValue: 'restricted' }),
      grp('permissionMode', 'Approval', [opt('on-request', 'Suggest'), opt('never', 'Full Auto')], { defaultValue: 'on-request' }),
    ]
    const actions = [{ label: 'Bypass permissions', testId: 'codex-bypass-permissions', sets: { network_access: 'enabled', permissionMode: 'never' } }]
    render(() => (
      <AgentSettingsPanel
        optionGroups={groups}
        optionValues={{ permissionMode: 'on-request', network_access: 'restricted' }}
        actions={actions}
        onChange={onChange}
      />
    ))

    fireEvent.click(screen.getByTestId('codex-bypass-permissions'))
    // A multi-axis action dispatches ONE atomic change carrying both axes (not one per axis),
    // so the worker applies them in a single RPC and can't leave the agent half-bypassed.
    expect(onChange).toHaveBeenCalledTimes(1)
    expect(onChange).toHaveBeenCalledWith({ sets: { network_access: 'enabled', permissionMode: 'never' } })
  })

  it('disables a provider action when its target values are already set', () => {
    const groups = [grp('permissionMode', 'Approval', [opt('on-request', 'Suggest'), opt('never', 'Full Auto')], { defaultValue: 'on-request' })]
    const actions = [{ label: 'Bypass permissions', testId: 'codex-bypass-permissions', sets: { permissionMode: 'never' } }]
    render(() => <AgentSettingsPanel optionGroups={groups} optionValues={{ permissionMode: 'never' }} actions={actions} onChange={() => {}} />)

    expect((screen.getByTestId('codex-bypass-permissions') as HTMLButtonElement).disabled).toBe(true)
  })

  it('disables a provider action when the catalog currentValue alone matches (no optimistic value)', () => {
    // The agent is already in the bypass state per the catalog, but no optimistic value
    // was mirrored into optionValues. currentForGroup must fall back to the group's
    // currentValue so the button still reads as already-applied rather than enabled.
    const groups = [grp('permissionMode', 'Approval', [opt('on-request', 'Suggest'), opt('never', 'Full Auto')], { currentValue: 'never', defaultValue: 'on-request' })]
    const actions = [{ label: 'Bypass permissions', testId: 'codex-bypass-permissions', sets: { permissionMode: 'never' } }]
    render(() => <AgentSettingsPanel optionGroups={groups} optionValues={{}} actions={actions} onChange={() => {}} />)

    expect((screen.getByTestId('codex-bypass-permissions') as HTMLButtonElement).disabled).toBe(true)
  })

  it('disables a provider action whose targets match each group\'s resolved default (empty currentValue, no optimistic)', () => {
    // A freshly-opened agent before its first status push: the group has no optimistic value AND
    // an empty currentValue, so the radios resolve it to the backend defaultValue. An action whose
    // target IS that default must read as already-applied -- the disabled-check resolves the SAME
    // default the radios show selected (currentValueOrDefault), not the raw empty current, which
    // would have left the button spuriously enabled.
    const groups = [grp('permissionMode', 'Approval', [opt('on-request', 'Suggest'), opt('never', 'Full Auto')], { currentValue: '', defaultValue: 'on-request' })]
    const actions = [{ label: 'Suggest mode', testId: 'set-suggest', sets: { permissionMode: 'on-request' } }]
    render(() => <AgentSettingsPanel optionGroups={groups} optionValues={{}} actions={actions} onChange={() => {}} />)

    expect((screen.getByTestId('set-suggest') as HTMLButtonElement).disabled).toBe(true)
  })
})

describe('genericTriggerLabel', () => {
  it('shows the model and permission-mode labels driven by well-known ids', () => {
    const groups = [
      grp('model', 'Model', [opt('opus', 'Opus 4.8')], { currentValue: 'opus' }),
      grp('permissionMode', 'Permission Mode', [opt('plan', 'Plan Mode')], { currentValue: 'plan' }),
    ]
    const { container } = render(() => <AgentSettingsPanelTriggerLabel optionGroups={groups} optionValues={{ model: 'opus', permissionMode: 'plan' }} triggerModeGroupKey="permissionMode" />)
    expect(container.textContent).toContain('Opus 4.8')
    expect(container.textContent).toContain('Plan Mode')
    // A resolved model must NOT fall back to the unresolved-model placeholder.
    expect(container.textContent).not.toContain('…')
  })

  it('falls back to the catalog currentValue when optionValues lacks the id (structural parity with the panel)', () => {
    // [S7] valueFor resolves through effectiveCurrent, so a partial optionValues map (no
    // optimistic mirror for a group) still renders the catalog's confirmed currentValue
    // rather than the unresolved-model placeholder -- the same resolution the panel uses,
    // so the trigger and panel agree structurally instead of only when the caller happens
    // to seed optionValues from the already-overlaid groups.
    const groups = [
      grp('model', 'Model', [opt('opus', 'Opus 4.8')], { currentValue: 'opus' }),
      grp('permissionMode', 'Permission Mode', [opt('plan', 'Plan Mode')], { currentValue: 'plan' }),
    ]
    const { container } = render(() => <AgentSettingsPanelTriggerLabel optionGroups={groups} optionValues={{}} triggerModeGroupKey="permissionMode" />)
    expect(container.textContent).toContain('Opus 4.8')
    expect(container.textContent).toContain('Plan Mode')
    expect(container.textContent).not.toContain('…')
  })

  it('shows a placeholder in the model slot when the model group is absent (pre-handshake)', () => {
    // Copilot/OpenCode/Goose discover models dynamically from session/new, so a
    // freshly opened tab has no model group until the handshake completes. The
    // trigger must show the placeholder rather than a dangling, model-less "· Agent".
    const groups = [grp('permissionMode', 'Permission Mode', [opt('agent', 'Agent')], { currentValue: 'agent' })]
    const { container } = render(() => <AgentSettingsPanelTriggerLabel optionGroups={groups} optionValues={{ permissionMode: 'agent' }} triggerModeGroupKey="permissionMode" />)
    expect(container.textContent).toBe('… · Agent')
  })

  it('shows a placeholder when a model group exists but its value is unresolved', () => {
    // The companion case: the model group has arrived but no current value resolves
    // to a known option yet. The slot still renders the placeholder, not empty text.
    const groups = [
      grp('model', 'Model', [opt('opus', 'Opus 4.8')], { currentValue: '' }),
      grp('permissionMode', 'Permission Mode', [opt('agent', 'Agent')], { currentValue: 'agent' }),
    ]
    const { container } = render(() => <AgentSettingsPanelTriggerLabel optionGroups={groups} optionValues={{ permissionMode: 'agent' }} triggerModeGroupKey="permissionMode" />)
    expect(container.textContent).toBe('… · Agent')
  })

  it('renders the model alone for a model-only provider (no mode segment, no separator)', () => {
    // Reasonix-class provider: a model group but no permissionMode/plan group.
    // modeLabel() hits its no-group branch and returns '', so the trigger is just
    // the model with no trailing " · ".
    const groups = [grp('model', 'Model', [opt('opus', 'Opus 4.8')], { currentValue: 'opus' })]
    const { container } = render(() => <AgentSettingsPanelTriggerLabel optionGroups={groups} optionValues={{ model: 'opus' }} />)
    expect(container.textContent).toBe('Opus 4.8')
  })

  it('renders no mode segment when the declared mode group is absent (pre-handshake)', () => {
    // A provider declares triggerModeGroupKey but the group has not arrived yet (e.g.
    // Copilot/OpenCode before session/new reports its mode axis). The hasGroup guard
    // keeps the trigger from dangling a " · " with nothing after it.
    const groups = [grp('model', 'Model', [opt('opus', 'Opus 4.8')], { currentValue: 'opus' })]
    const { container } = render(() => (
      <AgentSettingsPanelTriggerLabel optionGroups={groups} optionValues={{ model: 'opus' }} triggerModeGroupKey="permissionMode" />
    ))
    expect(container.textContent).toBe('Opus 4.8')
  })

  it('renders no mode segment when the declared mode group has zero options', () => {
    // A mode group can transiently arrive with an empty option list (a server advertising an
    // axis before populating it). The panel's sortedGroups drops zero-option groups, so the
    // trigger must treat such a group as absent too -- otherwise it would dangle a "· Mode"
    // segment for a group the panel refuses to render (a trigger/panel disagreement).
    const groups = [
      grp('model', 'Model', [opt('opus', 'Opus 4.8')], { currentValue: 'opus' }),
      grp('permissionMode', 'Permission Mode', [], { currentValue: 'plan' }),
    ]
    const { container } = render(() => (
      <AgentSettingsPanelTriggerLabel optionGroups={groups} optionValues={{ model: 'opus', permissionMode: 'plan' }} triggerModeGroupKey="permissionMode" />
    ))
    expect(container.textContent).toBe('Opus 4.8')
  })

  it('shows the lone placeholder when nothing is resolved yet (model-only provider, pre-handshake)', () => {
    // No groups at all: the model slot is the placeholder and there is no effort
    // icon or mode segment, so the whole trigger is a bare "…".
    const { container } = render(() => <AgentSettingsPanelTriggerLabel optionGroups={[]} optionValues={{}} />)
    expect(container.textContent).toBe('…')
  })

  it('hides the mode segment (no dangling separator) when the permission-mode value is unresolved', () => {
    // Mode-side analogue of the model placeholder: a present-but-unresolved
    // permissionMode group must not leave a trailing " · " hanging off the model.
    const groups = [
      grp('model', 'Model', [opt('opus', 'Opus 4.8')], { currentValue: 'opus' }),
      grp('permissionMode', 'Permission Mode', [opt('plan', 'Plan Mode')], { currentValue: '' }),
    ]
    const { container } = render(() => <AgentSettingsPanelTriggerLabel optionGroups={groups} optionValues={{ model: 'opus' }} triggerModeGroupKey="permissionMode" />)
    expect(container.textContent).toBe('Opus 4.8')
  })

  // A provider whose mode axis is a non-permission group (Codex's collaboration_mode
  // "Workflow") declares triggerModeGroupKey for it, so the trigger renders the
  // Workflow value -- "Plan Mode" at the plan value, the Workflow default otherwise --
  // rather than the approval-policy permission mode.
  describe('mode segment driven by a non-permission group (Codex Workflow)', () => {
    const codexGroups = (collab: string) => [
      grp('model', 'Model', [opt('gpt', 'GPT-5.4 Mini')], { currentValue: 'gpt' }),
      grp('collaboration_mode', 'Workflow', [opt('default', 'Default'), opt('plan', 'Plan Mode')], { currentValue: collab }),
      grp('permissionMode', 'Approval Policy', [opt('on-request', 'Suggest & Approve')], { currentValue: 'on-request' }),
    ]

    it('shows the Workflow plan label when collaboration_mode is plan', () => {
      const { container } = render(() => (
        <AgentSettingsPanelTriggerLabel
          optionGroups={codexGroups('plan')}
          optionValues={{ model: 'gpt', permissionMode: 'on-request', collaboration_mode: 'plan' }}
          triggerModeGroupKey="collaboration_mode"
        />
      ))
      expect(container.textContent).toContain('GPT-5.4 Mini')
      expect(container.textContent).toContain('Plan Mode')
      expect(container.textContent).not.toContain('Suggest & Approve')
    })

    it('shows the Workflow value (not the approval policy) when not in plan mode', () => {
      const { container } = render(() => (
        <AgentSettingsPanelTriggerLabel
          optionGroups={codexGroups('default')}
          optionValues={{ model: 'gpt', permissionMode: 'on-request', collaboration_mode: 'default' }}
          triggerModeGroupKey="collaboration_mode"
        />
      ))
      // The mode segment is the declared Workflow group, so it reads "Default", NOT the
      // approval-policy permissionMode -- the trigger sources from exactly one group.
      expect(container.textContent).toContain('Default')
      expect(container.textContent).not.toContain('Suggest & Approve')
      expect(container.textContent).not.toContain('Plan Mode')
    })
  })

  it('renders the primary-agent group as the mode segment (OpenCode/Kilo) even off the plan value', () => {
    // Regression guard for the previous planMode-or-permissionMode logic, which hid
    // OpenCode/Kilo's primaryAgent segment whenever it was not at the plan value (those
    // providers have no permissionMode group to fall back to). Declaring
    // triggerModeGroupKey='primaryAgent' renders it for every value, plan or not.
    const groups = [
      grp('model', 'Model', [opt('sonnet', 'Claude Sonnet 4')], { currentValue: 'sonnet' }),
      grp('primaryAgent', 'Primary Agent', [opt('build', 'Build'), opt('plan', 'Plan')], { currentValue: 'build' }),
    ]
    const { container } = render(() => (
      <AgentSettingsPanelTriggerLabel
        optionGroups={groups}
        optionValues={{ model: 'sonnet', primaryAgent: 'build' }}
        triggerModeGroupKey="primaryAgent"
      />
    ))
    expect(container.textContent).toBe('Claude Sonnet 4 · Build')
  })
})
