import type { Component } from 'solid-js'
import type { RepoStartPoint } from './repoStartPoints'
import type { WorkspaceStartActions, WorkspaceStartAt } from './workspaceStartActions'
import type { ContextMenuTargetProps } from '~/components/common/DropdownMenu'
import type { MenuInfoRow } from '~/components/common/MenuInfoRows'
import type { Section } from '~/generated/proto/leapmux/v1/section_pb'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { RepoGitStore } from '~/stores/repoGit'
import type { Tab } from '~/stores/tab.types'
import { createMemo, createResource, createSignal, For, Show } from 'solid-js'
import { platformBridge, revealInFileManager } from '~/api/platformBridge'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { MenuInfoButton } from '~/components/common/MenuInfoRows'
import { rowContextMenuTrigger } from '~/components/common/moreHorizontalTrigger'
import { SubMenu } from '~/components/common/SubMenu'
import { isMoveTargetSection } from '~/components/shell/sectionUtils'
import { usePreferences } from '~/context/PreferencesContext'
import { copyTextToClipboard } from '~/lib/clipboard'
import { loadDetectedEditors, preferredEditor, resolvePreferredEditor } from '~/lib/externalEditors'
import { prettifyJson } from '~/lib/jsonFormat'
import { createLogger } from '~/lib/logger'
import { repoGitView } from '~/stores/repoGit'
import { dangerMenuItem, menuSectionHeader } from '~/styles/shared.css'
import { listRepoStartPoints } from './repoStartPoints'

const log = createLogger('workspace-context-menu')

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

