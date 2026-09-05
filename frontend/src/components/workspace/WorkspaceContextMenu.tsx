import type { Component } from 'solid-js'
import type { RepoStartPoint } from './repoStartPoints'
import type { WorkspaceStartActions, WorkspaceStartAt } from './workspaceStartActions'
import type { ContextMenuTargetProps } from '~/components/common/DropdownMenu'
import type { Section } from '~/generated/proto/leapmux/v1/section_pb'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { RepoGitStore } from '~/stores/repoGit'
import type { Tab } from '~/stores/tab.types'
import { createMemo, createSignal, For, Show } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { MenuInfoButton } from '~/components/common/MenuInfoRows'
import { rowContextMenuTrigger } from '~/components/common/moreHorizontalTrigger'
import { SubMenu } from '~/components/common/SubMenu'
import { isMoveTargetSection } from '~/components/shell/sectionUtils'
import { dangerMenuItem } from '~/styles/shared.css'
import { listRepoStartPoints } from './repoStartPoints'
import { workspaceInfoJson, workspaceInfoRows } from './workspaceMenuInfo'
import { menuItem } from './workspaceMenuItem'
import { WorkspaceRepositoryMenu } from './WorkspaceRepositoryMenu'
import { WorkspaceStartMenuItems } from './WorkspaceStartMenuItems'

interface WorkspaceContextMenuProps extends ContextMenuTargetProps {
  workspaceId: string
  workspaceTitle: string
  /** The section the row sits in, for the info block. */
  sectionName: string
  isArchived: boolean
  sections: Section[]
  currentSectionId: string | undefined
  /** This workspace's tabs. */
  getTabs: () => Tab[]
  repoGitStore: RepoGitStore
  workerInfoFn?: (id: string) => WorkerInfo | null
  isWorkerOnline?: (workerId: string) => boolean
  /** Whether a worker runs on THIS machine. See `~/lib/workerLocality`. */
  isLocalWorkerFn?: (workerId: string) => boolean
  /** Open a new agent / terminal at one of this workspace's checkouts. */
  startActions?: WorkspaceStartActions
  onRename: () => void
  onMoveTo: (sectionId: string) => void
  onArchive: () => void
  onUnarchive: () => void
  onDelete: () => void
}

/**
 * One workspace row's menu, and the same menu on a right-click of the row.
 *
 * It composes rather than owning: the info projection is pure
 * (`workspaceMenuInfo`), the tab-creation shapes are
 * {@link WorkspaceStartMenuItems}, and the repository actions -- with the
 * editor probe that only they use -- are {@link WorkspaceRepositoryMenu}. What
 * stays here is the menu's own shape: which blocks appear, in what order, and
 * which of them an ARCHIVED workspace keeps.
 */
