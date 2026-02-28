import type { Component, JSX } from 'solid-js'
import type { FileInfo } from '~/generated/leapmux/v1/file_pb'
import AtSign from 'lucide-solid/icons/at-sign'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import ClipboardCopy from 'lucide-solid/icons/clipboard-copy'
import Copy from 'lucide-solid/icons/copy'
import File from 'lucide-solid/icons/file'
import Folder from 'lucide-solid/icons/folder'
import MoreHorizontal from 'lucide-solid/icons/more-horizontal'
import TerminalIcon from 'lucide-solid/icons/terminal'
import { createEffect, createSignal, For, Show, untrack } from 'solid-js'
import { fileClient } from '~/api/clients'
import { relativizePath, tildify } from '~/components/chat/messageUtils'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { IconButton } from '~/components/common/IconButton'
import * as styles from './DirectoryTree.css'

export interface DirectoryTreeProps {
  workerId: string
  showFiles?: boolean
  selectedPath: string
  onSelect: (path: string) => void
  onFileOpen?: (path: string) => void
  onMention?: (path: string) => void
  onOpenTerminal?: (dirPath: string) => void
  rootPath?: string
  homeDir?: string
}

interface TreeNodeData {
  path: string
  displayName: string
  isDir: boolean
  size: bigint
}

function sortEntries(a: FileInfo, b: FileInfo): number {
  if (a.isDir !== b.isDir)
    return a.isDir ? -1 : 1
  return a.name.localeCompare(b.name)
}

async function loadChildren(
  workerId: string,
  dirPath: string,
  showFiles: boolean,
): Promise<TreeNodeData[]> {
  const resp = await fileClient.listDirectory({ workerId, path: dirPath, maxDepth: 5 })
  const entries = [...resp.entries].sort(sortEntries)
  const filtered = showFiles ? entries : entries.filter(e => e.isDir)

  return filtered.map(entry => ({
    path: entry.path,
    displayName: entry.name,
    isDir: entry.isDir,
    size: entry.size,
  }))
}

/** Renders the three-dot context menu for a tree node (file or directory). */
function renderTreeContextMenu(menuProps: {
  path: string
  isDir: boolean
  rootPath: string
  homeDir?: string
  onMention?: (path: string) => void
  onOpenTerminal?: (dirPath: string) => void
}): JSX.Element {
  let popoverRef: HTMLElement | undefined
  const closeMenu = () => popoverRef?.hidePopover()

  return (
    <DropdownMenu
      trigger={triggerProps => (
        <IconButton
          icon={MoreHorizontal}
          iconSize={12}
          size="sm"
          class={styles.nodeMenuTrigger}
          onClick={(e: MouseEvent) => {
            e.stopPropagation()
            triggerProps.onClick()
          }}
          ref={triggerProps.ref}
          onPointerDown={(e: PointerEvent) => {
            e.stopPropagation()
            triggerProps.onPointerDown(e)
          }}
          aria-expanded={triggerProps['aria-expanded']}
          data-testid="tree-context-button"
        />
      )}
      popoverRef={(el) => { popoverRef = el }}
    >
      <Show when={menuProps.onMention}>
        <button
          role="menuitem"
          data-testid="tree-mention-button"
          onClick={() => {
            menuProps.onMention?.(menuProps.path)
            closeMenu()
          }}
        >
          <AtSign size={14} />
          Mention in chat
        </button>
      </Show>
      <Show when={menuProps.isDir && menuProps.onOpenTerminal}>
        <button
          role="menuitem"
          data-testid="tree-open-terminal-button"
          onClick={() => {
            menuProps.onOpenTerminal?.(menuProps.path)
            closeMenu()
          }}
        >
          <TerminalIcon size={14} />
          Open a terminal tab here
        </button>
      </Show>
      <button
        role="menuitem"
        data-testid="tree-copy-path-button"
        onClick={() => {
          navigator.clipboard.writeText(menuProps.path)
          closeMenu()
        }}
      >
        <Copy size={14} />
        Copy path
      </button>
      <button
        role="menuitem"
        data-testid="tree-copy-relative-path-button"
        onClick={() => {
          const rel = menuProps.path === menuProps.rootPath
            ? '.'
            : relativizePath(menuProps.path, menuProps.rootPath, menuProps.homeDir)
          navigator.clipboard.writeText(rel)
          closeMenu()
        }}
      >
        <ClipboardCopy size={14} />
        Copy relative path
      </button>
    </DropdownMenu>
  )
}

