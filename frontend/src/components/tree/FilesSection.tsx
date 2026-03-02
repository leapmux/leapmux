import type { Component } from 'solid-js'
import type { DirectoryTreeHandle } from './DirectoryTree'
import type { GitFileStatusEntry } from '~/generated/leapmux/v1/git_pb'
import type { createGitFileStatusStore, GitFilterTab } from '~/stores/gitFileStatus.store'
import ChevronsDownUp from 'lucide-solid/icons/chevrons-down-up'
import FileIcon from 'lucide-solid/icons/file'
import FolderTree from 'lucide-solid/icons/folder-tree'
import List from 'lucide-solid/icons/list'
import LocateFixed from 'lucide-solid/icons/locate-fixed'
import RefreshCw from 'lucide-solid/icons/refresh-cw'
import { createEffect, createSignal, For, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { IconButton, IconButtonState } from '~/components/common/IconButton'
import { GitFileStatusCode } from '~/generated/leapmux/v1/git_pb'
import { DirectoryTree } from './DirectoryTree'
import * as dtStyles from './DirectoryTree.css'
import * as styles from './FilesSection.css'

export interface FilesSectionHandle {
  collapseAll: () => void
  isFiltered: () => boolean
  flatListMode: () => boolean
  toggleFlatListMode: () => void
}

export interface FilesSectionProps {
  workerId: string
  workingDir: string
  homeDir: string
  fileTreePath: string
  onFileSelect: (path: string) => void
  onFileOpen?: (path: string, openSource?: GitFilterTab) => void
  onMention?: (path: string) => void
  onOpenTerminal?: (dirPath: string) => void
  gitStatusStore: ReturnType<typeof createGitFileStatusStore>
  /** Currently active file tab's path (for locate file). */
  activeFilePath?: string
  /** Whether the active tab is a file tab (for locate button enabled state). */
  hasActiveFileTab: boolean
  /** Ref callback for imperative actions (collapse all). */
  ref?: (handle: FilesSectionHandle) => void
}

export interface FilesSectionHeaderActionsProps {
  onCollapseAll: () => void
  onLocateFile: () => void
  onRefresh: () => void
  hasActiveFileTab: boolean
  isFiltered?: () => boolean
  flatListMode?: () => boolean
  onToggleFlatList?: () => void
}

const FILTER_TABS: { key: GitFilterTab, label: string }[] = [
  { key: 'all', label: 'All' },
  { key: 'changed', label: 'Changed' },
  { key: 'staged', label: 'Staged' },
  { key: 'unstaged', label: 'Unstaged' },
]

/** Colored file icon for a git file entry. */
export const FileStatusIcon: Component<{ entry: GitFileStatusEntry }> = (props) => {
  const iconClass = () => {
    const e = props.entry
    if (e.unstagedStatus === GitFileStatusCode.UNMERGED || e.stagedStatus === GitFileStatusCode.UNMERGED)
      return dtStyles.iconConflict
    if (e.unstagedStatus === GitFileStatusCode.UNTRACKED)
      return dtStyles.iconUntracked
    if (e.stagedStatus !== GitFileStatusCode.UNSPECIFIED && e.unstagedStatus === GitFileStatusCode.UNSPECIFIED)
      return dtStyles.iconStaged
    return dtStyles.iconUnstaged
  }

  const testId = () => {
    const e = props.entry
    if (e.stagedStatus !== GitFileStatusCode.UNSPECIFIED && e.unstagedStatus === GitFileStatusCode.UNSPECIFIED)
      return 'git-status-staged'
    if (e.unstagedStatus === GitFileStatusCode.UNTRACKED)
      return 'git-status-untracked'
    return 'git-status-unstaged'
  }

  return <Icon icon={FileIcon} size="sm" class={iconClass()} data-testid={testId()} />
}

/** Diff stats badge showing +N -M. */
export const DiffStatsBadge: Component<{ entry: GitFileStatusEntry }> = (props) => {
  const totalAdded = () => props.entry.linesAdded + props.entry.stagedLinesAdded
  const totalDeleted = () => props.entry.linesDeleted + props.entry.stagedLinesDeleted

  return (
    <Show when={totalAdded() > 0 || totalDeleted() > 0}>
      <span class={styles.diffStats} data-testid="git-diff-stats">
        <Show when={totalAdded() > 0}>
          <span class={styles.diffStatsAdded}>
            +
            {totalAdded()}
          </span>
        </Show>
        {totalAdded() > 0 && totalDeleted() > 0 ? ' ' : ''}
        <Show when={totalDeleted() > 0}>
          <span class={styles.diffStatsDeleted}>
            -
            {totalDeleted()}
          </span>
        </Show>
      </span>
    </Show>
  )
}

/** Toolbar buttons rendered in the section header. */
export const FilesSectionHeaderActions: Component<FilesSectionHeaderActionsProps> = (props) => {
  return (
    <>
      <Show when={props.isFiltered?.()}>
        <IconButton
          icon={props.flatListMode?.() ? FolderTree : List}
          iconSize="xs"
          size="sm"
          title={props.flatListMode?.() ? 'Tree view' : 'Flat list'}
          state={props.flatListMode?.() ? IconButtonState.Active : IconButtonState.Enabled}
          onClick={() => props.onToggleFlatList?.()}
          data-testid="files-flat-list-toggle"
        />
      </Show>
      <Show when={props.hasActiveFileTab}>
        <IconButton
          icon={LocateFixed}
          iconSize="xs"
          size="sm"
          title="Locate active file"
          onClick={() => props.onLocateFile()}
          data-testid="files-locate-file"
        />
      </Show>
      <IconButton
        icon={ChevronsDownUp}
        iconSize="xs"
        size="sm"
        title="Collapse all"
        onClick={() => props.onCollapseAll()}
        data-testid="files-collapse-all"
      />
      <IconButton
        icon={RefreshCw}
        iconSize="xs"
        size="sm"
        title="Refresh git status"
        onClick={() => props.onRefresh()}
        data-testid="files-refresh-git"
      />
    </>
  )
}

export const FilesSection: Component<FilesSectionProps> = (props) => {
  const [activeFilter, setActiveFilter] = createSignal<GitFilterTab>('all')
  const [flatListMode, setFlatListMode] = createSignal(false)
  let treeHandle: DirectoryTreeHandle | undefined

  const isFiltered = () => activeFilter() !== 'all'

  // Expose imperative handle via ref callback.
  createEffect(() => {
    props.ref?.({
      collapseAll: () => treeHandle?.collapseAll(),
      isFiltered,
      flatListMode,
      toggleFlatListMode: () => setFlatListMode(prev => !prev),
    })
  })

  const changedFiles = () => props.gitStatusStore.getChangedFiles(activeFilter())

  const handleFlatFileOpen = (entry: GitFileStatusEntry) => {
    const root = props.gitStatusStore.state.repoRoot || props.workingDir
    const absPath = `${root}/${entry.path}`
    props.onFileOpen?.(absPath, activeFilter())
  }

  /** Compute visible paths for filtered tree view. */
  const visiblePaths = (): Set<string> | undefined => {
    if (!isFiltered())
      return undefined

    const files = changedFiles()
    const root = props.gitStatusStore.state.repoRoot || props.workingDir
    const paths = new Set<string>()

    // Always include root.
    paths.add(root)

    for (const f of files) {
      const absPath = `${root}/${f.path}`
      paths.add(absPath)
      // Add all ancestor directories.
      let dir = absPath
      while (true) {
        const lastSlash = dir.lastIndexOf('/')
        if (lastSlash <= 0)
          break
        dir = dir.substring(0, lastSlash)
        if (dir === root)
          break
        paths.add(dir)
      }
    }

    return paths
  }

  return (
    <div class={styles.wrapper}>
      <Show when={props.gitStatusStore.state.isGitRepo}>
        <div class={styles.tabBar} data-testid="files-filter-tab-bar">
          <For each={FILTER_TABS}>
            {tab => (
              <button
                class={`${styles.tabButton}${activeFilter() === tab.key ? ` ${styles.tabButtonActive}` : ''}`}
                onClick={() => setActiveFilter(tab.key)}
                data-testid={`files-filter-${tab.key}`}
              >
                {tab.label}
              </button>
            )}
          </For>
        </div>
      </Show>

      <Show
        when={isFiltered() && flatListMode()}
        fallback={(
          <div class={styles.treeContent}>
            <DirectoryTree
              workerId={props.workerId}
              showFiles
              selectedPath={props.fileTreePath}
              onSelect={props.onFileSelect}
              onFileOpen={path => props.onFileOpen?.(path, activeFilter())}
              onMention={props.onMention}
              onOpenTerminal={props.onOpenTerminal}
              rootPath={props.workingDir || '~'}
              homeDir={props.homeDir}
              gitStatusStore={props.gitStatusStore}
              visiblePaths={visiblePaths()}
              ref={(h) => { treeHandle = h }}
            />
          </div>
        )}
      >
        <div class={styles.flatList} data-testid="files-flat-list">
          <For each={changedFiles()}>
            {entry => (
              <div
                class={styles.flatListItem}
                onClick={() => handleFlatFileOpen(entry)}
              >
                <FileStatusIcon entry={entry} />
                <span>{entry.path}</span>
                <DiffStatsBadge entry={entry} />
              </div>
            )}
          </For>
          <Show when={changedFiles().length === 0}>
            <div style={{ 'padding': 'var(--space-4)', 'color': 'var(--faint-foreground)', 'font-size': 'var(--text-7)', 'text-align': 'center' }}>
              No changes
            </div>
          </Show>
        </div>
      </Show>
    </div>
  )
}