export const WorkspaceContextMenu: Component<WorkspaceContextMenuProps> = (props) => {
  // Deferred for the same reason `BranchContextMenu` defers its two worker
  // lists, with more force: one of these mounts per workspace ROW, and it
  // serves the right-click menu as well.
  const [menuOpen, setMenuOpen] = createSignal(false)

  /** Every repository this workspace has a checkout in. */
  const repos = createMemo((): RepoStartPoint[] => {
    if (!menuOpen())
      return []
    return listRepoStartPoints(props.getTabs(), props.repoGitStore, {
      workerInfoFn: props.workerInfoFn,
    })
  })

  /**
   * The repositories a new tab can actually be opened in.
   *
   * Filtered by worker liveness, unlike {@link repos}: opening an agent needs
   * the machine the repository is on. The unfiltered list still backs the
   * `Repository` submenu, whose items copy a URL the store already holds and
   * open local paths -- neither of which needs the remote worker.
   */
  const startableRepos = createMemo((): RepoStartPoint[] => {
    const online = props.isWorkerOnline
    return online ? repos().filter(r => online(r.startPoint.workerId)) : repos()
  })

  const isLocal = (workerId: string) => props.isLocalWorkerFn?.(workerId) ?? false

  const info = () => ({
    workspaceId: props.workspaceId,
    title: props.workspaceTitle,
    sectionName: props.sectionName,
    tabCount: props.getTabs().length,
    repos: repos(),
  })

  // The memo, not a `<Show>`: `DropdownMenu` renders children eagerly and a
  // `createMemo` runs its body at setup, so an ungated builder would walk every
  // tab of every workspace on every reactive tick.
  const infoRows = createMemo(() => (menuOpen() ? workspaceInfoRows(info()) : []))

  const moveTargets = () => props.sections.filter(
    s => s.id !== props.currentSectionId && isMoveTargetSection(s.sectionType),
  )

  const startAt = (repo: RepoStartPoint): WorkspaceStartAt => ({
    workspaceId: props.workspaceId,
    workerId: repo.startPoint.workerId,
    workingDir: repo.startPoint.gitToplevel,
  })

  /**
   * The no-target start: this workspace, and no checkout.
   *
   * An empty worker and directory mean "follow the current tab context", which
   * the handler resolves AFTER switching to this workspace -- so the dialog
   * opens against this workspace even though the row that asked was not the
   * active one.
   */
  const startAtWorkspace = (): WorkspaceStartAt => ({
    workspaceId: props.workspaceId,
    workerId: '',
    workingDir: '',
  })

  return (
    <DropdownMenu
      trigger={rowContextMenuTrigger({ 'data-testid': 'workspace-row-menu-trigger' })}
      contextMenuFor={props.contextMenuFor}
      onToggle={setMenuOpen}
      data-testid="workspace-context-menu"
    >
      <MenuInfoButton
        rows={infoRows()}
        copyText={() => workspaceInfoJson(info())}
        toastMessage="Workspace info copied to clipboard"
        data-testid="workspace-info-button"
      />
      <Show when={infoRows().length > 0}>
        <hr />
      </Show>

      {/* Absent when archived. `isWorkspaceMutatable` says archival is the one
          thing that blocks mutation, and every other surface already obeys it:
          the tab bar drops its `+`, and the branch row hides its whole menu. A
          route that survives here would be the one way in that every other
          surface forbids. */}
      <Show when={!props.isArchived}>
        <WorkspaceStartMenuItems
          verb="New agent"
          repos={repos}
          startableRepos={startableRepos}
          run={at => props.startActions?.onNewAgentAt(at)}
          startAtWorkspace={startAtWorkspace}
          startAt={startAt}
          data-testid="workspace-new-agent"
        />
        <WorkspaceStartMenuItems
          verb="New terminal"
          repos={repos}
          startableRepos={startableRepos}
          run={at => props.startActions?.onNewTerminalAt(at)}
          startAtWorkspace={startAtWorkspace}
          startAt={startAt}
          data-testid="workspace-new-terminal"
        />
      </Show>

      {/* Copying a URL, revealing a directory and opening an editor mutate
          nothing, so this stays for an archived workspace. */}
      <WorkspaceRepositoryMenu
        repos={repos}
        repoGitStore={props.repoGitStore}
        isLocal={isLocal}
        menuOpen={menuOpen}
      />

      <hr />

      {/* Renaming an archived workspace is a MUTATION, and the tab bar already
          routes the sibling operation through `canRenameTab(archived, tab)`.
          This row was the one surface ignoring the rule its own predicate
          states. */}
      <Show when={!props.isArchived}>
        {menuItem('Rename', () => props.onRename())}
      </Show>

      {/* KEPT while archived, and it is the only way to unarchive into a
          SPECIFIC custom section, because `Unarchive` below always targets In
          progress. The hub REFUSES a `MoveWorkspace` that crosses the archive
          boundary (archival stops processes, so it cannot ride on a generic
          reorder), so `onMoveTo` routes a crossing through
          `SetWorkspaceArchiveState`, which carries the destination -- see
          `useWorkspaceOperations.moveWorkspace`. `isMoveTargetSection` excludes
          Archived as a target, so for an archived workspace every other
          workspace section is legal -- the list is non-empty exactly when it is
          useful. */}
      <Show when={moveTargets().length > 0}>
        <SubMenu label="Move to" data-testid="workspace-move-to" popoverTestId="workspace-move-to-popover">
          <For each={moveTargets()}>
            {section => menuItem(section.name, () => props.onMoveTo(section.id))}
          </For>
        </SubMenu>
      </Show>

      <Show when={!props.isArchived}>
        {menuItem('Archive', () => props.onArchive())}
      </Show>

      <Show when={props.isArchived}>
        {menuItem('Unarchive', () => props.onUnarchive())}
      </Show>

      <hr />
      <button type="button" role="menuitem" class={dangerMenuItem} onClick={() => props.onDelete()}>
        Delete
      </button>
    </DropdownMenu>
  )
}
