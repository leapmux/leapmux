import type { Component } from 'solid-js'
import type { FileInfo } from '~/generated/leapmux/v1/file_pb'
import AtSign from 'lucide-solid/icons/at-sign'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import File from 'lucide-solid/icons/file'
import Folder from 'lucide-solid/icons/folder'
import { createEffect, createSignal, For, Show, untrack } from 'solid-js'
import { fileClient } from '~/api/clients'
import { tildify } from '~/components/chat/messageUtils'
import * as styles from './DirectoryTree.css'

export interface DirectoryTreeProps {
  workerId: string
  showFiles?: boolean
  selectedPath: string
  onSelect: (path: string) => void
  onFileOpen?: (path: string) => void
  onMention?: (path: string) => void
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

const TreeNode: Component<{
  node: TreeNodeData
  workerId: string
  showFiles: boolean
  selectedPath: string
  onSelect: (path: string) => void
  onFileOpen?: (path: string) => void
  onMention?: (path: string) => void
  depth: number
  scrollContainer?: HTMLDivElement
}> = (props) => {
  const [expanded, setExpanded] = createSignal(false)
  const [children, setChildren] = createSignal<TreeNodeData[]>([])
  const [loading, setLoading] = createSignal(false)
  const [loaded, setLoaded] = createSignal(false)
  let wrapperRef!: HTMLDivElement

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
    setExpanded(willExpand)
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
          setExpanded(true)
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
      setExpanded(true)
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
        <Show when={props.onMention}>
          <div class={styles.nodeActions}>
            <button
              class={styles.nodeActionButton}
              onClick={(e) => {
                e.stopPropagation()
                props.onMention?.(props.node.path)
              }}
              title="Mention in the chat"
              data-testid="tree-mention-button"
            >
              <AtSign size={12} />
            </button>
          </div>
        </Show>
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
              depth={props.depth + 1}
              scrollContainer={props.scrollContainer}
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
                  depth={0}
                  scrollContainer={treeRef}
                />
              )}
            </For>
          </Show>
        </Show>
      </div>
    </div>
  )
}
