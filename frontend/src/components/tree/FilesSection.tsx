import type { Component } from 'solid-js'
import type { DirectoryTreeHandle } from './DirectoryTree'
import type { GitFileStatusEntry } from '~/generated/leapmux/v1/common_pb'
import type { FileSortFields, FileSortOrder } from '~/lib/fileSort'
import type { PathFlavor } from '~/lib/paths'
import type { createRepoGitStore, GitFilterTab } from '~/stores/repoGit.store'
import ChevronsDownUp from 'lucide-solid/icons/chevrons-down-up'
import Eye from 'lucide-solid/icons/eye'
import EyeOff from 'lucide-solid/icons/eye-off'
import FileIcon from 'lucide-solid/icons/file'
import FolderTree from 'lucide-solid/icons/folder-tree'
import List from 'lucide-solid/icons/list'
import LocateFixed from 'lucide-solid/icons/locate-fixed'
import { createEffect, createMemo, createSignal, createUniqueId, For, Show } from 'solid-js'
import { FilterTabBar } from '~/components/common/FilterTabBar'
import { Icon } from '~/components/common/Icon'
import { IconButton, IconButtonState } from '~/components/common/IconButton'
import { RefreshButton } from '~/components/common/RefreshButton'
import { PREFIX_FILES_SHOW_HIDDEN, PREFIX_FILES_SORT_ORDER } from '~/lib/browserStorage'
import { createPersistedSignal, persistedBoolean } from '~/lib/createPersistedSignal'
import { DEFAULT_FILE_SORT_ORDER, makeFileComparator, parseFileSortOrder } from '~/lib/fileSort'
import { shortcutHint } from '~/lib/shortcuts/display'
import { fileEntryToDiffStats, isUntrackedDirEntry } from '~/stores/repoGit.store'
import { DirectoryTree } from './DirectoryTree'
import * as styles from './FilesSection.css'
import { FilesSortMenu } from './FilesSortMenu'
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
  sortOrder: () => FileSortOrder
  setSortOrder: (order: FileSortOrder) => void
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
  gitStatusStore: ReturnType<typeof createRepoGitStore>
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
  /**
   * The mounted section's imperative handle, or undefined before its ref effect
   * runs. Every toolbar control that only reads or toggles section state goes
   * through this, rather than through a prop of its own.
   *
   * The handle rather than one prop per member: the two shapes used to mirror
   * each other member for member, so a new control cost three edits in three
   * files -- and each mirrored pair carried its own `?? DEFAULT`, a second copy
   * of a default that could drift from the one the toolbar renders.
   */
  handle: () => FilesSectionHandle | undefined
  /**
   * The three that are NOT on the handle: locate and refresh reach past the
   * section (to the file selection and the git-status store), and whether a
   * file tab is active is the shell's knowledge, not the section's.
   */
  onLocateFile: () => void
  onRefresh: () => void
  hasActiveFileTab: boolean
}

const FILTER_TABS: { key: GitFilterTab, label: string }[] = [
  { key: 'all', label: 'All' },
  { key: 'changed', label: 'Changed' },
  { key: 'staged', label: 'Staged' },
  { key: 'unstaged', label: 'Unstaged' },
]

/**
 * How the sort comparator reads a git status entry.
 *
 * The row displays the repo-relative path, so that is what a name sort orders
 * by. The worker leaves size and mod_time unset for an entry it could not stat
 * (a deleted file, a directory), and those sort last.
 */
const GIT_ENTRY_SORT_FIELDS: FileSortFields<GitFileStatusEntry> = {
  name: entry => entry.path,
  // `is_dir` covers a submodule, whose path carries no trailing slash; the
  // slash still covers git's collapsed untracked subtree. Both group with the
  // directories, which is the rule the tree follows.
  isDir: entry => entry.isDir || isUntrackedDirEntry(entry.path),
  size: entry => (entry.size === undefined ? undefined : Number(entry.size)),
  modTime: entry => entry.modTime,
}

/**
 * Toolbar buttons rendered in the section header.
 *
 * Every default lives HERE, once, because this is what renders. The handle is
 * undefined until the section mounts, so each read supplies the value the
 * toolbar should show in the meantime.
 */
