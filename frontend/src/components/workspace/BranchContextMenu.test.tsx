import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { GitMode } from '~/hooks/useGitModeState'
import { stubBranchMenuActions } from '~/test-support/branchMenu'
import { withPreferences } from '~/test-support/preferencesProvider'
import { BranchContextMenu } from './BranchContextMenu'

// Children contract: BranchContextMenu wraps a DropdownMenu with the three
// change items, the delete item, and the shared new-tab sections. DropdownMenu
// owns the popover open/close lifecycle; each item click closes the menu
// via DropdownMenu's own onClick → hidePopover wiring.

vi.mock('~/api/workerRpc', () => ({
  listAvailableProviders: vi.fn(async () => ({ providers: [AgentProvider.CLAUDE_CODE, AgentProvider.CODEX] })),
  listAvailableShells: vi.fn(async () => ({ shells: ['/bin/zsh', '/bin/bash'], defaultShell: '/bin/zsh' })),
}))

const listProviders = workerRpc.listAvailableProviders as unknown as ReturnType<typeof vi.fn>
const listShells = workerRpc.listAvailableShells as unknown as ReturnType<typeof vi.fn>

// The Popover API stubs come from `vitest.setup.ts`, and this suite needs THOSE
// rather than bare `vi.fn()` replacements: they dispatch the `toggle` event and
// answer `:popover-open`, which is how the menu learns that it opened -- and the
// two worker-list fetches below depend on exactly that.

beforeEach(() => {
  vi.clearAllMocks()
})

function renderMenu(disabledReason?: string, isWorktree = false) {
  const actions = stubBranchMenuActions()
  const result = render(() => (
    <BranchContextMenu
      isWorktree={isWorktree}
      workerId="w-1"
      actions={actions}
      disabledReason={disabledReason}
    />
  ))
  // Before the menu opens, the only button rendered is the trigger.
  const trigger = screen.getByRole('button')
  return { actions, trigger, ...result }
}

/**
 * The `<button>` of the item labelled `label`.
 *
 * Not `getByText` alone: the two dialog items wrap their label in
 * `DropdownMenuItemContent`'s clipping span, and a shell item wraps its path in
 * `<code>`, so the text node's own element is not the control.
 */
function menuItem(label: string): HTMLElement {
  const el = screen.getByText(label).closest('button')
  expect(el).not.toBeNull()
  return el!
}

/** Open the menu and let the two on-open list fetches settle. */
/**
 * Click one item, reopening the menu first when a previous click dismissed it.
 *
 * The items mount only while the menu is open, so a test that picks several in
 * a row has to reopen between them -- which is what a user does too.
 */
async function clickItem(trigger: HTMLElement, label: string) {
  if (!screen.queryByText(label))
    await fireEvent.click(trigger)
  await fireEvent.click(screen.getByText(label))
}

async function openMenu(trigger: HTMLElement) {
  await fireEvent.click(trigger)
  // Both list hooks resolve on a microtask; flush it so the provider row and
  // the shell items are in the DOM before the assertions run.
  await Promise.resolve()
  await Promise.resolve()
}

