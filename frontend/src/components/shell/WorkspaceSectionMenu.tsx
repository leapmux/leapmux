import type { Component } from 'solid-js'
import type { WorkspaceStartPoint } from '~/components/workspace/workspaceStartPoint'
import type { Section } from '~/generated/proto/leapmux/v1/section_pb'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { RepoGitStore } from '~/stores/repoGit'
import type { Tab } from '~/stores/tab.types'
import { createMemo, createSignal, For, Show } from 'solid-js'
import { DropdownMenu, DropdownMenuCheckableItem, DropdownMenuItemContent } from '~/components/common/DropdownMenu'
import { moreHorizontalTrigger } from '~/components/common/moreHorizontalTrigger'
import { SubMenu } from '~/components/common/SubMenu'
import { listRepoStartPoints } from '~/components/workspace/repoStartPoints'
import {
  isSectionFilterShown,
  setWorkspaceSortOrder,
  workspaceSortOrder,
} from '~/components/workspace/workspaceListState'
import { gitModeStickyKey, readStickyGitMode } from '~/components/workspace/workspaceStartPoint'
import { SectionType } from '~/generated/proto/leapmux/v1/section_pb'
import { GIT_MODE_LABELS } from '~/hooks/useGitModeState'
import { getShortcutHintsText } from '~/lib/shortcuts/display'
import {
  sortDirectionLabel,
  sortKeyLabel,
  WORKSPACE_SORT_DIRECTIONS,
  WORKSPACE_SORT_KEYS,
} from '~/lib/workspaceSort'
import { dangerMenuItem, menuSectionHeader } from '~/styles/shared.css'
import { sectionTypeTestId } from './sectionUtils'

/**
 * How many repositories the "New workspace in" group offers.
 *
 * A cap rather than the whole list: the group is a shortcut past the directory
 * picker, and a menu longer than the screen is not a shortcut. The dialog's own
 * directory tree reaches everything else.
 */
const MAX_REPO_ROWS = 8

export interface WorkspaceSectionMenuProps {
  section: Section
  /** Whether a workspace may be created into this section. */
  canCreate: boolean
  /** Every tab of every workspace this section holds. */
  getTabs: () => Tab[]
  /** The workspaces this section holds, in the order the rows are drawn. */
  getWorkspaceIds: () => string[]
  repoGitStore: RepoGitStore
  workerInfoFn?: (id: string) => WorkerInfo | null
  isWorkerOnline?: (workerId: string) => boolean
  /** Whether the active workspace is one of this section's. */
  hasActiveWorkspace: () => boolean
  onRevealActiveWorkspace: () => void
  onCollapseAll: () => void
  onExpandAll: () => void
  /** Show or hide this section's filter box, expanding the section to show it. */
  onToggleFilter: () => void
  onNewWorkspace: (startPoint: WorkspaceStartPoint) => void
  /**
   * Whether the ARCHIVE holds anything, unfiltered.
   *
   * Deliberately not `getWorkspaceIds().length`: that is the filtered view, and
   * the two operations below act on the whole archive. Gating them on the view
   * hid them while a filter matched nothing, and offered "Empty archive..." --
   * which cannot be undone -- while the user could see one row of fifty.
   */
  hasArchivedWorkspaces: () => boolean
  /** Archived only: move every archived workspace back to In progress. */
  onUnarchiveAll: () => void
  /** Archived only: delete every archived workspace, after one confirm. */
  onEmptyArchive: () => void
  onNewSection: () => void
  onRenameSection: () => void
  onDeleteSection: () => void
}

/**
 * The section header's menu, replacing the `+` that only opened the New
 * workspace dialog.
 *
 * Three things it fixes. `SectionService`'s create, rename and delete were
 * implemented and tested server-side with no UI at all. The dialog re-asked
 * which worker and which directory for a repository the section already works
 * on. And Archived had no header action whatsoever, so its bulk operations had
 * nowhere to live.
 *
 * The trigger is `moreHorizontalTrigger`, NOT `rowContextMenuTrigger`: the
 * latter adds the `menuTrigger` class, whose `opacity: 0` reveal depends on
 * sitting inside a hovered row's `sidebarActions` box. A section header is not
 * a row, which is the case that helper's own comment describes.
 */
