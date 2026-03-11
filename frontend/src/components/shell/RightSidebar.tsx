import type { Component } from 'solid-js'
import type { SidebarCommonProps } from './useSidebarCore'

import { Sidebar } from '~/generated/leapmux/v1/section_pb'
import { CollapsibleSidebar } from './CollapsibleSidebar'
import { useSidebarCore } from './useSidebarCore'

type RightSidebarProps = SidebarCommonProps

export const RightSidebar: Component<RightSidebarProps> = (props) => {
  const {
    buildSectionDefs,
    renderSharingDialog,
    expandSectionRef,
  } = useSidebarCore(props, Sidebar.RIGHT)

  return (
    <>
      <CollapsibleSidebar
        sections={buildSectionDefs()}
        side="right"
        isCollapsed={props.isCollapsed}
        onExpand={props.onExpand}
        onCollapse={props.onCollapse}
        initialOpenSections={props.initialOpenSections}
        initialSectionSizes={props.initialSectionSizes}
        onStateChange={props.onSectionStateChange}
        expandSectionRef={expandSectionRef}
      />

      {renderSharingDialog()}
    </>
  )
}