export const WorkspaceContextMenu: Component<WorkspaceContextMenuProps> = (props) => {
  // Gated for the same reason `BranchContextMenu` gates its two worker lists,
  // with more force: one of these mounts per workspace ROW, and it serves the
  // right-click menu as well, so each nested submenu is another eagerly-mounted
  // popover per row.
  const [menuOpen, setMenuOpen] = createSignal(false)
  const prefs = usePreferences()

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
  const anyLocal = () => repos().some(r => isLocal(r.startPoint.workerId))

  // Detected editors, fetched on open and only where a local checkout could use
  // one. `loadDetectedEditors` caches module-wide, so the second row's menu
  // pays nothing.
  const [editors] = createResource(
    () => menuOpen() && anyLocal(),
    async (eligible: boolean) => (eligible ? loadDetectedEditors() : []),
    { initialValue: [] },
  )
  // Named WITHOUT persisting: the label must not write a preference just by
  // rendering. The launch below goes through `resolvePreferredEditor`, which
  // pins whatever it picked.
  const namedEditor = () => preferredEditor(editors(), prefs.preferredEditorId())

  const moveTargets = () => props.sections.filter(
    s => s.id !== props.currentSectionId && isMoveTargetSection(s.sectionType),
  )

  const infoRows = createMemo((): MenuInfoRow[] => {
    // The memo, not a `<Show>`: `DropdownMenu` renders children eagerly and a
    // `createMemo` runs its body at setup, so an ungated builder would walk
    // every tab of every workspace on every reactive tick.
    if (!menuOpen())
      return []
    const rows: MenuInfoRow[] = [
      { label: 'Workspace:', value: props.workspaceTitle },
      { label: 'Section:', value: props.sectionName },
      { label: 'Tabs:', value: String(props.getTabs().length) },
    ]
    // Only when it is unambiguous. Two repositories cannot share one row, and
    // the `Repository` submenu below lists them all anyway.
    if (repos().length === 1)
      rows.push({ label: 'Repository:', value: repos()[0].label })
    return rows
  })

  // Carries the workspace id, which no row shows and which the CLI takes.
  const infoJson = () => prettifyJson({
    workspaceId: props.workspaceId,
    title: props.workspaceTitle,
    section: props.sectionName,
    tabs: props.getTabs().length,
    repositories: repos().map(r => ({
      label: r.label,
      workerId: r.startPoint.workerId,
      path: r.startPoint.gitToplevel,
    })),
  })

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

  /**
   * One tab-creation entry, in the shape the repository count calls for.
   *
   * Zero repositories still offers the item, with no target: a freshly created
   * workspace has no tabs, and that is exactly the row that most needs a way
   * in. One repository renders FLAT -- a submenu holding a single item is a
   * click the user should not have to make.
   */
  const startItems = (verb: string, run: (at: WorkspaceStartAt) => void, testId: string) => (
    <Show
      when={startableRepos().length > 0}
      fallback={(
        <button
          type="button"
          role="menuitem"
          data-testid={testId}
          onClick={() => run(startAtWorkspace())}
        >
          {`${verb}...`}
        </button>
      )}
    >
      <Show
        when={startableRepos().length > 1}
        fallback={(
          <button
            type="button"
            role="menuitem"
            data-testid={testId}
            onClick={() => run(startAt(startableRepos()[0]))}
          >
            {`${verb} in ${startableRepos()[0].label}...`}
          </button>
        )}
      >
        <SubMenu label={`${verb} in`} data-testid={testId} popoverTestId={`${testId}-popover`}>
          <For each={startableRepos()}>
            {repo => (
              <button type="button" role="menuitem" onClick={() => run(startAt(repo))}>
                {repo.label}
              </button>
            )}
          </For>
        </SubMenu>
      </Show>
    </Show>
  )

  /** The read-only actions that hang off ONE repository. */
  const repoActions = (repo: RepoStartPoint) => {
    const originUrl = () => {
      const tab = { workerId: repo.startPoint.workerId, gitToplevel: repo.startPoint.gitToplevel }
      return repoGitView(tab, props.repoGitStore).originUrl
    }
    const local = () => isLocal(repo.startPoint.workerId)
    return (
      <>
        {/* Hidden with no origin: there is no URL to copy. */}
        <Show when={originUrl()}>
          {url => (
            <button type="button" role="menuitem" onClick={() => void copyTextToClipboard(url())}>
              Copy repository URL
            </button>
          )}
        </Show>
        {/* Both of these open the LOCAL Finder or the LOCAL editor, so a
            remote worker's absolute path either does not exist here or --
            worse -- exists and is a different directory. */}
        <Show when={local()}>
          <button
            type="button"
            role="menuitem"
            onClick={() => void revealInFileManager(repo.startPoint.gitToplevel)}
          >
            Reveal in file manager
          </button>
          <Show when={namedEditor()}>
            {editor => (
              <button
                type="button"
                role="menuitem"
                onClick={() => {
                  const target = resolvePreferredEditor(editors(), prefs.preferredEditorId(), prefs.setPreferredEditorId)
                  if (!target)
                    return
                  platformBridge.openInEditor(target.id, repo.startPoint.gitToplevel).catch((err: unknown) => {
                    log.warn('open_in_editor failed', { id: target.id, err })
                  })
                }}
              >
                {`Open in ${editor().displayName}`}
              </button>
            )}
          </Show>
        </Show>
      </>
    )
  }

  /** Whether one repository offers any action at all. */
  const hasRepoActions = (repo: RepoStartPoint) => {
    const tab = { workerId: repo.startPoint.workerId, gitToplevel: repo.startPoint.gitToplevel }
    return Boolean(repoGitView(tab, props.repoGitStore).originUrl) || isLocal(repo.startPoint.workerId)
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
        copyText={infoJson}
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
        {startItems('New agent', at => props.startActions?.onNewAgentAt(at), 'workspace-new-agent')}
        {startItems('New terminal', at => props.startActions?.onNewTerminalAt(at), 'workspace-new-terminal')}
      </Show>

      {/* Copying a URL, revealing a directory and opening an editor mutate
          nothing, so this stays for an archived workspace. */}
      <Show when={repos().some(hasRepoActions)}>
        <SubMenu label="Repository" data-testid="workspace-repository" popoverTestId="workspace-repository-popover">
          <Show
            when={repos().length > 1}
            fallback={repoActions(repos()[0])}
          >
            <For each={repos()}>
              {repo => (
                <Show when={hasRepoActions(repo)}>
                  <div role="group" aria-label={repo.label}>
                    <div class={menuSectionHeader} aria-hidden="true">{repo.label}</div>
                    {repoActions(repo)}
                  </div>
                </Show>
              )}
            </For>
          </Show>
        </SubMenu>
      </Show>

      <hr />

      {/* Renaming an archived workspace is a MUTATION, and the tab bar already
          routes the sibling operation through `canRenameTab(archived, tab)`.
          This row was the one surface ignoring the rule its own predicate
          states. */}
      <Show when={!props.isArchived}>
        <button type="button" role="menuitem" onClick={() => props.onRename()}>
          Rename
        </button>
      </Show>

      {/* KEPT while archived. Moving a workspace writes
          `workspace_section_items.section_id`, which is not a mutation OF the
          workspace -- `MoveWorkspace` is unguarded in the hub by design, since
          archive and unarchive are the same call. It is also the only way to
          unarchive into a SPECIFIC custom section, because `Unarchive` below
          always targets In progress. `isMoveTargetSection` excludes Archived
          as a target, so for an archived workspace every other workspace
          section is legal -- the list is non-empty exactly when it is useful. */}
      <Show when={moveTargets().length > 0}>
        <SubMenu label="Move to" data-testid="workspace-move-to" popoverTestId="workspace-move-to-popover">
          <For each={moveTargets()}>
            {section => (
              <button type="button" role="menuitem" onClick={() => props.onMoveTo(section.id)}>
                {section.name}
              </button>
            )}
          </For>
        </SubMenu>
      </Show>

      <Show when={!props.isArchived}>
        <button type="button" role="menuitem" onClick={() => props.onArchive()}>
          Archive
        </button>
      </Show>

      <Show when={props.isArchived}>
        <button type="button" role="menuitem" onClick={() => props.onUnarchive()}>
          Unarchive
        </button>
      </Show>

      <hr />
      <button type="button" role="menuitem" class={dangerMenuItem} onClick={() => props.onDelete()}>
        Delete
      </button>
    </DropdownMenu>
  )
}