export const FilesSectionHeaderActions: Component<FilesSectionHeaderActionsProps> = (props) => {
  const showingHidden = () => props.handle()?.showHiddenFiles() ?? true
  const flatListMode = () => props.handle()?.flatListMode() ?? false
  return (
    <>
      <Show when={props.handle()?.isFiltered() ?? false}>
        <IconButton
          icon={flatListMode() ? FolderTree : List}
          iconSize="sm"
          size="sm"
          title={flatListMode() ? 'Tree view' : 'Flat list'}
          state={flatListMode() ? IconButtonState.Active : IconButtonState.Enabled}
          onClick={() => props.handle()?.toggleFlatListMode()}
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
        onClick={() => props.handle()?.collapseAll()}
        data-testid="files-collapse-all"
      />
      <IconButton
        icon={showingHidden() ? Eye : EyeOff}
        iconSize="sm"
        size="sm"
        title={shortcutHint(showingHidden() ? 'Hide hidden files' : 'Show hidden files', 'app.toggleHiddenFiles')}
        state={showingHidden() ? IconButtonState.Enabled : IconButtonState.Active}
        onClick={() => props.handle()?.toggleShowHiddenFiles()}
        data-testid="files-show-hidden-toggle"
      />
      <FilesSortMenu
        sortOrder={() => props.handle()?.sortOrder() ?? DEFAULT_FILE_SORT_ORDER}
        onChange={order => props.handle()?.setSortOrder(order)}
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
  // Ties each role=tab to the region it swaps, via aria-controls. Per MOUNT,
  // not a module constant: an id may name only one element, and the shared
  // FilterTabBar's other call site already generates its own for that reason.
  const filterPanelId = createUniqueId()
  const [flatListMode, setFlatListMode] = createSignal(false)
  // Both preferences are scoped to (workerId, workingDir), so the key changes
  // whenever the active tab does. createPersistedSignal owns the two rules that
  // go with that: re-read on a key change, and never write on mount.
  const showHiddenStorageKey = () => `${PREFIX_FILES_SHOW_HIDDEN}${props.workerId}:${props.workingDir}`
  const [showHiddenFiles, setShowHiddenFiles] = createPersistedSignal(showHiddenStorageKey, persistedBoolean(true))
  const sortOrderStorageKey = () => `${PREFIX_FILES_SORT_ORDER}${props.workerId}:${props.workingDir}`
  const [sortOrder, setSortOrder] = createPersistedSignal(sortOrderStorageKey, parseFileSortOrder)
  let treeHandle: DirectoryTreeHandle | undefined

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
      // Accessors, never values: this handle is rebuilt whenever anything it
      // reads changes, and passing sortOrder() would make every sort change
      // rebuild it and re-run setFilesSectionHandle.
      sortOrder,
      setSortOrder: order => setSortOrder(order),
    })
  })

  const changedFiles = () => props.gitStatusStore.getChangedFiles(activeFilter())

  const comparator = createMemo(() => makeFileComparator(sortOrder(), GIT_ENTRY_SORT_FIELDS))
  // Sorted only where the ORDER is what the user sees. The git-visibility
  // predicate below reads the unsorted list on purpose: it derives a set of
  // paths, so a re-sort would hand it an equivalent input and still invalidate
  // every filtered row in the tree.
  //
  // `toSorted`, never `sort`: getChangedFiles('all') returns the store's OWN
  // array, so sorting in place would reorder the store itself behind the keyed
  // reconcile that writes it, and every other reader would see this view's
  // order.
  const sortedChangedFiles = createMemo(() => changedFiles().toSorted(comparator()))

  const handleFlatFileOpen = (entry: GitFileStatusEntry) => {
    // statusRoot(), not repoRoot: a status path is relative to the working-tree
    // root, which is the worktree -- not the parent repo -- on a worktree tab.
    const root = props.gitStatusStore.statusRoot() || props.workingDir
    const absPath = flatEntryOpenTarget(entry, root, props.flavor)
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
    const root = props.gitStatusStore.statusRoot() || props.workingDir
    return makeGitVisibilityPredicate(computeGitVisibility(changedFiles(), root, props.flavor), props.flavor)
  })

  const isGitRepo = () => Boolean(props.gitStatusStore.focusedState()?.toplevel)

  return (
    // `data-working-dir` carries the RESOLVED dir, unlike the tree below, which
    // falls back to `~` so it can render something while the dir is still
    // hydrating. E2E needs the distinction: several actions (opening a terminal
    // from the tab bar) read the active tab's dir synchronously on click and
    // take a one-shot degraded branch when it is empty, so a spec must be able
    // to wait for the real thing rather than for a placeholder that is already
    // on screen.
    <div class={styles.wrapper} data-working-dir={props.workingDir}>
      <Show when={isGitRepo()}>
        <FilterTabBar
          tabs={FILTER_TABS}
          active={activeFilter()}
          onSelect={setActiveFilter}
          ariaLabel="Filter files"
          panelId={filterPanelId}
          testId="files-filter-tab-bar"
          tabTestId={key => `files-filter-${key}`}
        />
      </Show>

      <div
        id={filterPanelId}
        role={isGitRepo() ? 'tabpanel' : undefined}
        tabIndex={isGitRepo() ? 0 : undefined}
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
                sortOrder={sortOrder()}
                turnEndTrigger={props.turnEndTrigger}
                enabled={props.enabled}
                ref={(h) => { treeHandle = h }}
              />
            </div>
          )}
        >
          <div class={styles.flatList} data-testid="files-flat-list">
            <For each={sortedChangedFiles()}>
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
            <Show when={sortedChangedFiles().length === 0}>
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
