import type { Component } from 'solid-js'
import type { BranchRef } from './WorkspaceTabTree'
import type { Section } from '~/generated/proto/leapmux/v1/section_pb'
import type { TabType, Workspace } from '~/generated/proto/leapmux/v1/workspace_pb'
import type { WorkerInfo } from '~/lib/workerInfoCache'

import type { createRepoGitStore } from '~/stores/repoGit.store'
import type { Tab, TabItemOps } from '~/stores/tab.types'
import { createDroppable, SortableProvider } from '@thisbeyond/solid-dnd'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import { createEffect, createMemo, createSignal, For, Show } from 'solid-js'
import { DragHandle } from '~/components/common/DragHandle'
import { createContextMenuAnchor } from '~/components/common/DropdownMenu'
import { Spinner } from '~/components/common/Spinner'
import { Tooltip } from '~/components/common/Tooltip'
import { WORKSPACE_DROP_PREFIX } from '~/components/shell/TabDragContext'
import { KEY_EXPANDED_WORKSPACES, sessionStorageSet } from '~/lib/browserStorage'
import { attachDragActivators } from '~/lib/dragActivators'
import { createGuardedSortableRow } from '~/lib/dragRow'
import { DiffStatsBadge, LabelWithDiffStats } from '../tree/gitStatusUtils'
import * as shared from '../tree/sharedTree.css'
import { sidebarActions } from '../tree/sidebarActions.css'
import { readExpandedWorkspaceIds } from './expandedWorkspaces'
import { WorkspaceContextMenu } from './WorkspaceContextMenu'
import * as styles from './workspaceList.css'
import { sumDiffStatsFromTabs, WorkspaceTabTree } from './WorkspaceTabTree'

/** solid-dnd directives are callable but typed as objects; this wraps the unsafe cast. */
function applyDirective(directive: { ref: unknown }, el: HTMLElement) {
  (directive as unknown as (el: HTMLElement) => void)(el)
}

export interface WorkspaceSectionContentProps {
  workspaces: Workspace[]
  sectionId: string
  activeWorkspaceId: string | null
  sections: Section[]
  onSelect: (id: string) => void
  onRename: (workspace: Workspace) => void
  onMoveTo: (workspaceId: string, targetSectionId: string) => void
  onArchive: (workspaceId: string) => void
  onUnarchive: (workspaceId: string) => void
  onDelete: (workspaceId: string) => void
  isArchived: (workspaceId: string) => boolean
  renamingWorkspaceId: string | null
  renameValue: string
  onRenameInput: (value: string) => void
  onRenameCommit: () => void
  onRenameCancel: () => void
  isWorkspaceLoading: (id: string) => boolean
  getTabsForWorkspace: (workspaceId: string) => Tab[]
  getActiveTabKeyForWorkspace: (workspaceId: string) => string | null
  /**
   * Tile ids in their top-left-first traversal order for the given
   * workspace. Drives the in-tree leaf ordering so it tracks the live
   * tiling layout. Returns `[]` when no layout is projected yet (the CRDT
   * bootstrap hasn't landed); the tree falls back to position-only order in
   * that case.
   */
  getTileOrderForWorkspace: (workspaceId: string) => readonly string[]
  onTabClick: (type: TabType, id: string) => void
  tabItemOps?: TabItemOps
  readOnly?: boolean
  /**
   * Reactive lookup for worker display info. Forwarded to
   * {@link WorkspaceTabTree} to disambiguate same-name branches that
   * collide across distinct workers or working directories.
   */
  workerInfoFn?: (id: string) => WorkerInfo | null
  isWorkerKnownOnline?: (workerId: string) => boolean
  onChangeBranch?: (ref: BranchRef) => void
  onDeleteBranch?: (ref: BranchRef) => void
  repoGitStore: ReturnType<typeof createRepoGitStore>
}

