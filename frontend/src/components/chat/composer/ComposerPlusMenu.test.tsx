import type { AvailableOptionGroup } from '~/generated/proto/leapmux/v1/agent_pb'
import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { batch, createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { CODEX_BYPASS_SETTINGS } from '~/generated/contracts/codex-bypass'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { popoverCard } from '~/styles/popover.css'
import { stubBranchMenuActions } from '~/test-support/branchMenu'
import { hoverForTooltip } from '~/test-support/clipStub'
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
  isWorktree?: boolean
  directory?: string
  homeDir?: string
  branchStats?: { added: number, deleted: number, untracked: number }
  settingsDispatcher?: boolean
} = {}) {
  const onSettingChange = vi.fn()
  const onAttachFile = vi.fn()
  const onToggleEnterMode = vi.fn()
  const onToggleStatusBar = vi.fn()
  const branchActions = stubBranchMenuActions()
  const rendered = render(() => (
    <ComposerPlusMenu
      optionGroups={opts.groups ?? []}
      optionValues={opts.values ?? {}}
      agentProvider={opts.provider}
      onSettingChange={opts.settingsDispatcher === false ? undefined : onSettingChange}
      onAttachFile={onAttachFile}
      canAttach={opts.canAttach ?? true}
      disabledReason={opts.disabledReason}
      settingsLoading={opts.settingsLoading}
      workingTree={{
        isWorktree: opts.isWorktree ?? false,
        name: opts.branchName ?? '',
        directory: opts.directory ?? '',
        homeDir: opts.homeDir,
        stats: opts.branchStats,
      }}
      branchActions={branchActions}
      branchWorkerId="w-1"
      branchDisabledReason={opts.branchDisabledReason}
      enterKeyMode={() => 'cmd-enter-sends'}
      onToggleEnterMode={onToggleEnterMode}
      showStatusBar={() => true}
      onToggleStatusBar={onToggleStatusBar}
      agentInfo={opts.agentInfo ? () => <span data-testid="agent-info-rows" /> : undefined}
    />
  ))
  return { ...rendered, onSettingChange, onAttachFile, onToggleEnterMode, onToggleStatusBar, branchActions }
}