describe('branchContextMenu', () => {
  // One item per git mode, so the label the user picks states the radio they
  // then see. A single "Change branch..." made the mode invisible until the
  // dialog opened.
  describe('the three change items', () => {
    const cases = [
      ['Switch to branch...', GitMode.SwitchBranch],
      ['Create new branch...', GitMode.CreateBranch],
      ['Create new worktree...', GitMode.CreateWorktree],
    ] as const

    for (const [label, mode] of cases) {
      it(`fires onChangeBranch with its own mode from ${label}`, async () => {
        const { actions, trigger } = renderMenu()
        await fireEvent.click(trigger)
        await fireEvent.click(screen.getByText(label))
        expect(actions.onChangeBranch).toHaveBeenCalledTimes(1)
        expect(actions.onChangeBranch).toHaveBeenCalledWith(mode)
        expect(actions.onDeleteBranch).not.toHaveBeenCalled()
      })
    }

    it('offers no undifferentiated "Change branch..." item', async () => {
      const { trigger } = renderMenu()
      await fireEvent.click(trigger)
      expect(screen.queryByText('Change branch...')).toBeNull()
    })
  })

  it('fires onDeleteBranch when Delete branch... is clicked', async () => {
    const { actions, trigger } = renderMenu()
    await fireEvent.click(trigger)
    await fireEvent.click(screen.getByText('Delete branch...'))
    expect(actions.onDeleteBranch).toHaveBeenCalledTimes(1)
    expect(actions.onChangeBranch).not.toHaveBeenCalled()
  })

  // The DELETE item states what it destroys. On a worktree row it removes a
  // whole directory, and "Delete branch..." there is how a user destroys a
  // directory they meant to keep. The CHANGE items keep their names on both,
  // because a worktree has a branch checked out and the dialog still changes
  // that branch.
  describe('on a worktree row', () => {
    it('labels the delete item after the worktree', async () => {
      const { trigger } = renderMenu(undefined, true)
      await fireEvent.click(trigger)

      expect(screen.getByText('Delete worktree...')).toBeInTheDocument()
      expect(screen.queryByText('Delete branch...')).toBeNull()
      expect(screen.getByText('Switch to branch...')).toBeInTheDocument()
      expect(screen.getByText('Create new worktree...')).toBeInTheDocument()
    })

    it('fires onDeleteBranch from the renamed item', async () => {
      const { actions, trigger } = renderMenu(undefined, true)
      await fireEvent.click(trigger)
      await fireEvent.click(screen.getByText('Delete worktree...'))

      expect(actions.onDeleteBranch).toHaveBeenCalledTimes(1)
      expect(actions.onChangeBranch).not.toHaveBeenCalled()
    })

    it('still disables every item when the worker is offline', async () => {
      const { trigger } = renderMenu('Worker "mac-mini" is offline', true)
      await openMenu(trigger)

      for (const label of ['Switch to branch...', 'Create new branch...', 'Create new worktree...', 'Delete worktree...', 'New agent...', 'New terminal...'])
        expect(menuItem(label)).toBeDisabled()
    })
  })

  // The new-tab sections open an agent or a terminal on THIS branch's checkout.
  // They are the same block the tab bar renders, so the two menus cannot offer
  // different actions.
  describe('the Agents and Terminals sections', () => {
    it('asks for nothing until the menu opens', () => {
      renderMenu()
      expect(listProviders).not.toHaveBeenCalled()
      expect(listShells).not.toHaveBeenCalled()
    })

    it('lists the BRANCH worker\'s providers and shells once it opens', async () => {
      const { trigger } = renderMenu()
      await openMenu(trigger)

      expect(listProviders).toHaveBeenCalledWith('w-1', expect.anything())
      expect(listShells).toHaveBeenCalledWith('w-1', { workerId: 'w-1' })
      expect(screen.getByTestId(`menu-new-agent-${AgentProvider.CLAUDE_CODE}`)).toBeInTheDocument()
      expect(screen.getByText('/bin/zsh')).toBeInTheDocument()
      expect(screen.getByText('/bin/bash')).toBeInTheDocument()
    })

    // The two halves of this menu do different kinds of thing -- one changes
    // branch state, the other opens a tab -- so a rule divides them. Asserted
    // on the ORDER rather than on a count of `<hr>`, because the block below
    // carries a rule of its own between Agents and Terminals.
    it('divides the branch actions from the new-tab sections with one rule', async () => {
      const { trigger } = renderMenu()
      await openMenu(trigger)

      const header = screen.getByText('Agents')
      const rows = Array.from(header.parentElement!.children)
      const deleteIdx = rows.findIndex(el => el.contains(menuItem('Delete branch...')))
      const headerIdx = rows.indexOf(header)
      expect(deleteIdx).toBeGreaterThanOrEqual(0)
      expect(rows.slice(deleteIdx + 1, headerIdx).map(el => el.tagName)).toEqual(['HR'])
    })

    it('opens the clicked provider immediately', async () => {
      const { actions, trigger } = renderMenu()
      await openMenu(trigger)
      await fireEvent.click(screen.getByTestId(`menu-new-agent-${AgentProvider.CODEX}`))

      expect(actions.onNewAgent).toHaveBeenCalledTimes(1)
      expect(actions.onNewAgent).toHaveBeenCalledWith(AgentProvider.CODEX)
      expect(actions.onNewAgentAdvanced).not.toHaveBeenCalled()
    })

    it('opens the clicked shell immediately', async () => {
      const { actions, trigger } = renderMenu()
      await openMenu(trigger)
      await fireEvent.click(screen.getByText('/bin/bash'))

      expect(actions.onNewTerminalWithShell).toHaveBeenCalledTimes(1)
      expect(actions.onNewTerminalWithShell).toHaveBeenCalledWith('/bin/bash')
    })

    it('routes the two dialog items to their own actions', async () => {
      const { actions, trigger } = renderMenu()
      await openMenu(trigger)
      await clickItem(trigger, 'New agent...')
      await clickItem(trigger, 'New terminal...')

      expect(actions.onNewAgentAdvanced).toHaveBeenCalledTimes(1)
      expect(actions.onNewTerminalAdvanced).toHaveBeenCalledTimes(1)
    })

    // The shortcut opens a dialog for the CURRENT tab context, not for this
    // branch, so stating a key here would state one that does something else.
    it('shows no keyboard-shortcut hint on the dialog items', async () => {
      const { trigger } = renderMenu()
      await openMenu(trigger)

      const item = screen.getByText('New agent...').closest('button')
      expect(item?.textContent).toBe('New agent...')
    })
  })

  // The offline restriction. Every item stays VISIBLE and disabled rather than
  // disappearing, because a row that silently loses its menu reads as a bug while
  // a dimmed item that states a reason says which machine to bring back.
  //
  // Native `disabled` is deliberate: the dimming and not-allowed cursor come from
  // oat's global `:disabled` rule, which every other menu in the app relies on.
  // The cost is that a disabled button leaves the focus order, so the tooltip
  // opens only under a pointer -- see the note on disabledReason.
  describe('when the worker is offline', () => {
    const reason = 'Worker "mac-mini" is offline'

    it('disables every item and describes it with the reason', async () => {
      const { trigger } = renderMenu(reason)
      await openMenu(trigger)
      for (const label of ['Switch to branch...', 'Create new branch...', 'Create new worktree...', 'Delete branch...', 'New agent...', 'New terminal...', '/bin/zsh']) {
        const item = menuItem(label)
        expect(item).toBeDisabled()
        // Through the Tooltip's offscreen description, not `title`. A reason
        // this long on `title` BECAME the item's accessible name, so a screen
        // reader announced it in place of "Switch to branch...".
        expect(item).not.toHaveAttribute('title')
        const describedBy = item.getAttribute('aria-describedby')
        expect(describedBy).toBeTruthy()
        expect(document.getElementById(describedBy!)?.textContent).toBe(reason)
      }
    })

    it('disables the provider glyphs too', async () => {
      const { trigger } = renderMenu(reason)
      await openMenu(trigger)
      expect(screen.getByTestId(`menu-new-agent-${AgentProvider.CLAUDE_CODE}`)).toBeDisabled()
    })

    it('fires no action while disabled', async () => {
      const { actions, trigger } = renderMenu(reason)
      await openMenu(trigger)
      await clickItem(trigger, 'Switch to branch...')
      await clickItem(trigger, 'Delete branch...')
      await clickItem(trigger, 'New agent...')
      if (!screen.queryByTestId(`menu-new-agent-${AgentProvider.CODEX}`))
        await fireEvent.click(trigger)
      await fireEvent.click(screen.getByTestId(`menu-new-agent-${AgentProvider.CODEX}`))
      expect(actions.onChangeBranch).not.toHaveBeenCalled()
      expect(actions.onDeleteBranch).not.toHaveBeenCalled()
      expect(actions.onNewAgentAdvanced).not.toHaveBeenCalled()
      expect(actions.onNewAgent).not.toHaveBeenCalled()
    })

    it('leaves every item enabled when no reason is given', async () => {
      const { trigger } = renderMenu()
      await openMenu(trigger)
      for (const label of ['Switch to branch...', 'Create new branch...', 'Create new worktree...', 'Delete branch...', 'New agent...', 'New terminal...'])
        expect(menuItem(label)).toBeEnabled()
    })
  })

  describe('with a custom trigger', () => {
    it('renders the custom trigger instead of the kebab', () => {
      render(() => (
        <BranchContextMenu
          isWorktree={false}
          workerId="w-1"
          actions={stubBranchMenuActions()}
          trigger={triggerProps => (
            <button data-testid="custom-trigger" {...triggerProps}>
              main
            </button>
          )}
        />
      ))

      // The custom trigger renders, not the kebab.
      expect(screen.getByTestId('custom-trigger')).toBeInTheDocument()
      expect(screen.getByText('main')).toBeInTheDocument()
    })

    it('fires onChangeBranch via the custom trigger opening the menu', async () => {
      const actions = stubBranchMenuActions()
      render(() => (
        <BranchContextMenu
          isWorktree={false}
          workerId="w-1"
          actions={actions}
          trigger={triggerProps => (
            <button data-testid="custom-trigger" {...triggerProps}>
              main
            </button>
          )}
        />
      ))

      await fireEvent.click(screen.getByTestId('custom-trigger'))
      await fireEvent.click(screen.getByText('Switch to branch...'))
      expect(actions.onChangeBranch).toHaveBeenCalledWith(GitMode.SwitchBranch)
    })
  })
})

