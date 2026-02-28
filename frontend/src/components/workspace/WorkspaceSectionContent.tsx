import type { Component } from 'solid-js'
import type { Section } from '~/generated/leapmux/v1/section_pb'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'

import { createDroppable, createSortable, SortableProvider, transformStyle } from '@thisbeyond/solid-dnd'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { createEffect, createMemo, For, Show } from 'solid-js'
import { ShareMode } from '~/generated/leapmux/v1/common_pb'
import { spinner } from '~/styles/animations.css'
import { iconSize } from '~/styles/tokens'
import { WorkspaceContextMenu } from './WorkspaceContextMenu'
import * as styles from './workspaceList.css'

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
                <div
                  ref={sortable}
                  class={styles.item}
                  classList={{
                    [styles.itemActive]: isActive(),
                    [styles.itemDragging]: sortable.isActiveDraggable,
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
                  </Show>

                  <Show
                    when={!isLoading()}
                    fallback={<LoaderCircle size={iconSize.xs} class={spinner} style={{ 'flex-shrink': '0' }} />}
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
              )
            }}
          </For>
        </Show>
      </div>
    </SortableProvider>
  )
}
