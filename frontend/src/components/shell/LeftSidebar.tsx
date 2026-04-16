import type { Component } from 'solid-js'
import type { SidebarCommonProps } from './useSidebarCore'

import { onCleanup } from 'solid-js'
import { dragOverlay as wsDragOverlay } from '~/components/workspace/workspaceList.css'
import { Sidebar } from '~/generated/leapmux/v1/section_pb'
import { CollapsibleSidebar } from './CollapsibleSidebar'
import { useSectionDrag } from './SectionDragContext'
import { useSidebarCore } from './useSidebarCore'

type LeftSidebarProps = SidebarCommonProps

export const LeftSidebar: Component<LeftSidebarProps> = (props) => {
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

  return (
    <>
      <CollapsibleSidebar
        sections={buildSectionDefs()}
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
