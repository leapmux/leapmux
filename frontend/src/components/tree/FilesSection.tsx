import type { Component } from 'solid-js'
import type { DirectoryTreeHandle } from './DirectoryTree'
import type { GitFileStatusEntry } from '~/generated/leapmux/v1/common_pb'
import type { PathFlavor } from '~/lib/paths'
import type { createGitFileStatusStore, GitFilterTab } from '~/stores/gitFileStatus.store'
import ChevronsDownUp from 'lucide-solid/icons/chevrons-down-up'
import Eye from 'lucide-solid/icons/eye'
import EyeOff from 'lucide-solid/icons/eye-off'
import FileIcon from 'lucide-solid/icons/file'
import FolderTree from 'lucide-solid/icons/folder-tree'
import List from 'lucide-solid/icons/list'
import LocateFixed from 'lucide-solid/icons/locate-fixed'
import { createEffect, createMemo, createSignal, For, on, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { IconButton, IconButtonState } from '~/components/common/IconButton'
import { RefreshButton } from '~/components/common/RefreshButton'
import { localStorageGet, localStorageSet, PREFIX_FILES_SHOW_HIDDEN } from '~/lib/browserStorage'
import { shortcutHint } from '~/lib/shortcuts/display'
import { fileEntryToDiffStats } from '~/stores/gitFileStatus.store'
import { DirectoryTree } from './DirectoryTree'
import * as styles from './FilesSection.css'
import { getGitFileIconClass, RowLabelWithStats } from './gitStatusUtils'
import { computeGitVisibility, flatEntryOpenTarget, makeGitVisibilityPredicate } from './gitVisibility'

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
  /** Path flavor for the worker this section is rendering. */
  flavor: PathFlavor
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
  /**
   * When false, the directory tree's initial filesystem fetch is
   * suppressed. Forwarded to DirectoryTree — see its `enabled` prop.
   */
  enabled?: boolean
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

/**
 * Which tab an arrow/Home/End keypress moves to, or undefined for a key the tab
 * bar does not own.
 *
 * Pure and exported so the contract role=tab requires (Tab reaches the SET, the
 * arrows move within it, and both ends wrap) is testable without mounting a
 * tree against a live worker.
 */
export function nextFilterTab(keys: GitFilterTab[], current: GitFilterTab, key: string): GitFilterTab | undefined {
  const i = keys.indexOf(current)
  const cur = i < 0 ? 0 : i
  switch (key) {
    case 'ArrowRight':
    case 'ArrowDown':
      return keys[(cur + 1) % keys.length]
    case 'ArrowLeft':
    case 'ArrowUp':
      return keys[(cur - 1 + keys.length) % keys.length]
    case 'Home':
      return keys[0]
    case 'End':
      return keys[keys.length - 1]
    default:
      return undefined
  }
}

/** Ties each role=tab to the region it swaps, via aria-controls. */
const FILTER_PANEL_ID = 'files-filter-panel'

const FILTER_TABS: { key: GitFilterTab, label: string }[] = [
  { key: 'all', label: 'All' },
  { key: 'changed', label: 'Changed' },
  { key: 'staged', label: 'Staged' },
  { key: 'unstaged', label: 'Unstaged' },
]

/** Toolbar buttons rendered in the section header. */
export const FilesSectionHeaderActions: Component<FilesSectionHeaderActionsProps> = (props) => {
  const showingHidden = () => props.showHiddenFiles?.() ?? true
  return (
    <>
      <Show when={props.isFiltered?.()}>
        <IconButton
          icon={props.flatListMode?.() ? FolderTree : List}
          iconSize="sm"
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
          iconSize="sm"
          size="sm"
          title="Locate active file"
          onClick={() => props.onLocateFile()}
          data-testid="files-locate-file"
        />
      </Show>
      <IconButton
        icon={ChevronsDownUp}
        iconSize="sm"
        size="sm"
        title="Collapse all"
        onClick={() => props.onCollapseAll()}
        data-testid="files-collapse-all"
      />
      <IconButton
        icon={showingHidden() ? Eye : EyeOff}
        iconSize="sm"
        size="sm"
        title={shortcutHint(showingHidden() ? 'Hide hidden files' : 'Show hidden files', 'app.toggleHiddenFiles')}
        state={showingHidden() ? IconButtonState.Enabled : IconButtonState.Active}
        onClick={() => props.onToggleShowHidden?.()}
        data-testid="files-show-hidden-toggle"
      />
      <RefreshButton
        title={shortcutHint('Refresh', 'app.refreshDirectoryTree')}
        onClick={() => props.onRefresh()}
        data-testid="files-refresh"
      />
    </>
  )
}

export const FilesSection: Component<FilesSectionProps> = (props) => {
  const [activeFilter, setActiveFilter] = createSignal<GitFilterTab>('all')

  // Roving tabindex: only the selected tab is in the tab order, so Tab reaches
  // the tab SET and the arrows move within it. Focus has to follow selection
  // for that to work, hence the element refs.
  const filterTabEls = new Map<GitFilterTab, HTMLButtonElement>()
  const selectFilterTab = (key: GitFilterTab) => {
    setActiveFilter(key)
    filterTabEls.get(key)?.focus()
  }
  const onTabBarKeyDown = (e: KeyboardEvent) => {
    const next = nextFilterTab(FILTER_TABS.map(t => t.key), activeFilter(), e.key)
    if (next === undefined)
      return
    e.preventDefault()
    selectFilterTab(next)
  }
  const [flatListMode, setFlatListMode] = createSignal(false)
  const showHiddenStorageKey = () => `${PREFIX_FILES_SHOW_HIDDEN}${props.workerId}:${props.workingDir}`
  const [showHiddenFiles, setShowHiddenFiles] = createSignal(localStorageGet<boolean>(showHiddenStorageKey()) ?? true)
  let treeHandle: DirectoryTreeHandle | undefined

  // Re-read from localStorage when the storage key changes (workerId/workingDir changed).
  createEffect(on(showHiddenStorageKey, (key) => {
    setShowHiddenFiles(localStorageGet<boolean>(key) ?? true)
  }, { defer: true }))

  // Persist showHiddenFiles when it changes (skip initial mount).
  createEffect(on(showHiddenFiles, (value) => {
    localStorageSet(showHiddenStorageKey(), value)
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
    const absPath = flatEntryOpenTarget(entry.path, root, props.flavor)
    if (absPath === undefined)
      return
    props.onFileOpen?.(absPath, activeFilter())
  }

  /**
   * The predicate a filtered tree renders through, or undefined when no filter
   * is active. Built here rather than in DirectoryTree so git's
   * untracked-subtree rule lives with the git-aware section -- see
   * ./gitVisibility.
   */
  const isVisible = createMemo<((path: string) => boolean) | undefined>(() => {
    if (!isFiltered())
      return undefined
    const root = props.gitStatusStore.state.repoRoot || props.workingDir
    return makeGitVisibilityPredicate(computeGitVisibility(changedFiles(), root, props.flavor), props.flavor)
  })

  return (
    // `data-working-dir` carries the RESOLVED dir, unlike the tree below, which
    // falls back to `~` so it can render something while the dir is still
    // hydrating. E2E needs the distinction: several actions (opening a terminal
    // from the tab bar) read the active tab's dir synchronously on click and
    // take a one-shot degraded branch when it is empty, so a spec must be able
    // to wait for the real thing rather than for a placeholder that is already
    // on screen.
    <div class={styles.wrapper} data-working-dir={props.workingDir}>
      <Show when={props.gitStatusStore.state.isGitRepo}>
        {/*
          * A real TAB SET, not a row of toggle buttons: picking a tab swaps the
          * content of the region below it, which is what role=tab describes and
          * what the repo's own TabBar already uses.
          *
          * It was four buttons whose only state was a CSS class, so assistive
          * tech read them as identical and stateless. aria-pressed fixed the
          * "stateless" half and got the widget wrong: a toggle button announces
          * "pressed", promises it can be un-pressed (clicking the active filter
          * does nothing), and carries no group name and no "2 of 4". role=tab
          * announces "Changed, tab, selected, 2 of 4" inside a named tab list.
          *
          * Roving tabindex + arrow keys come with the role -- APG requires Tab
          * to reach the SET and the arrows to move within it, so only the
          * selected tab is in the tab order.
          */}
        <div
          class={styles.tabBar}
          role="tablist"
          aria-label="Filter files"
          data-testid="files-filter-tab-bar"
          onKeyDown={onTabBarKeyDown}
        >
          <For each={FILTER_TABS}>
            {tab => (
              <button
                class={styles.tabButton}
                classList={{ [styles.tabButtonActive]: activeFilter() === tab.key }}
                role="tab"
                aria-selected={activeFilter() === tab.key}
                aria-controls={FILTER_PANEL_ID}
                tabIndex={activeFilter() === tab.key ? 0 : -1}
                ref={el => filterTabEls.set(tab.key, el)}
                onClick={() => setActiveFilter(tab.key)}
                data-testid={`files-filter-${tab.key}`}
              >
                {tab.label}
              </button>
            )}
          </For>
        </div>
      </Show>

      <div
        id={FILTER_PANEL_ID}
        role={props.gitStatusStore.state.isGitRepo ? 'tabpanel' : undefined}
        tabIndex={props.gitStatusStore.state.isGitRepo ? 0 : undefined}
        class={styles.panel}
      >
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
                flavor={props.flavor}
                gitStatusStore={props.gitStatusStore}
                isVisible={isVisible()}
                showHiddenFiles={showHiddenFiles()}
                turnEndTrigger={props.turnEndTrigger}
                enabled={props.enabled}
                ref={(h) => { treeHandle = h }}
              />
            </div>
          )}
        >
          <div class={styles.flatList} data-testid="files-flat-list">
            <For each={changedFiles()}>
              {(entry) => {
                const gitIcon = getGitFileIconClass(entry)
                const stats = fileEntryToDiffStats(entry)
                return (
                  <div
                    class={styles.flatListItem}
                    onClick={() => handleFlatFileOpen(entry)}
                  >
                    <Icon icon={FileIcon} size="sm" class={gitIcon.class} data-testid={gitIcon.testId} />
                    <RowLabelWithStats label={entry.path} stats={stats} />
                  </div>
                )
              }}
            </For>
            <Show when={changedFiles().length === 0}>
              <div style={{ 'padding': 'var(--space-4)', 'color': 'var(--faint-foreground)', 'font-size': 'var(--text-7)', 'text-align': 'center' }}>
                No changes
              </div>
            </Show>
          </div>
        </Show>
      </div>
    </div>
  )
}
