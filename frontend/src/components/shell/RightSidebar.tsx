import type { Component } from 'solid-js'
import type { SidebarSectionDef } from './CollapsibleSidebar'
import type { TodoItem } from '~/stores/chat.store'
import FolderTree from 'lucide-solid/icons/folder-tree'
import ListChecks from 'lucide-solid/icons/list-checks'
import { Show } from 'solid-js'
import { TodoList } from '~/components/todo/TodoList'
import { DirectoryTree } from '~/components/tree/DirectoryTree'
import * as swlStyles from '~/components/workspace/workspaceList.css'
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
  isCollapsed: boolean
  onExpand: () => void
  onCollapse?: () => void
  initialOpenSections?: Record<string, boolean>
  initialSectionSizes?: Record<string, number>
  onSectionStateChange?: (openSections: Record<string, boolean>, sectionSizes: Record<string, number>) => void
}

export const RightSidebar: Component<RightSidebarProps> = (props) => {
  // Build section definitions reactively
  const sections = (): SidebarSectionDef[] => [
    {
      id: 'files',
      title: 'Files',
      railIcon: FolderTree,
      railTitle: 'Files',
      collapsible: props.showTodos,
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
    },
    {
      id: 'todos',
      title: 'To-dos',
      railIcon: ListChecks,
      railTitle: 'To-dos',
      visible: props.showTodos,
      railBadge: () => (
        <span class={csStyles.railBadgeText}>
          {props.activeTodos.filter(t => t.status === 'completed').length}
          /
          {props.activeTodos.length}
        </span>
      ),
      content: () => <TodoList todos={props.activeTodos} />,
    },
  ]

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
