import type { Component } from 'solid-js'
import type { DirectoryTreeHandle } from './DirectoryTree'
import type { GitFileStatusEntry } from '~/generated/leapmux/v1/common_pb'
import type { createGitFileStatusStore, GitFilterTab } from '~/stores/gitFileStatus.store'
import ChevronsDownUp from 'lucide-solid/icons/chevrons-down-up'
import Eye from 'lucide-solid/icons/eye'
import EyeOff from 'lucide-solid/icons/eye-off'
import FileIcon from 'lucide-solid/icons/file'
import FolderTree from 'lucide-solid/icons/folder-tree'
import List from 'lucide-solid/icons/list'
import LocateFixed from 'lucide-solid/icons/locate-fixed'
import { createEffect, createSignal, For, on, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { IconButton, IconButtonState } from '~/components/common/IconButton'
import { RefreshButton } from '~/components/common/RefreshButton'
import { GitFileStatusCode } from '~/generated/leapmux/v1/common_pb'
import { PREFIX_FILES_SHOW_HIDDEN, safeGetJson, safeSetJson } from '~/lib/browserStorage'
import { shortcutHint } from '~/lib/shortcuts/display'
import { DirectoryTree } from './DirectoryTree'
import * as styles from './FilesSection.css'
import { DiffStatsBadge, getGitFileIconClass } from './gitStatusUtils'

export interface FilesSectionHandle {
  collapseAll: () => void
  refresh: () => void
  isFiltered: () => boolean
  flatListMode: () => boolean
  toggleFlatListMode: () => void
  showHiddenFiles: () => boolean
  toggleShowHiddenFiles: () => void
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
  /** Signal bumped on agent turn-end; drives directory tree refresh. */
  turnEndTrigger?: number
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
  showHiddenFiles?: () => boolean
  onToggleShowHidden?: () => void
}

const FILTER_TABS: { key: GitFilterTab, label: string }[] = [
  { key: 'all', label: 'All' },
  { key: 'changed', label: 'Changed' },
  { key: 'staged', label: 'Staged' },
  { key: 'unstaged', label: 'Unstaged' },
]

/** Colored file icon for a git file entry. */
export const FileStatusIcon: Component<{ entry: GitFileStatusEntry }> = (props) => {
  const gitIcon = () => getGitFileIconClass(props.entry)
  return <Icon icon={FileIcon} size="sm" class={gitIcon().class} data-testid={gitIcon().testId} />
}

/** Diff stats badge showing +N -M for a git file entry, or *1 for untracked. */
export const FileDiffStatsBadge: Component<{ entry: GitFileStatusEntry }> = (props) => {
  const isUntracked = () => props.entry.unstagedStatus === GitFileStatusCode.UNTRACKED
  return (
    <DiffStatsBadge
      added={isUntracked() ? 0 : props.entry.linesAdded + props.entry.stagedLinesAdded}
      deleted={isUntracked() ? 0 : props.entry.linesDeleted + props.entry.stagedLinesDeleted}
      untracked={isUntracked() ? 1 : 0}
    />
  )
}

/** Toolbar buttons rendered in the section header. */
export const FilesSectionHeaderActions: Component<FilesSectionHeaderActionsProps> = (props) => {
  const showingHidden = () => props.showHiddenFiles?.() ?? true
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
        icon={showingHidden() ? Eye : EyeOff}
        iconSize="xs"
        size="sm"
        title={shortcutHint(showingHidden() ? 'Hide hidden files' : 'Show hidden files', 'app.toggleHiddenFiles')}
        state={showingHidden() ? IconButtonState.Enabled : IconButtonState.Active}
        onClick={() => props.onToggleShowHidden?.()}
        data-testid="files-show-hidden-toggle"
      />
      <RefreshButton
        iconSize="xs"
        size="sm"
        title={shortcutHint('Refresh', 'app.refreshDirectoryTree')}
        onClick={() => props.onRefresh()}
        data-testid="files-refresh"
      />
    </>
  )
}

export const FilesSection: Component<FilesSectionProps> = (props) => {
  const [activeFilter, setActiveFilter] = createSignal<GitFilterTab>('all')
  const [flatListMode, setFlatListMode] = createSignal(false)
  const showHiddenStorageKey = () => `${PREFIX_FILES_SHOW_HIDDEN}${props.workerId}:${props.workingDir}`
  const [showHiddenFiles, setShowHiddenFiles] = createSignal(safeGetJson<boolean>(showHiddenStorageKey()) ?? true)
  let treeHandle: DirectoryTreeHandle | undefined

  // Re-read from localStorage when the storage key changes (workerId/workingDir changed).
  createEffect(on(showHiddenStorageKey, (key) => {
    setShowHiddenFiles(safeGetJson<boolean>(key) ?? true)
  }, { defer: true }))

  // Persist showHiddenFiles when it changes (skip initial mount).
  createEffect(on(showHiddenFiles, (value) => {
    safeSetJson(showHiddenStorageKey(), value)
  }, { defer: true }))

  const isFiltered = () => activeFilter() !== 'all'

  // Expose imperative handle via ref callback.
  createEffect(() => {
    props.ref?.({
      collapseAll: () => treeHandle?.collapseAll(),
      refresh: () => treeHandle?.refresh(),
      isFiltered,
      flatListMode,
      toggleFlatListMode: () => setFlatListMode(prev => !prev),
      showHiddenFiles,
      toggleShowHiddenFiles: () => setShowHiddenFiles(prev => !prev),
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
                class={styles.tabButton}
                classList={{ [styles.tabButtonActive]: activeFilter() === tab.key }}
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
              showHiddenFiles={showHiddenFiles()}
              turnEndTrigger={props.turnEndTrigger}
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
                <FileDiffStatsBadge entry={entry} />
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
