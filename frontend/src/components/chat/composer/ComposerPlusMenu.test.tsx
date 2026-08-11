import type { AvailableOptionGroup } from '~/generated/leapmux/v1/agent_pb'
import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { ComposerPlusMenu } from './ComposerPlusMenu'
import '~/components/chat/providers'

function group(id: string, label: string, order: number, optionIds: string[] = ['a', 'b']): AvailableOptionGroup {
  return {
    id,
    label,
    order,
    mutable: true,
    defaultValue: optionIds[0] ?? '',
    currentValue: optionIds[0] ?? '',
    options: optionIds.map(o => ({ id: o, name: o })),
  } as unknown as AvailableOptionGroup
}

function renderMenu(opts: {
  groups?: AvailableOptionGroup[]
  provider?: AgentProvider
  values?: Record<string, string>
  disabledReason?: string
  canAttach?: boolean
  agentInfo?: boolean
  settingsLoading?: boolean
  branchName?: string
  branchDisabledReason?: string
} = {}) {
  const onSettingChange = vi.fn()
  const onAttachFile = vi.fn()
  const onToggleEnterMode = vi.fn()
  const onToggleStatusBar = vi.fn()
  const onChangeBranch = vi.fn()
  const onDeleteBranch = vi.fn()
  const rendered = render(() => (
    <ComposerPlusMenu
      optionGroups={opts.groups ?? []}
      optionValues={opts.values ?? {}}
      agentProvider={opts.provider}
      onSettingChange={onSettingChange}
      onAttachFile={onAttachFile}
      canAttach={opts.canAttach ?? true}
      disabledReason={opts.disabledReason}
      settingsLoading={opts.settingsLoading}
      branchName={opts.branchName}
      onChangeBranch={onChangeBranch}
      onDeleteBranch={onDeleteBranch}
      branchDisabledReason={opts.branchDisabledReason}
      enterKeyMode={() => 'cmd-enter-sends'}
      onToggleEnterMode={onToggleEnterMode}
      showStatusBar={() => true}
      onToggleStatusBar={onToggleStatusBar}
      agentInfo={opts.agentInfo ? () => <span data-testid="agent-info-rows" /> : undefined}
    />
  ))
  return { ...rendered, onSettingChange, onAttachFile, onToggleEnterMode, onToggleStatusBar, onChangeBranch, onDeleteBranch }
}