export const WorkspaceSectionMenu: Component<WorkspaceSectionMenuProps> = (props) => {
  const [menuOpen, setMenuOpen] = createSignal(false)

  const isArchived = () => props.section.sectionType === SectionType.WORKSPACES_ARCHIVED
  // The hub refuses a rename or a delete of a built-in section, so the two
  // items are hidden rather than shown and refused. That refusal is the real
  // enforcement; this is the second layer over it.
  const isCustom = () => props.section.sectionType === SectionType.WORKSPACES_CUSTOM

  /**
   * The repositories this section works on.
   *
   * `menuOpen()` restricts it, and it must be the MEMO rather than a `<Show>`
   * around the rows: a `<Show>` hides the DOM and keeps the subscriptions, so
   * every section's menu would re-scan every tab on every reactive tick while
   * closed. `FileActionsMenu` documents the same trap for its info rows.
   */
  const repos = createMemo(() => {
    if (!menuOpen() || !props.canCreate)
      return []
    const rows = listRepoStartPoints(props.getTabs(), props.repoGitStore, {
      workerInfoFn: props.workerInfoFn,
      isWorkerOnline: props.isWorkerOnline,
      limit: MAX_REPO_ROWS,
    })
    // The remembered mode is resolved HERE, inside the gate, so each row is a
    // pure projection of one snapshot: the label and the note come from the
    // same read, and no storage access happens while a row renders.
    //
    // `detail` is the stored mode's own label, verbatim from the table the
    // dialog's radios read -- a second vocabulary here would be exactly the
    // duplication that table exists to remove. Undefined when nothing is
    // remembered, rather than a placeholder: a row that reads "Use current
    // state" for a repository nobody has started a workspace in states a
    // default as though it were a memory. It must also never read "Manual",
    // which the Sort by submenu binds to a sort key two items below.
    return rows.map((row) => {
      const sp = row.startPoint
      const mode = readStickyGitMode(gitModeStickyKey(sp.workerId, sp.gitToplevel))
      return { ...row, detail: mode === undefined ? undefined : GIT_MODE_LABELS[mode] }
    })
  })

  /**
   * Whether this section shows any row.
   *
   * A memo, because three items read it and `DropdownMenu` renders its children
   * eagerly -- so this would otherwise run the row projection three times per
   * tick in every section's menu, open or closed. `menuOpen()` deliberately
   * does NOT restrict it: `disabled` describes the action, not the menu, and a
   * check on the menu state would disable the second item the moment a click on
   * the first closed the popover. The projection itself is memoized per section
   * upstream, so one read costs a map lookup.
   *
   * It answers about the VISIBLE rows, which is what Collapse all and Expand
   * all act on. The archive operations below ask a different question, because
   * they act on the whole archive.
   */
  const hasWorkspaces = createMemo(() => props.getWorkspaceIds().length > 0)

  return (
    <DropdownMenu
      onToggle={setMenuOpen}
      data-testid={`sidebar-section-menu-${sectionTypeTestId(props.section.sectionType)}-popover`}
      aria-label={`${props.section.name} actions`}
      // `title` is a GETTER. The section name is live now, and reading it
      // eagerly would rebuild the options object -- and with it the trigger
      // element -- on every rename, taking any open popover with it.
      trigger={moreHorizontalTrigger({
        'data-testid': `sidebar-section-menu-${sectionTypeTestId(props.section.sectionType)}`,
        get 'title'() { return `${props.section.name} actions` },
      })}
    >
      <Show when={props.canCreate}>
        <button
          type="button"
          role="menuitem"
          // Kept on the ITEM, and still only on In progress. `DropdownMenu`
          // renders children eagerly into the DOM, so an unconditional test id
          // would put one node per workspace section in the document and every
          // Playwright lookup would fail strict mode.
          data-testid={props.section.sectionType === SectionType.WORKSPACES_IN_PROGRESS ? 'sidebar-new-workspace' : undefined}
          onClick={() => props.onNewWorkspace({ kind: 'directory' })}
        >
          <DropdownMenuItemContent
            label="New workspace..."
            detail={getShortcutHintsText('app.newWorkspaceDialog')}
          />
        </button>

        {/* The group and its header disappear together when there is no
            repository to offer. An empty group header, or a separator over
            nothing, is a menu describing a list that is not there. */}
        <Show when={repos().length > 0}>
          <div role="group" aria-label="New workspace in">
            <div class={menuSectionHeader} aria-hidden="true">New workspace in</div>
            <For each={repos()}>
              {repo => (
                <button
                  type="button"
                  role="menuitem"
                  // One shared test id, not one per repository: the key joins
                  // a worker id and a path with a control byte, which is not
                  // something a CSS selector can carry. Scoped to this
                  // section's popover, `.first()` is unambiguous.
                  data-testid="sidebar-new-workspace-repo"
                  onClick={() => props.onNewWorkspace(repo.startPoint)}
                >
                  <DropdownMenuItemContent label={repo.label} detail={repo.detail} />
                </button>
              )}
            </For>
          </div>
        </Show>
        {/* RESERVED: the future "Clone repository..." and "Create empty
            repository..." items land here, after their own <hr/>. The rule is
            that the <hr/> arrives WITH the first item -- a trailing rule over
            nothing is a bug, which is why the group above carries its own. */}
        <hr />
      </Show>

      {/* Through a prop, not `toggleSectionFilter` directly: the box lives in
          the section BODY, and this menu opens while the section is collapsed
          -- where a hidden input cannot take focus and the checkbox would
          report itself checked for a control nobody can see. The section def
          expands first, the same rule "Reveal active workspace" follows. */}
      <DropdownMenuCheckableItem
        kind="checkbox"
        label="Filter workspaces"
        checked={isSectionFilterShown(props.section.id)}
        data-testid="sidebar-filter-workspaces"
        onSelect={() => props.onToggleFilter()}
      />

      {/* The submenu is `as="div"`: it holds TWO independent radio groups, and
          a `menu` popover dismisses on any click inside it -- so picking a
          criterion would close the panel before the user reached the order.
          `FilesSortMenu` is the working precedent and states the same reason.
          `as="div"` is also why the `role="menu"` / `role="group"` scaffolding
          below is written out by hand. */}
      <SubMenu
        label="Sort by"
        as="div"
        data-testid="sidebar-sort-by"
        popoverTestId="sidebar-sort-by-popover"
      >
        <div role="menu" aria-label="Sort workspaces">
          <div role="group" aria-label="Sort by">
            <div class={menuSectionHeader} aria-hidden="true">Sort by</div>
            <For each={WORKSPACE_SORT_KEYS}>
              {key => (
                <DropdownMenuCheckableItem
                  kind="radio"
                  label={sortKeyLabel(key)}
                  checked={workspaceSortOrder().key === key}
                  data-testid={`workspace-sort-key-${key}`}
                  onSelect={() => setWorkspaceSortOrder({ ...workspaceSortOrder(), key })}
                />
              )}
            </For>
          </div>
          <hr />
          <div role="group" aria-label="Order">
            <div class={menuSectionHeader} aria-hidden="true">Order</div>
            <For each={WORKSPACE_SORT_DIRECTIONS}>
              {direction => (
                <DropdownMenuCheckableItem
                  kind="radio"
                  label={sortDirectionLabel(workspaceSortOrder().key, direction)}
                  checked={workspaceSortOrder().direction === direction}
                  data-testid={`workspace-sort-direction-${direction}`}
                  onSelect={() => setWorkspaceSortOrder({ ...workspaceSortOrder(), direction })}
                />
              )}
            </For>
          </div>
        </div>
      </SubMenu>

      <button type="button" role="menuitem" disabled={!hasWorkspaces()} onClick={() => props.onCollapseAll()}>
        Collapse all
      </button>
      <button type="button" role="menuitem" disabled={!hasWorkspaces()} onClick={() => props.onExpandAll()}>
        Expand all
      </button>
      <Show when={props.hasActiveWorkspace()}>
        <button type="button" role="menuitem" onClick={() => props.onRevealActiveWorkspace()}>
          Reveal active workspace
        </button>
      </Show>

      {/* Hidden when the archive is empty: "Unarchive all" and "Empty
          archive..." on nothing are two items that do nothing, and the second
          one asks for a confirmation first. */}
      <Show when={isArchived() && props.hasArchivedWorkspaces()}>
        <hr />
        <button type="button" role="menuitem" onClick={() => props.onUnarchiveAll()}>
          Unarchive all
        </button>
        <button type="button" role="menuitem" class={dangerMenuItem} onClick={() => props.onEmptyArchive()}>
          Empty archive...
        </button>
      </Show>

      <hr />
      <button type="button" role="menuitem" data-testid="sidebar-new-section" onClick={() => props.onNewSection()}>
        New section...
      </button>
      <Show when={isCustom()}>
        <button type="button" role="menuitem" onClick={() => props.onRenameSection()}>
          Rename section...
        </button>
        <button type="button" role="menuitem" class={dangerMenuItem} onClick={() => props.onDeleteSection()}>
          Delete section...
        </button>
      </Show>
    </DropdownMenu>
  )
}
