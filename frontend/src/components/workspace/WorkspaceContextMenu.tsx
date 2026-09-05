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
import { Tooltip } from '~/components/common/Tooltip'
import { isMoveTargetSection } from '~/components/shell/sectionUtils'
import { useExternalApps } from '~/hooks/useExternalApps'
import { repoGitView } from '~/stores/repoGit'
import { dangerMenuItem } from '~/styles/shared.css'
import { RepositoryMenuItems } from './RepositoryMenuItems'
import { RepositoryTargetMenu } from './RepositoryTargetMenu'
import { listRepoStartPoints } from './repoStartPoints'
import { workspaceInfoJson, workspaceInfoRows } from './workspaceMenuInfo'
import { menuItem } from './workspaceMenuItem'

/**
 * Why a repository's tab-creation items are unusable.
 *
 * Per repository, not per workspace: each one sits on its own worker, and the
 * menu now names the repository before the action, so it can say which machine
 * is missing instead of one sentence about all of them.
 */
const REPO_WORKER_OFFLINE_REASON
  = 'This machine is offline. Opening a tab needs the machine the repository is on.'

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
 * (`workspaceMenuInfo`), the repository actions are
 * {@link RepositoryMenuItems}, and the "which repository" shape is
 * {@link RepositoryTargetMenu}. What stays here is the menu's own shape:
 * which blocks appear, in what order, and which of them an ARCHIVED workspace
 * keeps.
 *
 * The repository comes FIRST and the actions hang off it. The previous shape
 * asked for the action first -- `New agent in ▸`, `New terminal in ▸`,
 * `Repository ▸` -- which scattered one repository's actions across three
 * submenus, and let an offline repository appear in one of them and not the
 * others.
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

  const isLocal = (workerId: string) => props.isLocalWorkerFn?.(workerId) ?? false
  const isOnline = (workerId: string) => props.isWorkerOnline?.(workerId) ?? true

  // Probed on open, and only where a LOCAL checkout could use one. The
  // detection cache is module-wide, so a second row's menu pays nothing.
  const anyLocal = () => repos().some(r => isLocal(r.startPoint.workerId))
  const apps = useExternalApps(() => menuOpen() && anyLocal())

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
   *
   * Rendered ONLY for a workspace with no repository at all. A workspace that
   * has one must never fall back to it: "follow the current tab context" would
   * start an agent on a machine the user never picked.
   */
  const startAtWorkspace = (): WorkspaceStartAt => ({
    workspaceId: props.workspaceId,
    workerId: '',
    workingDir: '',
  })

  /**
   * One repository's actions: the two tab-creation items, then the shared
   * repository block.
   *
   * The tab-creation items are DISABLED rather than hidden when the machine is
   * unreachable, and only they are: the repository block below either copies
   * text the browser already holds or acts on this machine, so an offline
   * worker leaves all of it usable.
   */
  const repoActions = (repo: RepoStartPoint) => {
    const at = () => startAt(repo)
    const reason = () => (isOnline(repo.startPoint.workerId) ? undefined : REPO_WORKER_OFFLINE_REASON)
    // The FLAT shape names the repository in the item, because nothing else on
    // screen does. Inside a submenu the trigger already carries that name, and
    // repeating it there would read as a second repository.
    const startLabel = (verb: string) =>
      (repos().length > 1 ? `${verb}...` : `${verb} in ${repo.label}...`)
    const startItem = (label: string, run: (at: WorkspaceStartAt) => void) => (
      // The reason goes through <Tooltip>, which works on a disabled control
      // and leaves the item its own name. A `title` this long BECOMES the
      // accessible name instead.
      <Tooltip text={reason()}>
        <button type="button" role="menuitem" disabled={Boolean(reason())} onClick={() => run(at())}>
          {label}
        </button>
      </Tooltip>
    )
    return (
      <>
        {/* Absent when archived. `isWorkspaceMutatable` says archival is the
            one thing that blocks mutation, and every other surface already
            obeys it: the tab bar drops its `+`, and the branch row hides its
            whole menu. A route that survives here would be the one way in
            that every other surface forbids. */}
        <Show when={!props.isArchived}>
          {startItem(startLabel('New agent'), a => props.startActions?.onNewAgentAt(a))}
          {startItem(startLabel('New terminal'), a => props.startActions?.onNewTerminalAt(a))}
          <hr />
        </Show>
        <RepositoryMenuItems
          checkout={() => ({
            gitToplevel: repo.startPoint.gitToplevel,
            originUrl: repoGitView(
              { workerId: repo.startPoint.workerId, gitToplevel: repo.startPoint.gitToplevel },
              props.repoGitStore,
            ).originUrl ?? '',
            isLocal: isLocal(repo.startPoint.workerId),
          })}
          apps={apps}
          testIdPrefix="workspace-repository"
        />
      </>
    )
  }

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

      {/* A workspace with no checkout yet -- a freshly created one has no tabs
          -- is exactly the row that most needs a way in, so the two items
          still open with no target. */}
      <Show when={repos().length === 0 && !props.isArchived}>
        {menuItem('New agent...', () => props.startActions?.onNewAgentAt(startAtWorkspace()), 'workspace-new-agent')}
        {menuItem('New terminal...', () => props.startActions?.onNewTerminalAt(startAtWorkspace()), 'workspace-new-terminal')}
      </Show>

      <RepositoryTargetMenu
        targets={repos}
        labelOf={repo => repo.label}
        header="Repositories"
        testIdPrefix="workspace-repository"
      >
        {repoActions}
      </RepositoryTargetMenu>

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