const TreeNode: Component<{
  node: TreeNodeData
  workerId: string
  showFiles: boolean
  selectedPath: string
  onSelect: (path: string) => void
  onFileOpen?: (path: string) => void
  onMention?: (path: string) => void
  onOpenTerminal?: (dirPath: string) => void
  depth: number
  scrollContainer?: HTMLDivElement
  rootPath: string
  homeDir?: string
  isNodeExpanded: (path: string) => boolean
  setNodeExpanded: (path: string, expanded: boolean) => void
}> = (props) => {
  const [children, setChildren] = createSignal<TreeNodeData[]>([])
  const [loading, setLoading] = createSignal(false)
  const [loaded, setLoaded] = createSignal(false)
  let wrapperRef!: HTMLDivElement

  const expanded = () => props.isNodeExpanded(props.node.path)
  const isSelected = () => props.selectedPath === props.node.path

  const scrollIntoViewIfNeeded = () => {
    const container = props.scrollContainer
    if (!container || !wrapperRef)
      return
    requestAnimationFrame(() => {
      const containerRect = container.getBoundingClientRect()
      const wrapperRect = wrapperRef.getBoundingClientRect()
      if (wrapperRect.bottom > containerRect.bottom) {
        container.scrollTop += wrapperRect.top - containerRect.top
      }
    })
  }

  const doLoad = async () => {
    if (loaded() || loading())
      return
    setLoading(true)
    try {
      const result = await loadChildren(props.workerId, props.node.path, props.showFiles)
      setChildren(result)
      setLoaded(true)
    }
    catch {
      // ignore load errors
    }
    finally {
      setLoading(false)
    }
  }

  const toggle = async () => {
    if (!props.node.isDir) {
      props.onSelect(props.node.path)
      props.onFileOpen?.(props.node.path)
      return
    }
    props.onSelect(props.node.path)
    await doLoad()
    const willExpand = !expanded()
    props.setNodeExpanded(props.node.path, willExpand)
    if (willExpand) {
      scrollIntoViewIfNeeded()
    }
  }

  // Auto-expand when selectedPath is a descendant of this node.
  createEffect(() => {
    const selected = props.selectedPath
    if (!props.node.isDir)
      return
    if (!selected.startsWith(`${props.node.path}/`))
      return
    if (loading())
      return

    if (!loaded()) {
      untrack(() => {
        doLoad().then(() => { // eslint-disable-line solid/reactivity -- one-shot async load inside untrack
          props.setNodeExpanded(props.node.path, true)
          // Scroll into view for the deepest auto-expanded node.
          // Only scroll if this is the closest ancestor (children will handle deeper).
          const hasMatchingChild = children().some(
            c => c.isDir && (selected.startsWith(`${c.path}/`) || selected === c.path),
          )
          if (!hasMatchingChild) {
            scrollIntoViewIfNeeded()
          }
        })
      })
    }
    else if (!expanded()) {
      props.setNodeExpanded(props.node.path, true)
    }
  })

  // Scroll into view when this node is selected via path input.
  createEffect(() => {
    if (props.selectedPath === props.node.path && wrapperRef) {
      const container = props.scrollContainer
      if (!container)
        return
      requestAnimationFrame(() => {
        const containerRect = container.getBoundingClientRect()
        const wrapperRect = wrapperRef.getBoundingClientRect()
        if (wrapperRect.top < containerRect.top || wrapperRect.bottom > containerRect.bottom) {
          container.scrollTop += wrapperRect.top - containerRect.top
        }
      })
    }
  })

  const indent = () => `${8 + props.depth * 16}px`

  return (
    <div ref={wrapperRef}>
      <div
        class={`${styles.node} ${isSelected() ? styles.nodeSelected : ''}`}
        style={{ 'padding-left': indent() }}
        onClick={toggle}
      >
        <Show
          when={props.node.isDir}
          fallback={<span class={styles.chevronPlaceholder} />}
        >
          <Show
            when={expanded()}
            fallback={<ChevronRight size={16} class={styles.chevron} />}
          >
            <ChevronDown size={16} class={styles.chevron} />
          </Show>
        </Show>
        <Show
          when={props.node.isDir}
          fallback={<File size={14} class={styles.fileIcon} />}
        >
          <Folder size={14} class={styles.folderIcon} />
        </Show>
        <span class={styles.nodeName}>{props.node.displayName}</span>
        <div class={styles.nodeActions}>
          {renderTreeContextMenu({
            path: props.node.path,
            isDir: props.node.isDir,
            rootPath: props.rootPath,
            homeDir: props.homeDir,
            onMention: props.onMention,
            onOpenTerminal: props.onOpenTerminal,
          })}
        </div>
      </div>
      <Show when={loading()}>
        <div class={styles.loadingInline} style={{ 'padding-left': `${8 + (props.depth + 1) * 16}px` }}>
          Loading...
        </div>
      </Show>
      <Show when={expanded() && !loading()}>
        <For each={children()}>
          {child => (
            <TreeNode
              node={child}
              workerId={props.workerId}
              showFiles={props.showFiles}
              selectedPath={props.selectedPath}
              onSelect={props.onSelect}
              onFileOpen={props.onFileOpen}
              onMention={props.onMention}
              onOpenTerminal={props.onOpenTerminal}
              depth={props.depth + 1}
              scrollContainer={props.scrollContainer}
              rootPath={props.rootPath}
              homeDir={props.homeDir}
              isNodeExpanded={props.isNodeExpanded}
              setNodeExpanded={props.setNodeExpanded}
            />
          )}
        </For>
        <Show when={children().length === 0 && loaded()}>
          <div class={styles.emptyInline} style={{ 'padding-left': `${8 + (props.depth + 1) * 16}px` }}>
            Empty
          </div>
        </Show>
      </Show>
    </div>
  )
}

