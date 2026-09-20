import type { Component } from 'solid-js'
import type { SidebarCommonProps } from './useSidebarCore'

import { Sidebar } from '~/generated/proto/leapmux/v1/section_pb'
import { CollapsibleSidebar } from './CollapsibleSidebar'
import { useSidebarCore } from './useSidebarCore'

type RightSidebarProps = SidebarCommonProps

export const RightSidebar: Component<RightSidebarProps> = (props) => {
  const {
    buildSectionDefs,
    expandSectionRef,
  } = useSidebarCore(props, Sidebar.RIGHT)

  return (
    <CollapsibleSidebar
      sections={buildSectionDefs()}
      side="right"
      isCollapsed={props.isCollapsed}
      onExpand={props.onExpand}
      {...(props.initialOpenSections !== undefined ? { initialOpenSections: props.initialOpenSections } : {})}
      {...(props.initialSectionSizes !== undefined ? { initialSectionSizes: props.initialSectionSizes } : {})}
      {...(props.onSectionStateChange !== undefined ? { onStateChange: props.onSectionStateChange } : {})}
      expandSectionRef={expandSectionRef}
    />
  )
}
