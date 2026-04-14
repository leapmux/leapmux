import type { Component } from 'solid-js'
import type { SidebarSectionDef } from './CollapsibleSidebar'
import type { SidebarCommonProps } from './useSidebarCore'

import CircleUser from 'lucide-solid/icons/circle-user'
import Settings from 'lucide-solid/icons/settings'
import { onCleanup } from 'solid-js'
import { IconButton } from '~/components/common/IconButton'
import { dragOverlay as wsDragOverlay } from '~/components/workspace/workspaceList.css'
import { useAuth } from '~/context/AuthContext'
import { Sidebar } from '~/generated/leapmux/v1/section_pb'
import { isSoloMode } from '~/lib/systemInfo'
import { CollapsibleSidebar } from './CollapsibleSidebar'
import * as csStyles from './CollapsibleSidebar.css'
import { useSectionDrag } from './SectionDragContext'
import { UserMenu } from './UserMenu'
import { useSidebarCore } from './useSidebarCore'

type LeftSidebarProps = SidebarCommonProps

export const LeftSidebar: Component<LeftSidebarProps> = (props) => {
  const auth = useAuth()
  const { addExternalDragHandler, addExternalOverlayRenderer } = useSectionDrag()

  const {
    wsOps,
    buildSectionDefs,
    renderSharingDialog,
    expandSectionRef,
  } = useSidebarCore(props, Sidebar.LEFT)

  // ---------------------------------------------------------------------------
  // DnD
  // ---------------------------------------------------------------------------

  // Register workspace DnD handlers with the unified SectionDragProvider.
  // This allows workspace dragging to work through the shared DragDropProvider
  // instead of requiring a separate nested provider (which would shadow section DnD).
  const disposeWsDragHandler = addExternalDragHandler(wsOps.handleWorkspaceDragEnd)
  // eslint-disable-next-line solid/reactivity -- overlay renderer is called from DragOverlay, not a tracked scope
  const disposeWsOverlayRenderer = addExternalOverlayRenderer((draggable: any) => {
    if (!draggable)
      return null
    const id = String(draggable.id)
    if (!id.startsWith('ws-'))
      return null
    const wsId = id.slice(3)
    const workspace = props.workspaces.find(w => w.id === wsId)
    return workspace
      ? <div class={wsDragOverlay}>{workspace.title || 'Untitled'}</div>
      : null
  })
  onCleanup(() => {
    disposeWsDragHandler()
    disposeWsOverlayRenderer()
  })

  // ---------------------------------------------------------------------------
  // Build sidebar section definitions
  // ---------------------------------------------------------------------------

  const sidebarSections = (): SidebarSectionDef[] => {
    const sections = buildSectionDefs()

    // User Menu section (rail-only in collapsed, rendered at bottom in expanded)
    const solo = isSoloMode()
    sections.push({
      id: 'user-menu',
      title: solo ? 'Preferences' : 'User',
      railOnly: true,
      railPosition: 'bottom',
      collapsible: false,
      railIcon: solo ? Settings : CircleUser,
      railTitle: solo ? 'Preferences' : 'User menu',
      railElement: (
        <UserMenu
          trigger={<IconButton icon={solo ? Settings : CircleUser} iconSize="lg" size="lg" title={solo ? 'Preferences' : 'User menu'} data-testid="user-menu-trigger" />}
        />
      ),
      content: () => (
        <UserMenu
          trigger={(
            <span class={csStyles.sidebarTitle} style={{ cursor: 'pointer' }} data-testid="user-menu-trigger">
              {solo ? 'Preferences' : (auth.user()?.username ?? '...')}
            </span>
          )}
        />
      ),
    })

    return sections
  }

  // ---------------------------------------------------------------------------
  // Render
  // ---------------------------------------------------------------------------

  return (
    <>
      <CollapsibleSidebar
        sections={sidebarSections()}
        side="left"
        isCollapsed={props.isCollapsed}
        onExpand={props.onExpand}
        initialOpenSections={props.initialOpenSections}
        initialSectionSizes={props.initialSectionSizes}
        onStateChange={props.onSectionStateChange}
        expandSectionRef={expandSectionRef}
      />

      {renderSharingDialog()}
    </>
  )
}