export const WorkspaceSectionContent: Component<WorkspaceSectionContentProps> = (props) => {
  /* eslint-disable solid/reactivity -- stable identifier for createDroppable */
  const droppable = createDroppable(`section-${props.sectionId}`, {
    sectionId: props.sectionId,
  })
  /* eslint-enable solid/reactivity */

  // ---------------------------------------------------------------------------
  // Stable ID-based iteration for workspace items.
  //
  // Workspace objects may be new references on every reactive update.  By
  // iterating over workspace ID strings (which are value-stable), the <For>
  // callbacks persist across updates and createSortable is called only once per
  // workspace — preventing orphaned DnD primitives and "nonexistent
  // transformer" warnings.
  // ---------------------------------------------------------------------------

  // Track which workspaces have their tab tree expanded (independent of selection).
  // Restore from sessionStorage so expanded state survives page refresh.
  const [expandedIds, setExpandedIds] = createSignal<Set<string>>(readExpandedWorkspaceIds())

  function isExpanded(id: string): boolean {
    return expandedIds().has(id)
  }

  function toggleExpanded(id: string) {
    setExpandedIds((prev) => {
      const next = new Set(prev)
      if (next.has(id))
        next.delete(id)
      else
        next.add(id)
      return next
    })
  }

  // Persist expanded state to sessionStorage.
  createEffect(() => {
    const ids = expandedIds()
    sessionStorageSet(KEY_EXPANDED_WORKSPACES, [...ids])
  })

  // Auto-expand the active workspace when it changes (if it has tabs).
  createEffect(() => {
    const activeId = props.activeWorkspaceId
    if (activeId && props.getTabsForWorkspace(activeId).length > 0) {
      setExpandedIds((prev) => {
        if (prev.has(activeId))
          return prev
        const next = new Set(prev)
        next.add(activeId)
        return next
      })
    }
  })

  // The active workspace used to be forked onto separate `tabs` /
  // `activeTabKey` props "for backwards compatibility". Both were wired to the
  // very same call at the very same argument as the per-workspace lookups
  // (`view.forWorkspace(activeWorkspaceId)`), so the fork chose between two
  // identical answers. Every workspace is live in the projection now; there is
  // no active/inactive distinction left to make here.
  function tabsFor(workspaceId: string): Tab[] {
    return props.getTabsForWorkspace(workspaceId)
  }

  function activeTabKeyFor(workspaceId: string): string | null {
    return props.getActiveTabKeyForWorkspace(workspaceId)
  }

  /** Per-workspace diff stats. */
  function workspaceDiffStatsFor(workspaceId: string) {
    return sumDiffStatsFromTabs(tabsFor(workspaceId), props.repoGitStore)
  }

  const workspaceIds = () => props.workspaces.map(w => w.id)

  const workspaceById = createMemo(() => {
    const map = new Map<string, Workspace>()
    for (const w of props.workspaces) map.set(w.id, w)
    return map
  })

  return (
    <SortableProvider ids={props.workspaces.map(w => `ws-${w.id}`)}>
      <div
        ref={droppable}
        class={styles.sectionItems}
        classList={{
          [styles.sectionHeaderDropTarget]: droppable.isActiveDroppable,
        }}
      >
        <Show
          when={props.workspaces.length > 0}
          fallback={<div class={styles.emptySection}>No workspaces</div>}
        >
          <For each={workspaceIds()}>
            {(id) => {
              const workspace = () => workspaceById().get(id)!
              const dragRow = createGuardedSortableRow(`ws-${id}`, {
                sectionId: props.sectionId,
                workspaceId: id,
              })
              const wsDroppable = createDroppable(`${WORKSPACE_DROP_PREFIX}${id}`)
              const isActive = () => id === props.activeWorkspaceId
              const isRenaming = () => props.renamingWorkspaceId === id
              const isLoading = () => props.isWorkspaceLoading(id)
              const title = () => workspace().title || 'Untitled'
              // workspaceDiffStatsFor sums diff stats across the
              // workspace's tabs; memoize so we don't re-sum on every
              // access.
              const stats = createMemo(() => workspaceDiffStatsFor(id))

              // Track whether the item was dragged so we can suppress the click
              // that fires on mouseup after a drag-and-drop operation.
              let wasDragging = false
              createEffect(() => {
                if (dragRow.isActiveDraggable)
                  wasDragging = true
              })

              // The row element, for the right-click / long-press menu below.
              const [rowEl, setRowEl] = createContextMenuAnchor()
              // Mouse-only activation on the row body; the grip carries the
              // raw handlers, so touch drags start there and nowhere else.
              attachDragActivators(() => rowEl(), dragRow.bodyActivators, { touch: 'block' })

              return (
                <>
                  <div
                    ref={(el: HTMLElement) => {
                      setRowEl(el)
                      // Node registration only — activation lives on the
                      // guarded body and the grip, not the whole row.
                      dragRow.ref(el)
                      applyDirective(wsDroppable, el)
                    }}
                    class={styles.item}
                    classList={{
                      [styles.itemActive]: isActive(),
                      [styles.itemDragging]: dragRow.isActiveDraggable,
                      [styles.itemDropTarget]: wsDroppable.isActiveDroppable,
                    }}
                    style={dragRow.style()}
                    onClick={() => {
                      if (wasDragging) {
                        wasDragging = false
                        return
                      }
                      props.onSelect(id)
                    }}
                    onDblClick={() => props.onRename(workspace())}
                    data-testid={`workspace-item-${id}`}
                    // Which workspace is active is no longer in the URL, so this
                    // row is the only place it is observable from outside. E2E
                    // asserts a switch landed on it; the styling still comes
                    // from `itemActive` above.
                    data-active={isActive() ? 'true' : 'false'}
                    // Collapsing only sets `visibility: hidden` on the children
                    // wrapper, so the leaves stay in the DOM and counting them
                    // cannot tell expanded from collapsed. Expose the bit.
                    data-expanded={isExpanded(id) ? 'true' : 'false'}
                  >
                    <DragHandle activators={dragRow.gripActivators} testId="workspace-drag-handle" />
                    <ChevronRight
                      size={14}
                      class={`${shared.chevron} ${isExpanded(id) ? shared.chevronExpanded : ''}`}
                      data-testid={`workspace-chevron-${id}`}
                      onClick={(e) => {
                        e.stopPropagation()
                        toggleExpanded(id)
                      }}
                    />
                    <Show
                      when={!isRenaming()}
                      fallback={(
                        <input
                          class={styles.itemRenameInput}
                          value={props.renameValue}
                          onInput={e => props.onRenameInput(e.currentTarget.value)}
                          onKeyDown={(e) => {
                            if (e.key === 'Enter') {
                              e.preventDefault()
                              props.onRenameCommit()
                            }
                            if (e.key === 'Escape')
                              props.onRenameCancel()
                          }}
                          onBlur={() => props.onRenameCommit()}
                          onClick={e => e.stopPropagation()}
                          ref={(el) => {
                            requestAnimationFrame(() => {
                              el.focus()
                              el.select()
                            })
                          }}
                        />
                      )}
                    >
                      <Tooltip content={<LabelWithDiffStats label={title()} stats={stats()} />} showWhen="clipped">
                        <span class={styles.itemLabel}>
                          <span class={styles.itemTitle}>{title()}</span>
                          <DiffStatsBadge stats={stats()} />
                        </span>
                      </Tooltip>
                    </Show>

                    <div class={sidebarActions}>
                      <Show
                        when={!isLoading()}
                        fallback={<Spinner size="xs" />}
                      >
                        <Show when={!isRenaming()}>
                          <WorkspaceContextMenu
                            contextMenuFor={rowEl}
                            isArchived={props.isArchived(id)}
                            sections={props.sections}
                            currentSectionId={props.sectionId}
                            onRename={() => props.onRename(workspace())}
                            onMoveTo={targetSectionId => props.onMoveTo(id, targetSectionId)}
                            onArchive={() => props.onArchive(id)}
                            onUnarchive={() => props.onUnarchive(id)}
                            onDelete={() => props.onDelete(id)}
                          />
                        </Show>
                      </Show>
                    </div>
                  </div>
                  <div
                    class={`${shared.childrenWrapper} ${isExpanded(id) ? shared.childrenWrapperExpanded : ''}`}
                    data-testid={`workspace-children-${id}`}
                  >
                    <div class={shared.childrenInner}>
                      <WorkspaceTabTree
                        tabs={tabsFor(id)}
                        tileOrder={props.getTileOrderForWorkspace(id)}
                        activeTabKey={activeTabKeyFor(id)}
                        onTabClick={(type, tabId) => {
                          // Select first, then switch. Both orders work now —
                          // every workspace's tabs are in the projection, so the
                          // clicked tab is selectable before its workspace is on
                          // screen. This used to hand the choice off through
                          // sessionStorage for the restore path to pick up,
                          // because the tab did not exist in any live store
                          // until the switch had finished loading it.
                          props.onTabClick(type, tabId)
                          if (id !== props.activeWorkspaceId)
                            props.onSelect(id)
                        }}
                        tabItemOps={props.tabItemOps}
                        readOnly={props.readOnly}
                        workspaceId={id}
                        workerInfoFn={props.workerInfoFn}
                        isWorkerKnownOnline={props.isWorkerKnownOnline}
                        onChangeBranch={props.onChangeBranch}
                        onDeleteBranch={props.onDeleteBranch}
                        repoGitStore={props.repoGitStore}
                      />
                    </div>
                  </div>
                </>
              )
            }}
          </For>
        </Show>
      </div>
    </SortableProvider>
  )
}