export const DirectoryTree: Component<DirectoryTreeProps> = (props) => {
  const [rootChildren, setRootChildren] = createSignal<TreeNodeData[]>([])
  const [loading, setLoading] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)
  const [inputValue, setInputValue] = createSignal('')
  let loadVersion = 0
  let treeRef!: HTMLDivElement

  // -------------------------------------------------------------------------
  // Expand/collapse state persistence via sessionStorage
  // -------------------------------------------------------------------------
  const storageKey = () => `directoryTree:expanded:${props.rootPath ?? '~'}`

  const [expandedPaths, setExpandedPaths] = createSignal<Set<string>>(new Set())

  // Load persisted state on mount / when rootPath changes
  createEffect(() => {
    const key = storageKey()
    try {
      const stored = sessionStorage.getItem(key)
      if (stored) {
        setExpandedPaths(new Set(JSON.parse(stored)))
        return
      }
    }
    catch { /* ignore corrupt data */ }
    // Default: root is expanded
    setExpandedPaths(new Set([props.rootPath ?? '~']))
  })

  // Persist whenever expanded set changes
  createEffect(() => {
    const paths = expandedPaths()
    try {
      sessionStorage.setItem(storageKey(), JSON.stringify([...paths]))
    }
    catch { /* quota exceeded — ignore */ }
  })

  const isNodeExpanded = (path: string) => expandedPaths().has(path)
  const setNodeExpanded = (path: string, expanded: boolean) => {
    setExpandedPaths((prev) => {
      const next = new Set(prev)
      if (expanded) {
        next.add(path)
      }
      else {
        next.delete(path)
      }
      return next
    })
  }

  const rootPath = () => props.rootPath ?? '~'
  const rootExpanded = () => isNodeExpanded(rootPath())
  const rootDisplayName = () => {
    const rp = rootPath()
    return rp.split('/').pop() || rp
  }

  // Sync external selectedPath to input (tildified for display)
  createEffect(() => {
    setInputValue(tildify(props.selectedPath, props.homeDir))
  })

  // Load root children when workerId or rootPath changes
  createEffect(() => {
    const workerId = props.workerId
    const root = props.rootPath ?? '~'
    if (!workerId)
      return

    const version = ++loadVersion
    setLoading(true)
    setError(null)
    setRootChildren([])
    loadChildren(workerId, root, props.showFiles ?? false)
      .then((children) => {
        if (version !== loadVersion)
          return
        setRootChildren(children)
        setLoading(false)
      })
      .catch((err) => {
        if (version !== loadVersion)
          return
        setError(err instanceof Error ? err.message : 'Failed to load directory')
        setLoading(false)
      })
  })

  const handlePathKeyDown = (e: KeyboardEvent) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      const value = inputValue().trim()
      if (value) {
        props.onSelect(value)
      }
    }
  }

  const handlePathBlur = () => {
    const value = inputValue().trim()
    if (value) {
      props.onSelect(value)
    }
  }

  const toggleRoot = () => {
    setNodeExpanded(rootPath(), !rootExpanded())
  }

  return (
    <div class={styles.container}>
      <div class={styles.pathInput}>
        <input
          class={styles.pathInputField}
          type="text"
          value={inputValue()}
          title={props.selectedPath}
          onInput={e => setInputValue(e.currentTarget.value)}
          onKeyDown={handlePathKeyDown}
          onBlur={handlePathBlur}
          placeholder="Enter path..."
        />
      </div>
      <div class={styles.tree} ref={treeRef}>
        <Show when={error()}>
          <div class={styles.errorState}>{error()}</div>
        </Show>
        <Show when={loading()}>
          <div class={styles.loadingState}>Loading...</div>
        </Show>
        <Show when={!loading() && !error()}>
          {/* Root directory row */}
          <div
            class={styles.node}
            style={{ 'padding-left': '8px' }}
            onClick={toggleRoot}
            data-testid="tree-root-node"
          >
            <Show
              when={rootExpanded()}
              fallback={<ChevronRight size={16} class={styles.chevron} />}
            >
              <ChevronDown size={16} class={styles.chevron} />
            </Show>
            <Folder size={14} class={styles.folderIcon} />
            <span class={styles.nodeName}>{rootDisplayName()}</span>
            <div class={styles.nodeActions}>
              {renderTreeContextMenu({
                path: rootPath(),
                isDir: true,
                rootPath: rootPath(),
                homeDir: props.homeDir,
                onMention: props.onMention,
                onOpenTerminal: props.onOpenTerminal,
              })}
            </div>
          </div>
          <Show when={rootExpanded()}>
            <Show
              when={rootChildren().length > 0}
              fallback={<div class={styles.emptyState}>Empty directory</div>}
            >
              <For each={rootChildren()}>
                {node => (
                  <TreeNode
                    node={node}
                    workerId={props.workerId}
                    showFiles={props.showFiles ?? false}
                    selectedPath={props.selectedPath}
                    onSelect={props.onSelect}
                    onFileOpen={props.onFileOpen}
                    onMention={props.onMention}
                    onOpenTerminal={props.onOpenTerminal}
                    depth={1}
                    scrollContainer={treeRef}
                    rootPath={rootPath()}
                    homeDir={props.homeDir}
                    isNodeExpanded={isNodeExpanded}
                    setNodeExpanded={setNodeExpanded}
                  />
                )}
              </For>
            </Show>
          </Show>
        </Show>
      </div>
    </div>
  )
}
