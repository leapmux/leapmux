import type { Component, JSX } from 'solid-js'
import type { BranchMenuActions } from './branchActions'
import type { RepoCheckout } from './repoCheckouts'
import type { ContextMenuTargetProps } from '~/components/common/DropdownMenu'
import { createSignal, Show } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { rowContextMenuTrigger } from '~/components/common/moreHorizontalTrigger'
import { NewTabMenuItems } from '~/components/common/NewTabMenuItems'
import { useAvailableProviders } from '~/hooks/useAvailableProviders'
import { useAvailableShells } from '~/hooks/useAvailableShells'
import { useExternalApps } from '~/hooks/useExternalApps'
import { RepositoryMenuItems } from './RepositoryMenuItems'
import { RepositoryTargetMenu } from './RepositoryTargetMenu'

interface RepoContextMenuProps extends ContextMenuTargetProps {
  /**
   * Every distinct checkout of this repository inside this workspace: the
   * main clone plus any linked worktree, on any Worker. One entry renders
   * flat; more than one gives each its own submenu.
   */
  checkouts: () => readonly RepoCheckout[]
  /** Bind the row menu's actions to one checkout. */
  actionsFor: (checkout: RepoCheckout) => BranchMenuActions | undefined
  /** Why one checkout's tab-creation items are unusable, if they are. */
  disabledReasonFor: (checkout: RepoCheckout) => string | undefined
  /** Collapse every branch row under this repository. */
  onCollapseAllBranches: () => void
  /** True while there is nothing left to collapse. */
  nothingToCollapse: () => boolean
  /**
   * Told when the menu opens, so the row can defer the checkout projection.
   * Forwarded rather than owned here for the same reason `SubMenu` forwards
   * it: the caller's work is scoped to THIS menu being open.
   */
  onToggle?: (open: boolean) => void
}

/**
 * Everything one checkout offers. Its own component so each checkout's
 * provider and shell lists are fetched separately: they belong to the Worker
 * that checkout is on, and a repository can span two machines.
 */
const CheckoutActions: Component<{
  checkout: RepoCheckout
  actions: BranchMenuActions | undefined
  disabledReason: string | undefined
  menuOpen: () => boolean
}> = (props) => {
  // Fetched only while the menu is open, like the branch row's. One of these
  // mounts per repository row of every workspace, so a mount-time fetch would
  // scan every Worker in the fleet to fill menus nobody opened.
  const listSource = () =>
    props.menuOpen() && props.checkout.workerId ? { workerId: props.checkout.workerId } : null
  const { providers } = useAvailableProviders(listSource)
  const { shells, defaultShell } = useAvailableShells(listSource)
  const apps = useExternalApps(() => props.menuOpen() && props.checkout.isLocal)

  return (
    <>
      <Show when={props.actions}>
        {actions => (
          <>
            <NewTabMenuItems
              availableProviders={providers()}
              availableShells={shells()}
              defaultShell={defaultShell()}
              disabledReason={props.disabledReason}
              onNewAgent={provider => actions().onNewAgent(provider)}
              onNewAgentAdvanced={() => actions().onNewAgentAdvanced()}
              onNewTerminalWithShell={shell => actions().onNewTerminalWithShell(shell)}
              onNewTerminalAdvanced={() => actions().onNewTerminalAdvanced()}
            />
            <hr />
          </>
        )}
      </Show>
      {/* `disabledReason` deliberately does NOT reach this block: every item
          copies text the browser already holds or acts on THIS machine. */}
      <RepositoryMenuItems
        checkout={() => props.checkout}
        apps={apps}
        testIdPrefix="repo-repository"
      />
    </>
  )
}

/**
 * The repository row's menu, and the same menu on a right-click of the row.
 *
 * The row had no menu at all before, so a user who wanted a repository's path
 * or wanted to open it had to find a branch under it first. Its sections match
 * the branch row's -- Agents, Terminals, Repository -- because a repository
 * with one checkout and the branch inside it offer the same things, and two
 * shapes for that would be two shapes to learn.
 *
 * A repository GROUP is keyed by repo identity, so it can span several
 * checkouts and even several Workers. With more than one, each checkout opens
 * a submenu holding exactly what the flat shape holds.
 */
export const RepoContextMenu: Component<RepoContextMenuProps> = (props) => {
  const [menuOpen, setMenuOpen] = createSignal(false)

  const checkoutActions = (checkout: RepoCheckout): JSX.Element => (
    <CheckoutActions
      checkout={checkout}
      actions={props.actionsFor(checkout)}
      disabledReason={props.disabledReasonFor(checkout)}
      menuOpen={menuOpen}
    />
  )

  return (
    <DropdownMenu
      trigger={rowContextMenuTrigger({ 'data-testid': 'repo-row-menu-trigger' })}
      contextMenuFor={props.contextMenuFor}
      onToggle={(open) => {
        setMenuOpen(open)
        props.onToggle?.(open)
      }}
      data-testid="repo-context-menu"
    >
      <RepositoryTargetMenu
        targets={props.checkouts}
        labelOf={checkout => checkout.label}
        header="Checkouts"
        testIdPrefix="repo-checkout"
      >
        {checkoutActions}
      </RepositoryTargetMenu>

      <hr />
      {/* Distinct from the row's own click, which toggles the repository
          group itself. This one folds the branch rows inside it, and goes
          dim once there is nothing left for it to fold. */}
      <button
        type="button"
        role="menuitem"
        disabled={props.nothingToCollapse()}
        onClick={() => props.onCollapseAllBranches()}
        data-testid="repo-collapse-branches"
      >
        Collapse all branches
      </button>
    </DropdownMenu>
  )
}
