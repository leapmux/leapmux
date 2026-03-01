import type { Component, JSX } from 'solid-js'
import type { Sidebar } from '~/generated/leapmux/v1/section_pb'
import type { createSectionStore } from '~/stores/section.store'
import { Show } from 'solid-js'
import * as styles from './AppShell.css'
import { SectionDragProvider } from './SectionDragContext'

interface MobileLayoutProps {
  sectionStore: ReturnType<typeof createSectionStore>
  onMoveSection: (sectionId: string, sidebar: Sidebar, position: string) => void
  onMoveSectionServer: (sectionId: string, sidebar: Sidebar, position: string) => void
  leftSidebarOpen: boolean
  rightSidebarOpen: boolean
  closeAllSidebars: () => void
  leftSidebarElement: JSX.Element
  rightSidebarElement: JSX.Element
  tabBarElement: JSX.Element
  tileContent: JSX.Element
  editorPanel: JSX.Element | false
}

export const MobileLayout: Component<MobileLayoutProps> = (props) => {
  return (
    <SectionDragProvider
      sections={() => props.sectionStore.state.sections}
      onMoveSection={props.onMoveSection}
      onMoveSectionServer={props.onMoveSectionServer}
    >
      <div class={styles.mobileShell}>
        <Show when={props.leftSidebarOpen || props.rightSidebarOpen}>
          <div class={styles.mobileOverlay} onClick={() => props.closeAllSidebars()} />
        </Show>

        <div
          class={styles.mobileSidebar}
          classList={{ [styles.mobileSidebarOpen]: props.leftSidebarOpen }}
        >
          {props.leftSidebarElement}
        </div>

        <div
          class={`${styles.mobileSidebar} ${styles.mobileSidebarRight}`}
          classList={{ [styles.mobileSidebarOpen]: props.rightSidebarOpen }}
        >
          {props.rightSidebarElement}
        </div>

        <div class={styles.mobileCenter}>
          <div class={styles.mobileTabBar}>
            {props.tabBarElement}
          </div>
          {props.tileContent}
          {props.editorPanel}
        </div>
      </div>
    </SectionDragProvider>
  )
}