describe('composerPlusMenu', () => {
  it('lists the settings submenus in backend order', () => {
    renderMenu({
      groups: [group('c', 'Gamma', 30), group('a', 'Alpha', 10), group('b', 'Beta', 20)],
    })

    const labels = screen.getAllByRole('menuitem', { hidden: true })
      .map(el => el.textContent ?? '')
      .filter(t => ['Alpha', 'Beta', 'Gamma'].some(l => t.includes(l)))
    expect(labels).toEqual(['Alpha', 'Beta', 'Gamma'])
  })

  it('drops a group that offers no options', () => {
    // The backend can report a group before its option list resolves; a
    // submenu that opens onto nothing is a dead end.
    renderMenu({ groups: [group('a', 'Alpha', 10), group('empty', 'Empty', 20, [])] })

    expect(screen.getByText('Alpha')).toBeInTheDocument()
    expect(screen.queryByText('Empty')).toBeNull()
  })

  it('dispatches a settings change from a submenu', async () => {
    const { onSettingChange } = renderMenu({ groups: [group('model', 'Model', 10, ['opus', 'sonnet'])] })

    await fireEvent.click(screen.getByTestId('model-sonnet'))
    expect(onSettingChange).toHaveBeenCalledWith({ sets: { model: 'sonnet' } })
  })

  it('dispatches a provider action as ONE atomic change carrying every axis', async () => {
    // Splitting a multi-axis action into several RPCs can leave the agent
    // half-configured, so the action must set all of its axes together.
    const { onSettingChange } = renderMenu({
      provider: AgentProvider.CODEX,
      groups: [
        group('network_access', 'Network', 10, ['disabled', 'enabled']),
        group('sandbox_policy', 'Sandbox', 20, ['read-only', 'danger-full-access']),
        group('permissionMode', 'Approval', 30, ['on-request', 'never']),
      ],
    })

    const action = screen.getAllByRole('menuitem', { hidden: true })
      .find(el => (el.textContent ?? '').toLowerCase().includes('bypass'))
    expect(action).toBeDefined()
    await fireEvent.click(action!)

    expect(onSettingChange).toHaveBeenCalledTimes(1)
    const change = onSettingChange.mock.calls[0]![0] as { sets: Record<string, string> }
    expect(Object.keys(change.sets).length).toBeGreaterThan(1)
  })

  it('disables a provider action whose axes are already applied', () => {
    const { onSettingChange } = renderMenu({
      provider: AgentProvider.CODEX,
      groups: [
        group('network_access', 'Network', 10, ['enabled']),
        group('sandbox_policy', 'Sandbox', 20, ['danger-full-access']),
        group('permissionMode', 'Approval', 30, ['never']),
      ],
    })

    const action = screen.getAllByRole('menuitem', { hidden: true })
      .find(el => (el.textContent ?? '').toLowerCase().includes('bypass'))
    expect(action).toBeDisabled()
    expect(onSettingChange).not.toHaveBeenCalled()
  })

  it('disables attach during a control request, and says why', () => {
    renderMenu({ canAttach: false })

    const attach = screen.getByTestId('composer-attach-file')
    expect(attach).toBeDisabled()
    expect(attach).toHaveAttribute('title', 'Attach is unavailable during a control request')
  })

  it('disables attach and the settings submenus when the composer accepts no input', async () => {
    const { onSettingChange } = renderMenu({
      groups: [group('model', 'Model', 10, ['opus', 'sonnet'])],
      disabledReason: 'This subagent doesn\'t accept messages.',
    })

    expect(screen.getByTestId('composer-attach-file')).toBeDisabled()

    await fireEvent.click(screen.getByTestId('model-sonnet'))
    expect(onSettingChange).not.toHaveBeenCalled()
  })

  it('states the caller\'s disabled reason, not one of its own', () => {
    // The panel feeds the SAME string to the editor's disabled placeholder and
    // to the hint above the box. A reason invented here would be a third copy
    // that drifts -- a subagent tab would read "subagent" in the box and
    // "agent" in this menu.
    renderMenu({ disabledReason: 'This subagent doesn\'t accept messages.' })

    expect(screen.getByTestId('composer-attach-file'))
      .toHaveAttribute('title', 'This subagent doesn\'t accept messages.')
  })

  it('leaves attach live when no reason is given', () => {
    // The reason IS the disabled flag, so "dead with nothing to say" cannot be
    // expressed. The menu also applies no fallback of its own: AgentEditorPanel
    // resolves ONE reason and hands every surface the same resolved string, so a
    // default invented here could only be a second copy that drifts.
    renderMenu({})

    const attach = screen.getByTestId('composer-attach-file')
    expect(attach).toBeEnabled()
    expect(attach).not.toHaveAttribute('title')
  })

  it('prefers the disabled reason over the control-request reason', () => {
    // A disabled composer during a control request is still disabled; the
    // narrower "unavailable during a control request" would understate it.
    renderMenu({ canAttach: false, disabledReason: 'No input here.' })

    expect(screen.getByTestId('composer-attach-file'))
      .toHaveAttribute('title', 'No input here.')
  })

  it('keeps the view toggles live on a disabled composer', async () => {
    // They are local display preferences, not agent settings.
    const { onToggleStatusBar } = renderMenu({ disabledReason: 'This subagent doesn\'t accept messages.' })

    await fireEvent.click(screen.getByRole('menuitemcheckbox', { name: /Show status bar/, hidden: true }))
    expect(onToggleStatusBar).toHaveBeenCalledTimes(1)
  })

  it('carries the agent-info rows so hiding the status bar cannot lose them', () => {
    // Every settings axis has a second home in this menu, but the context-usage
    // and rate-limit rows do not -- so switching the status bar off would
    // otherwise make them unreachable.
    renderMenu({ agentInfo: true })

    expect(screen.getByTestId('composer-agent-info')).toBeInTheDocument()
    expect(screen.getByTestId('agent-info-rows')).toBeInTheDocument()
  })

  it('omits the agent-info item when there is nothing to show', () => {
    renderMenu()
    expect(screen.queryByTestId('composer-agent-info')).toBeNull()
  })

  it('marks an in-flight settings change on the trigger, and only while it is in flight', () => {
    // Every settings surface flips its label optimistically the moment a value
    // is picked, so without this marker a pending change is indistinguishable
    // from an applied one and the user picks again. It rides THIS button
    // because it is the only settings surface that is always present -- the
    // status bar is a preference this menu can switch off.
    renderMenu({ settingsLoading: true })
    expect(screen.getByTestId('settings-loading-spinner')).toBeInTheDocument()
    // The menu still opens while a change applies.
    expect(screen.getByTestId('composer-plus-trigger')).not.toBeDisabled()

    cleanup()
    renderMenu()
    expect(screen.queryByTestId('settings-loading-spinner')).toBeNull()
  })

  it('carries the branch actions so hiding the status bar cannot lose them', async () => {
    // Every other axis has a second home in this menu. The branch chip lives
    // only on the status bar, which this very menu can switch off, leaving the
    // sidebar -- itself behind a toggle on a narrow layout -- as the only route.
    const { onChangeBranch } = renderMenu({ branchName: 'main' })

    expect(screen.getByTestId('composer-plus-branch')).toHaveTextContent('main')
    await fireEvent.click(screen.getByRole('menuitem', { name: /Change branch/, hidden: true }))
    expect(onChangeBranch).toHaveBeenCalledTimes(1)
  })

  it('omits the branch item when the agent reports no branch', () => {
    renderMenu()
    expect(screen.queryByTestId('composer-plus-branch')).toBeNull()
  })

  it('separates the settings axes from the session items', () => {
    // Above the rule: what the agent DOES. Below it: the session it works in.
    const { container } = renderMenu({
      groups: [group('model', 'Model', 10)],
      branchName: 'main',
    })

    const items = [...container.querySelectorAll('hr, [data-testid]')]
    const groupItem = items.findIndex(el => el.getAttribute('data-testid') === 'composer-group-model')
    const branchItem = items.findIndex(el => el.getAttribute('data-testid') === 'composer-plus-branch')

    expect(groupItem).toBeGreaterThan(-1)
    expect(branchItem).toBeGreaterThan(groupItem)
    // Exactly one rule between them — not zero, and not one on each side.
    expect(items.slice(groupItem + 1, branchItem).filter(el => el.tagName === 'HR')).toHaveLength(1)
  })

  it('leaves no stranded rule when either side of it is empty', () => {
    // Two adjacent rules read as a rendering bug. Neither an agent with no
    // option groups nor one with no branch and no info rows may produce one.
    const countAdjacentRules = (root: HTMLElement) => {
      const kids = [...root.querySelectorAll('hr')]
      return kids.filter(hr => hr.nextElementSibling?.tagName === 'HR').length
    }

    const noGroups = renderMenu({ branchName: 'main' })
    expect(countAdjacentRules(noGroups.container)).toBe(0)
    expect(screen.getByTestId('composer-plus-branch')).toBeInTheDocument()

    cleanup()
    const noSessionItems = renderMenu({ groups: [group('model', 'Model', 10)] })
    expect(countAdjacentRules(noSessionItems.container)).toBe(0)
    expect(screen.queryByTestId('composer-plus-branch')).toBeNull()

    // The case the two above miss: a fresh tab before its first status push has
    // NOTHING between the attach item and the view toggles, so both fencing
    // rules used to render back to back.
    cleanup()
    const bare = renderMenu()
    expect(countAdjacentRules(bare.container)).toBe(0)
    expect(bare.container.querySelectorAll('hr')).toHaveLength(0)
  })

  it('disables both branch actions when the Worker is unreachable, and says why', () => {
    const reason = 'This Worker is offline. Branch actions need the machine the repository is on.'
    const { onChangeBranch, onDeleteBranch } = renderMenu({ branchName: 'main', branchDisabledReason: reason })

    // The trigger stays ENABLED — the two items inside it are what the guard
    // disables — and carries the reason through Tooltip, which renders its text
    // lazily on hover and so is not assertable here. The contract this test
    // names is the two items.
    expect(screen.getByRole('menuitem', { name: /Change branch/, hidden: true })).toBeDisabled()
    expect(screen.getByRole('menuitem', { name: /Delete branch/, hidden: true })).toBeDisabled()
    expect(onChangeBranch).not.toHaveBeenCalled()
    expect(onDeleteBranch).not.toHaveBeenCalled()
  })

  it('reports each toggle state through aria-checked', () => {
    renderMenu()

    expect(screen.getByRole('menuitemcheckbox', { name: /Show status bar/, hidden: true }))
      .toHaveAttribute('aria-checked', 'true')
  })
})
