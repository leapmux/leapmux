import type { Component } from 'solid-js'
import type { Section } from '~/generated/leapmux/v1/section_pb'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import type { Tab, TabType } from '~/stores/tab.store'

import { createDroppable, createSortable, SortableProvider, transformStyle } from '@thisbeyond/solid-dnd'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { createEffect, createMemo, createSignal, For, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { WORKSPACE_DROP_PREFIX } from '~/components/shell/CrossTileDragContext'
import { ShareMode } from '~/generated/leapmux/v1/common_pb'
import { spinner } from '~/styles/animations.css'
import { DiffStatsBadge } from '../tree/gitStatusUtils'
import * as shared from '../tree/sharedTree.css'
import { WorkspaceContextMenu } from './WorkspaceContextMenu'
import * as styles from './workspaceList.css'
import { buildTree, WorkspaceTabTree } from './WorkspaceTabTree'

export interface WorkspaceSectionContentProps {
  workspaces: Workspace[]
  sectionId: string
  activeWorkspaceId: string | null
  currentUserId: string
  isVirtual?: boolean
  sections: Section[]
  onSelect: (id: string) => void
  onRename: (workspace: Workspace) => void
  onMoveTo: (workspaceId: string, targetSectionId: string) => void
  onShare: (workspaceId: string) => void
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
  tabs: Tab[]
  activeTabKey: string | null
  getTabsForWorkspace: (workspaceId: string) => Tab[]
  getActiveTabKeyForWorkspace: (workspaceId: string) => string | null
  onTabClick: (type: TabType, id: string) => void
  onExpandWorkspace?: (workspaceId: string) => void
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
  const EXPANDED_KEY = 'leapmux:expandedWorkspaces'
  function loadExpandedIds(): Set<string> {
    try {
      const stored = sessionStorage.getItem(EXPANDED_KEY)
      return stored ? new Set(JSON.parse(stored) as string[]) : new Set()
    }
    catch {
      return new Set()
    }
  }
  const [expandedIds, setExpandedIds] = createSignal<Set<string>>(loadExpandedIds())

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
    try {
      sessionStorage.setItem(EXPANDED_KEY, JSON.stringify([...ids]))
    }
    catch { /* ignore quota errors */ }
  })

  // Auto-expand the active workspace when it changes (if it has tabs).
  createEffect(() => {
    const activeId = props.activeWorkspaceId
    if (activeId && props.tabs.length > 0) {
      setExpandedIds((prev) => {
        if (prev.has(activeId))
          return prev
        const next = new Set(prev)
        next.add(activeId)
        return next
      })
    }
  })

  // Trigger lazy loading for non-active workspaces restored as expanded
  // from sessionStorage (their tabs haven't been fetched yet).
  createEffect(() => {
    for (const id of expandedIds()) {
      if (id !== props.activeWorkspaceId && tabsFor(id).length === 0) {
        props.onExpandWorkspace?.(id)
      }
    }
  })

  /**
   * Get the tabs for a specific workspace, using the per-workspace lookup or
   * falling back to the active workspace's tabs for backwards compatibility.
   */
  function tabsFor(workspaceId: string): Tab[] {
    if (workspaceId === props.activeWorkspaceId)
      return props.tabs
    return props.getTabsForWorkspace(workspaceId)
  }

  function activeTabKeyFor(workspaceId: string): string | null {
    if (workspaceId === props.activeWorkspaceId)
      return props.activeTabKey
    return props.getActiveTabKeyForWorkspace(workspaceId)
  }

  /** Per-workspace diff stats. */
  function workspaceDiffStatsFor(workspaceId: string) {
    const tree = buildTree(tabsFor(workspaceId))
    return {
      added: tree.groups.reduce((sum, g) => sum + g.diffAdded, 0),
      deleted: tree.groups.reduce((sum, g) => sum + g.diffDeleted, 0),
      untracked: tree.groups.reduce((sum, g) => sum + g.diffUntracked, 0),
    }
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
          [styles.sectionHeaderDropTarget]: droppable.isActiveDroppable && props.sectionId !== '__shared__',
        }}
      >
        <Show
          when={props.workspaces.length > 0}
          fallback={(
            <Show when={!props.isVirtual}>
              <div class={styles.emptySection}>No workspaces</div>
            </Show>
          )}
        >
          <For each={workspaceIds()}>
            {(id) => {
              const workspace = () => workspaceById().get(id)!
              const sortable = createSortable(`ws-${id}`, {
                sectionId: props.sectionId,
                workspaceId: id,
              })
              const wsDroppable = createDroppable(`${WORKSPACE_DROP_PREFIX}${id}`)
              const isActive = () => id === props.activeWorkspaceId
              const isOwner = () => workspace().createdBy === props.currentUserId
              const isRenaming = () => props.renamingWorkspaceId === id
              const isLoading = () => props.isWorkspaceLoading(id)

              // Track whether the item was dragged so we can suppress the click
              // that fires on mouseup after a drag-and-drop operation.
              let wasDragging = false
              createEffect(() => {
                if (sortable.isActiveDraggable)
                  wasDragging = true
              })

              return (
                <>
                  <div
                    ref={(el: HTMLElement) => {
                      (sortable as unknown as (el: HTMLElement) => void)(el);
                      (wsDroppable as unknown as (el: HTMLElement) => void)(el)
                    }}
                    class={styles.item}
                    classList={{
                      [styles.itemActive]: isActive(),
                      [styles.itemDragging]: sortable.isActiveDraggable,
                      [styles.itemDropTarget]: wsDroppable.isActiveDroppable,
                    }}
                    style={transformStyle(sortable.transform)}
                    onClick={() => {
                      if (wasDragging) {
                        wasDragging = false
                        return
                      }
                      props.onSelect(id)
                    }}
                    onDblClick={() => {
                      if (isOwner())
                        props.onRename(workspace())
                    }}
                    data-testid={`workspace-item-${id}`}
                  >
                    <ChevronRight
                      size={14}
                      class={`${shared.chevron} ${isExpanded(id) ? shared.chevronExpanded : ''}`}
                      onClick={(e) => {
                        e.stopPropagation()
                        toggleExpanded(id)
                        if (!isActive())
                          props.onExpandWorkspace?.(id)
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
                            if (e.key === 'Enter')
                              props.onRenameCommit()
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
                      <span class={styles.itemTitle}>
                        {workspace().title || 'Untitled'}
                      </span>
                      <Show when={workspace().shareMode !== ShareMode.PRIVATE && workspace().shareMode !== ShareMode.UNSPECIFIED}>
                        <span class={styles.sharedBadge}>shared</span>
                      </Show>
                      {(() => {
                        const stats = workspaceDiffStatsFor(id)
                        return (
                          <Show when={stats.added > 0 || stats.deleted > 0 || stats.untracked > 0}>
                            <DiffStatsBadge added={stats.added} deleted={stats.deleted} untracked={stats.untracked} />
                          </Show>
                        )
                      })()}
                    </Show>

                    <div class={styles.itemActions}>
                      <Show
                        when={!isLoading()}
                        fallback={<Icon icon={LoaderCircle} size="xs" class={spinner} style={{ 'flex-shrink': '0' }} />}
                      >
                        <Show when={!isRenaming() && !props.isVirtual}>
                          <WorkspaceContextMenu
                            isOwner={isOwner()}
                            isArchived={props.isArchived(id)}
                            sections={props.sections}
                            currentSectionId={props.sectionId}
                            onRename={() => props.onRename(workspace())}
                            onMoveTo={targetSectionId => props.onMoveTo(id, targetSectionId)}
                            onShare={() => props.onShare(id)}
                            onArchive={() => props.onArchive(id)}
                            onUnarchive={() => props.onUnarchive(id)}
                            onDelete={() => props.onDelete(id)}
                          />
                        </Show>
                      </Show>
                    </div>
                  </div>
                  <div class={`${shared.childrenWrapper} ${isExpanded(id) ? shared.childrenWrapperExpanded : ''}`}>
                    <div class={shared.childrenInner}>
                      <WorkspaceTabTree
                        tabs={tabsFor(id)}
                        activeTabKey={activeTabKeyFor(id)}
                        onTabClick={(type, tabId) => {
                          if (id !== props.activeWorkspaceId) {
                            // Store desired tab so workspace restore activates it.
                            sessionStorage.setItem(`leapmux:activeTab:${id}`, `${type}:${tabId}`)
                            props.onSelect(id)
                          }
                          else {
                            props.onTabClick(type, tabId)
                          }
                        }}
                        workspaceId={id}
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