// The `Repository` section acts on the checkout the branch sits in. It is the
// same block the workspace row menu and the repository row menu render, so a
// user who learns it once has learned all three.
describe('branchContextMenu repository section', () => {
  function renderWithRepository(overrides: Partial<{ isLocal: boolean, originUrl: string, disabledReason: string }> = {}) {
    const actions = stubBranchMenuActions()
    const result = render(withPreferences(() => (
      <BranchContextMenu
        isWorktree={false}
        workerId="w-1"
        actions={actions}
        disabledReason={overrides.disabledReason}
        repository={() => ({
          gitToplevel: '/home/me/leapmux',
          originUrl: overrides.originUrl ?? 'https://example.com/o/r.git',
          isLocal: overrides.isLocal ?? false,
        })}
      />
    )))
    return { ...result, trigger: screen.getByRole('button') }
  }

  it('is absent until the menu opens', () => {
    renderWithRepository()

    expect(screen.queryByText('Repository')).not.toBeInTheDocument()
  })

  it('appears AFTER the Terminals section, so nothing above it moved', async () => {
    const { trigger } = renderWithRepository()
    await openMenu(trigger)

    const headers = screen.getAllByText(/^(?:Agents|Terminals|Repository)$/).map(el => el.textContent)
    expect(headers).toEqual(['Agents', 'Terminals', 'Repository'])
  })

  // Every item of it either copies text the browser already holds or acts on
  // THIS machine, so an offline Worker leaves all of it usable while it
  // disables everything above.
  it('stays usable while an offline Worker disables the rest', async () => {
    const { trigger } = renderWithRepository({ disabledReason: 'Worker "mac-mini" is offline' })
    await openMenu(trigger)

    expect(menuItem('New agent...')).toBeDisabled()
    expect(menuItem('Copy repository path')).not.toBeDisabled()
    expect(menuItem('Copy repository URL')).not.toBeDisabled()
  })

  it('offers the local-only rows only for a checkout on THIS machine', async () => {
    const { trigger, unmount } = renderWithRepository({ isLocal: false })
    await openMenu(trigger)
    expect(screen.queryByText('Reveal in file manager')).not.toBeInTheDocument()
    unmount()

    const local = renderWithRepository({ isLocal: true })
    await openMenu(local.trigger)
    expect(screen.getByText('Reveal in file manager')).toBeInTheDocument()
  })

  // The two composer surfaces render this menu with a branch and a Worker but
  // no repository, and neither should have to stand up a preference store to
  // show a branch menu.
  it('renders no section at all when no repository is supplied', async () => {
    const actions = stubBranchMenuActions()
    render(() => (
      <BranchContextMenu isWorktree={false} workerId="w-1" actions={actions} />
    ))
    await openMenu(screen.getByRole('button'))

    expect(screen.queryByText('Repository')).not.toBeInTheDocument()
    expect(screen.getByText('Agents')).toBeInTheDocument()
  })
})
