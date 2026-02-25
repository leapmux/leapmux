import type { Component } from 'solid-js'
import type { SidebarSectionDef } from './CollapsibleSidebar'
import type { Section } from '~/generated/leapmux/v1/section_pb'
import type { TodoItem } from '~/stores/chat.store'
import type { createSectionStore } from '~/stores/section.store'
import FolderTree from 'lucide-solid/icons/folder-tree'
import ListChecks from 'lucide-solid/icons/list-checks'
import { createMemo, Show } from 'solid-js'
import { TodoList } from '~/components/todo/TodoList'
import { DirectoryTree } from '~/components/tree/DirectoryTree'
import * as swlStyles from '~/components/workspace/workspaceList.css'
import { Sidebar, SectionType } from '~/generated/leapmux/v1/section_pb'
import { CollapsibleSidebar } from './CollapsibleSidebar'
import * as csStyles from './CollapsibleSidebar.css'

interface RightSidebarProps {
  workspaceId: string
  workerId: string
  workingDir: string
  showTodos: boolean
  activeTodos: TodoItem[]
  fileTreePath: string
  onFileSelect: (path: string) => void
  sectionStore: ReturnType<typeof createSectionStore>
  isCollapsed: boolean
  onExpand: () => void
  onCollapse?: () => void
  initialOpenSections?: Record<string, boolean>
  initialSectionSizes?: Record<string, number>
  onSectionStateChange?: (openSections: Record<string, boolean>, sectionSizes: Record<string, number>) => void
}

export const RightSidebar: Component<RightSidebarProps> = (props) => {
  const store = props.sectionStore

  /** Get sections assigned to the right sidebar, sorted by position. */
  const rightSections = createMemo(() =>
    store.getSectionsForSidebar(Sidebar.RIGHT),
  )

  // Build section definitions reactively from the store
  const sections = (): SidebarSectionDef[] => {
    const secs = rightSections()
    return secs.map((section): SidebarSectionDef => {
      switch (section.sectionType) {
        case SectionType.FILES:
          return {
            id: section.id,
            title: section.name,
            railIcon: FolderTree,
            railTitle: section.name,
            collapsible: secs.length > 1,
            draggable: true,
            testId: 'section-header-files',
            content: () => (
              <Show
                when={props.workerId}
                fallback={<div class={swlStyles.emptySection}>No tab selected</div>}
              >
                <DirectoryTree
                  workerId={props.workerId}
                  showFiles
                  selectedPath={props.fileTreePath}
                  onSelect={props.onFileSelect}
                  rootPath={props.workingDir || '~'}
                />
              </Show>
            ),
          }
        case SectionType.TODOS:
          return {
            id: section.id,
            title: section.name,
            railIcon: ListChecks,
            railTitle: section.name,
            visible: props.showTodos,
            draggable: true,
            testId: 'section-header-todos',
            railBadge: () => (
              <span class={csStyles.railBadgeText}>
                {props.activeTodos.filter(t => t.status === 'completed').length}
                /
                {props.activeTodos.length}
              </span>
            ),
            content: () => <TodoList todos={props.activeTodos} />,
          }
        default:
          // Workspace sections that got moved to the right sidebar
          return {
            id: section.id,
            title: section.name,
            railIcon: FolderTree,
            railTitle: section.name,
            collapsible: true,
            draggable: true,
            testId: `section-header-${sectionTypeTestId(section.sectionType)}`,
            content: () => <></>,
          }
      }
    })
  }

  return (
    <CollapsibleSidebar
      sections={sections()}
      side="right"
      isCollapsed={props.isCollapsed}
      onExpand={props.onExpand}
      onCollapse={props.onCollapse}
      initialOpenSections={props.initialOpenSections}
      initialSectionSizes={props.initialSectionSizes}
      onStateChange={props.onSectionStateChange}
    />
  )
}

/** Map section type to a test ID slug. */
function sectionTypeTestId(sectionType: SectionType): string {
  switch (sectionType) {
    case SectionType.WORKSPACES_IN_PROGRESS: return 'workspaces_in_progress'
    case SectionType.WORKSPACES_CUSTOM: return 'workspaces_custom'
    case SectionType.WORKSPACES_ARCHIVED: return 'workspaces_archived'
    case SectionType.WORKSPACES_SHARED: return 'workspaces_shared'
    case SectionType.FILES: return 'files'
    case SectionType.TODOS: return 'todos'
    default: return String(sectionType)
  }
}