describe('composerPlusMenu structure freeze', () => {
  const rowIds = () => screen.getAllByRole('menuitem', { hidden: true })
    .concat(screen.getAllByRole('menuitemcheckbox', { hidden: true }))
    .map(el => el.getAttribute('data-testid'))

  function renderLive(sources: {
    groups: () => AvailableOptionGroup[]
    provider?: AgentProvider
    branch?: () => string | undefined
    isWorktree?: () => boolean
  }) {
    render(() => (
      <ComposerPlusMenu
        optionGroups={sources.groups()}
        optionValues={{}}
        agentProvider={sources.provider}
        onSettingChange={vi.fn()}
        onAttachFile={vi.fn()}
        canAttach
        workingTree={{
          isWorktree: sources.isWorktree?.() ?? false,
          name: sources.branch?.() ?? '',
          directory: '/repo',
        }}
        branchActions={stubBranchMenuActions()}
        branchWorkerId="w-1"
        enterKeyMode={() => 'cmd-enter-sends'}
        onToggleEnterMode={vi.fn()}
        showStatusBar={() => true}
        onToggleStatusBar={vi.fn()}
      />
    ))
  }

  /**
   * A push that lands while the menu is OPEN inserts rows ABOVE the two toggles
   * at the bottom -- so a pointer already aimed at "Send with Enter" lands on
   * whatever slid into its place, and one of those is a provider action that
   * applies a setting at once.
   *
   * The same hazard `OptionGroupPopover` freezes one level down.
   */
  it('does not move a row under the pointer when a push lands mid-open', async () => {
    const [groups, setGroups] = createSignal<AvailableOptionGroup[]>([group('model', 'Model', 1, ['sonnet'])])
    const [branch, setBranch] = createSignal<string | undefined>(undefined)
    renderLive({ groups, branch })

    await fireEvent.click(screen.getByTestId('composer-plus-trigger'))
    const before = rowIds()

    // A second axis AND a branch, both at once.
    batch(() => {
      setGroups([group('model', 'Model', 1, ['sonnet']), group('effort', 'Effort', 2, ['high'])])
      setBranch('feature/x')
    })
    await Promise.resolve()

    expect(rowIds(), 'the open menu keeps the shape the user aimed at').toEqual(before)
  })

  /**
   * The kind is frozen WITH the name, because it NAMES the destructive item
   * rather than merely labelling it.
   *
   * A push that flips `isWorktree` on a key that already carries a branch --
   * `stampBranchOnTabs` upserts a branch without one, and the git view defaults
   * it to false -- would otherwise rename "Delete branch..." to "Delete
   * worktree..." under a pointer already aimed at it. The user then clicks an
   * action that removes a whole directory after reading one that does not,
   * which is the same hazard as a row sliding into that place.
   */
  it('does not rename the delete item under the pointer when a push lands mid-open', async () => {
    const [groups] = createSignal<AvailableOptionGroup[]>([group('model', 'Model', 1, ['sonnet'])])
    const [isWorktree, setIsWorktree] = createSignal(false)
    renderLive({ groups, branch: () => 'feature', isWorktree })

    await fireEvent.click(screen.getByTestId('composer-plus-trigger'))
    expect(screen.getByRole('menuitem', { name: /Delete branch/, hidden: true })).toBeInTheDocument()

    setIsWorktree(true)
    await Promise.resolve()

    expect(screen.getByRole('menuitem', { name: /Delete branch/, hidden: true })).toBeInTheDocument()
    expect(screen.queryByRole('menuitem', { name: /Delete worktree/, hidden: true })).toBeNull()
    // The glyph is part of the same identity, so it holds still too.
    expect(screen.getByTestId('composer-plus-branch').querySelector('[data-testid="branch-icon"]'))
      .not
      .toBeNull()
  })

  // ...and the corrected kind is there the next time the menu opens.
  it('names the new kind the next time it opens', async () => {
    const [groups] = createSignal<AvailableOptionGroup[]>([group('model', 'Model', 1, ['sonnet'])])
    const [isWorktree, setIsWorktree] = createSignal(false)
    renderLive({ groups, branch: () => 'feature', isWorktree })

    await fireEvent.click(screen.getByTestId('composer-plus-trigger'))
    setIsWorktree(true)
    await Promise.resolve()

    await fireEvent.click(screen.getByTestId('composer-plus-trigger'))
    await fireEvent.click(screen.getByTestId('composer-plus-trigger'))
    await Promise.resolve()

    expect(screen.getByRole('menuitem', { name: /Delete worktree/, hidden: true })).toBeInTheDocument()
  })

  // ...and the freeze is released on close, so the next open is current.
  it('shows the new rows the next time it opens', async () => {
    const [groups, setGroups] = createSignal<AvailableOptionGroup[]>([group('model', 'Model', 1, ['sonnet'])])
    renderLive({ groups })

    await fireEvent.click(screen.getByTestId('composer-plus-trigger'))
    setGroups([group('model', 'Model', 1, ['sonnet']), group('effort', 'Effort', 2, ['high'])])
    await Promise.resolve()
    expect(screen.queryByTestId('composer-group-effort'), 'frozen while open').toBeNull()

    // Close, then open again: the freeze is released on close.
    await fireEvent.click(screen.getByTestId('composer-plus-trigger'))
    await fireEvent.click(screen.getByTestId('composer-plus-trigger'))
    await Promise.resolve()
    expect(screen.getByTestId('composer-group-effort')).toBeInTheDocument()
  })

  /**
   * The freeze holds ROWS still; an empty middle section has none, and freezing
   * on one stranded the menu. Every axis, the branch and the agent info arrive
   * together on the first push, so a menu opened before it held attach and the
   * two toggles until the user closed it and opened it again -- with nothing on
   * screen to say so, and no other settings surface once the status bar is off.
   */
  it('fills in the first push while it is open', async () => {
    const [groups, setGroups] = createSignal<AvailableOptionGroup[]>([])
    const [branch, setBranch] = createSignal<string | undefined>(undefined)
    renderLive({ groups, branch })

    await fireEvent.click(screen.getByTestId('composer-plus-trigger'))
    expect(screen.queryByTestId('composer-group-model'), 'no catalog yet').toBeNull()

    // ONE push, the way the worker's status lands: a single metadata patch
    // carrying the catalog and the branch together.
    batch(() => {
      setGroups([group('model', 'Model', 1, ['sonnet'])])
      setBranch('feature/x')
    })
    await Promise.resolve()

    expect(screen.getByTestId('composer-group-model')).toBeInTheDocument()
    expect(screen.getByTestId('composer-plus-branch')).toBeInTheDocument()
  })

  /** ...and the freeze engages on that same push, without a close in between. */
  it('holds the rows still from the push that created them', async () => {
    const [groups, setGroups] = createSignal<AvailableOptionGroup[]>([])
    renderLive({ groups })

    await fireEvent.click(screen.getByTestId('composer-plus-trigger'))
    setGroups([group('model', 'Model', 1, ['sonnet'])])
    await Promise.resolve()
    const filled = rowIds()

    setGroups([group('model', 'Model', 1, ['sonnet']), group('effort', 'Effort', 2, ['high'])])
    await Promise.resolve()

    expect(rowIds(), 'the second push waits for the next open').toEqual(filled)
  })

  it('disables a held permission action when its capability disappears', async () => {
    const [groups, setGroups] = createSignal<AvailableOptionGroup[]>([
      group('network_access', 'Network', 10, ['restricted', 'enabled']),
      group('sandbox_policy', 'Sandbox', 20, ['workspace-write', 'danger-full-access']),
      group('permissionMode', 'Approval', 30, ['on-request', 'never']),
    ])
    renderLive({ groups, provider: AgentProvider.CODEX })

    await fireEvent.click(screen.getByTestId('composer-plus-trigger'))
    const bypass = screen.getByTestId('composer-bypass-permissions')
    expect(bypass).toBeEnabled()

    setGroups([group('permissionMode', 'Approval', 30, ['on-request', 'never'])])
    await Promise.resolve()

    expect(bypass, 'the held row stays visible but cannot apply a stale preset').toBeDisabled()
  })
})

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

    await fireEvent.click(screen.getByTestId('composer-bypass-permissions'))

    expect(onSettingChange).toHaveBeenCalledTimes(1)
    const change = onSettingChange.mock.calls[0]![0] as { sets: Record<string, string> }
    expect(change).toEqual(CODEX_BYPASS_SETTINGS)
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

    const action = screen.getByTestId('composer-bypass-permissions')
    expect(action).toBeDisabled()
    expect(onSettingChange).not.toHaveBeenCalled()
  })

  it('shows smart immediately above bypass when both presets are usable', () => {
    const { container } = renderMenu({
      provider: AgentProvider.CLAUDE_CODE,
      groups: [group('permissionMode', 'Permission Mode', 10, ['default', 'auto', 'bypassPermissions'])],
    })

    const actions = [...container.querySelectorAll('[data-testid="composer-smart-permissions"], [data-testid="composer-bypass-permissions"]')]
    expect(actions.map(action => action.textContent)).toEqual(['Smart permissions', 'Bypass permissions'])
  })

  it('hides a permission action when one target group or value is unavailable', () => {
    renderMenu({
      provider: AgentProvider.CODEX,
      groups: [
        group('sandbox_policy', 'Sandbox', 20, ['workspace-write', 'danger-full-access']),
        group('permissionMode', 'Approval', 30, ['on-request', 'never']),
      ],
    })

    expect(screen.queryByTestId('composer-bypass-permissions')).toBeNull()

    cleanup()
    renderMenu({
      provider: AgentProvider.CODEX,
      groups: [
        group('network_access', 'Network', 10, ['restricted']),
        group('sandbox_policy', 'Sandbox', 20, ['workspace-write', 'danger-full-access']),
        group('permissionMode', 'Approval', 30, ['on-request', 'never']),
      ],
    })

    expect(screen.queryByTestId('composer-bypass-permissions')).toBeNull()
  })

  it('hides a permission action when a target group is read-only', () => {
    renderMenu({
      provider: AgentProvider.CLAUDE_CODE,
      groups: [{
        ...group('permissionMode', 'Permission Mode', 10, ['default', 'auto', 'bypassPermissions']),
        mutable: false,
      }],
    })

    expect(screen.queryByTestId('composer-smart-permissions')).toBeNull()
    expect(screen.queryByTestId('composer-bypass-permissions')).toBeNull()
  })

  it('disables Smart permissions when all target values are active', () => {
    renderMenu({
      provider: AgentProvider.CLAUDE_CODE,
      groups: [group('permissionMode', 'Permission Mode', 10, ['default', 'auto', 'bypassPermissions'])],
      values: { permissionMode: 'auto' },
    })

    expect(screen.getByTestId('composer-smart-permissions')).toBeDisabled()
  })

  it('keeps a multi-axis permission action enabled until every target is active', () => {
    renderMenu({
      provider: AgentProvider.CODEX,
      groups: [
        group('network_access', 'Network', 10, ['restricted', 'enabled']),
        group('sandbox_policy', 'Sandbox', 20, ['workspace-write', 'danger-full-access']),
        group('permissionMode', 'Approval', 30, ['on-request', 'never']),
      ],
      values: {
        network_access: 'enabled',
        sandbox_policy: 'danger-full-access',
        permissionMode: 'on-request',
      },
    })

    expect(screen.getByTestId('composer-bypass-permissions')).toBeEnabled()
  })

  it('disables permission actions when no settings dispatcher exists', () => {
    renderMenu({
      provider: AgentProvider.CLAUDE_CODE,
      groups: [group('permissionMode', 'Permission Mode', 10, ['default', 'auto', 'bypassPermissions'])],
      settingsDispatcher: false,
    })

    expect(screen.getByTestId('composer-smart-permissions')).toBeDisabled()
    expect(screen.getByTestId('composer-bypass-permissions')).toBeDisabled()
  })

  it('disables attach during a control request, and says why', () => {
    renderMenu({ canAttach: false })

    const attach = screen.getByTestId('composer-attach-file')
    expect(attach).toBeDisabled()
    expect(reasonOf(attach)).toBe('Attach is unavailable during a control request')
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

    expect(reasonOf(screen.getByTestId('composer-attach-file')))
      .toBe('This subagent doesn\'t accept messages.')
  })

  it('leaves attach live when no reason is given', () => {
    // The reason IS the disabled flag, so "dead with nothing to say" cannot be
    // expressed. The menu also applies no fallback of its own: AgentEditorPanel
    // resolves ONE reason and hands every surface the same resolved string, so a
    // default invented here could only be a second copy that drifts.
    renderMenu({})

    const attach = screen.getByTestId('composer-attach-file')
    expect(attach).toBeEnabled()
    expect(attach).not.toHaveAttribute('aria-describedby')
  })

  it('prefers the disabled reason over the control-request reason', () => {
    // A disabled composer during a control request is still disabled; the
    // narrower "unavailable during a control request" would understate it.
    renderMenu({ canAttach: false, disabledReason: 'No input here.' })

    expect(reasonOf(screen.getByTestId('composer-attach-file')))
      .toBe('No input here.')
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

  it('shows the agent-info rows on the shared card surface, not a menu', () => {
    // The same card also opens from the status bar's context-usage trigger, and
    // the two insets drifted apart while each call site set its own padding.
    // `popoverCard` is the single source both now use.
    //
    // The tag matters as much as the class: a `div` popover keeps a click on a
    // row, so the user can select the text in it, while a `menu` popover
    // dismisses on that click.
    renderMenu({ agentInfo: true })

    const popover = screen.getByTestId('composer-agent-info-popover')
    expect(popover.className).toBe(popoverCard)
    expect(popover.tagName).toBe('DIV')
  })

  it('keeps the agent-info card open when a row is clicked', async () => {
    renderMenu({ agentInfo: true })

    const popover = screen.getByTestId('composer-agent-info-popover')
    const hide = vi.spyOn(popover, 'hidePopover')

    await fireEvent.click(screen.getByTestId('composer-agent-info'))
    await fireEvent.click(screen.getByTestId('agent-info-rows'))
    expect(hide).not.toHaveBeenCalled()
  })

  it('marks an in-flight settings change on the trigger, and only while it is in flight', () => {
    // Every settings surface flips its label optimistically the moment the user
    // picks a value, so without this marker a pending change is indistinguishable
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
    const { branchActions } = renderMenu({ branchName: 'main' })

    expect(screen.getByTestId('composer-plus-branch')).toHaveTextContent('main')
    await fireEvent.click(screen.getByRole('menuitem', { name: /Switch to branch/, hidden: true }))
    expect(branchActions.onChangeBranch).toHaveBeenCalledTimes(1)
  })

  // The new-tab sections ride along for the same reason: with the status bar
  // off and the sidebar collapsed, this menu is the only route to them.
  it('carries the new-tab sections too', async () => {
    const { branchActions } = renderMenu({ branchName: 'main' })

    await fireEvent.click(screen.getByRole('menuitem', { name: /New agent\.\.\./, hidden: true }))
    expect(branchActions.onNewAgentAdvanced).toHaveBeenCalledTimes(1)
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
    // rules used to render one directly after the other.
    cleanup()
    const bare = renderMenu()
    expect(countAdjacentRules(bare.container)).toBe(0)
    expect(bare.container.querySelectorAll('hr')).toHaveLength(0)
  })

  it('disables every branch action when the Worker is unreachable, and says why', () => {
    const reason = 'This Worker is offline. Branch actions need the machine the repository is on.'
    const { branchActions } = renderMenu({ branchName: 'main', branchDisabledReason: reason })

    // The trigger stays ENABLED — the items inside it are what the guard
    // disables — and carries the reason through Tooltip, which renders its text
    // lazily on hover and so is not assertable here. The contract this test
    // specifies is the items.
    for (const name of [/Switch to branch/, /Create new branch/, /Create new worktree/, /Delete branch/, /New agent\.\.\./, /New terminal\.\.\./])
      expect(screen.getByRole('menuitem', { name, hidden: true })).toBeDisabled()
    expect(branchActions.onChangeBranch).not.toHaveBeenCalled()
    expect(branchActions.onDeleteBranch).not.toHaveBeenCalled()
  })

  it('reports each toggle state through aria-checked', () => {
    renderMenu()

    expect(screen.getByRole('menuitemcheckbox', { name: /Show status bar/, hidden: true }))
      .toHaveAttribute('aria-checked', 'true')
  })

  // This menu exists because the status bar is a preference the user can switch
  // off, so it has to name the checkout exactly as the status-bar chip does.
  describe('naming the checkout', () => {
    it('marks a worktree with the worktree glyph and renames the delete item', () => {
      renderMenu({ branchName: 'feature', isWorktree: true })

      const trigger = screen.getByTestId('composer-plus-branch')
      expect(trigger.querySelector('[data-testid="worktree-icon"]')).not.toBeNull()
      expect(screen.getByRole('menuitem', { name: /Delete worktree/, hidden: true })).toBeInTheDocument()
      expect(screen.queryByRole('menuitem', { name: /Delete branch/, hidden: true })).toBeNull()
    })

    it('marks a main-repo checkout with the branch glyph', () => {
      renderMenu({ branchName: 'main', isWorktree: false })

      const trigger = screen.getByTestId('composer-plus-branch')
      expect(trigger.querySelector('[data-testid="branch-icon"]')).not.toBeNull()
      expect(screen.getByRole('menuitem', { name: /Delete branch/, hidden: true })).toBeInTheDocument()
    })

    it('states the kind, the directory and the diff stats on hover', () => {
      vi.useFakeTimers()
      try {
        renderMenu({
          branchName: 'feature',
          isWorktree: true,
          directory: '/home/dev/repos/leapmux-worktrees/feature',
          homeDir: '/home/dev',
          branchStats: { added: 38, deleted: 12, untracked: 0 },
        })

        const tooltip = hoverForTooltip(screen.getByTestId('composer-plus-branch'))
        expect(tooltip).not.toBeNull()
        expect(tooltip!.textContent).toContain('Worktree')
        expect(tooltip!.textContent).toContain('~/repos/leapmux-worktrees/feature')
        expect(tooltip!.textContent).toContain('+38')
      }
      finally {
        vi.useRealTimers()
      }
    })

    it('replaces the rows with the reason when the actions are unusable', () => {
      vi.useFakeTimers()
      try {
        renderMenu({
          branchName: 'feature',
          isWorktree: true,
          directory: '/home/dev/repos/leapmux-worktrees/feature',
          branchDisabledReason: 'This Worker is offline.',
        })

        const tooltip = hoverForTooltip(screen.getByTestId('composer-plus-branch'))
        expect(tooltip!.textContent).toBe('This Worker is offline.')
        expect(tooltip!.querySelector('[data-testid="working-tree-rows"]')).toBeNull()
      }
      finally {
        vi.useRealTimers()
      }
    })
  })
})
