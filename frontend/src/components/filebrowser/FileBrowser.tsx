import type { Component } from 'solid-js'
import type { FileInfo } from '~/generated/leapmux/v1/file_pb'
import { createMemo, For, Show } from 'solid-js'
import { detectFlavor, parentDirectory, pathSegments, sep } from '~/lib/paths'
import { emptyState } from '~/styles/shared.css'
import * as styles from './FileBrowser.css'

interface FileBrowserProps {
  currentPath: string
  entries: FileInfo[]
  loading: boolean
  error: string | null
  onNavigate: (path: string) => void
  onFileSelect: (entry: FileInfo) => void
}

function formatSize(bytes: bigint): string {
  const n = Number(bytes)
  if (n < 1024)
    return `${n} B`
  if (n < 1024 * 1024)
    return `${(n / 1024).toFixed(1)} K`
  if (n < 1024 * 1024 * 1024)
    return `${(n / (1024 * 1024)).toFixed(1)} M`
  return `${(n / (1024 * 1024 * 1024)).toFixed(1)} G`
}

function sortedEntries(entries: FileInfo[]): FileInfo[] {
  return entries.toSorted((a, b) => {
    if (a.isDir !== b.isDir)
      return a.isDir ? -1 : 1
    return a.name.localeCompare(b.name)
  })
}

export const FileBrowser: Component<FileBrowserProps> = (props) => {
  const handleClick = (entry: FileInfo) => {
    if (entry.isDir) {
      props.onNavigate(entry.path)
    }
    else {
      props.onFileSelect(entry)
    }
  }

  const flavor = createMemo(() => detectFlavor(props.currentPath))
  const segments = createMemo(() => pathSegments(props.currentPath, flavor()))

  return (
    <div class={styles.container}>
      <nav aria-label="Breadcrumb" class={styles.pathBar}>
        <ol class={styles.breadcrumbList}>
          <li>
            <button class={styles.pathSegment} onClick={() => props.onNavigate('.')}>
              ~
            </button>
          </li>
          <For each={segments()}>
            {segment => (
              <li>
                <span class={styles.pathSeparator}>{sep(flavor())}</span>
                <button
                  class={styles.pathSegment}
                  onClick={() => props.onNavigate(segment.path)}
                >
                  {segment.name}
                </button>
              </li>
            )}
          </For>
        </ol>
      </nav>

      <Show when={props.loading}>
        <div class={styles.loadingState}>Loading...</div>
      </Show>

      <Show when={props.error}>
        <div class={styles.errorState}>{props.error}</div>
      </Show>

      <Show when={!props.loading && !props.error}>
        <Show
          when={props.entries.length > 0}
          fallback={<div class={emptyState}>Empty directory</div>}
        >
          <div class={styles.fileList}>
            <Show when={props.currentPath !== '.'}>
              <div
                class={styles.fileItem}
                onClick={() => {
                  props.onNavigate(parentDirectory(props.currentPath, flavor()) || '.')
                }}
              >
                <span class={styles.dirIcon}>..</span>
                <span class={styles.fileName}>..</span>
              </div>
            </Show>
            <For each={sortedEntries(props.entries)}>
              {entry => (
                <div
                  class={styles.fileItem}
                  onClick={() => handleClick(entry)}
                >
                  <span class={entry.isDir ? styles.dirIcon : styles.fileIcon}>
                    {entry.isDir ? 'D' : 'F'}
                  </span>
                  <span class={styles.fileName}>{entry.name}</span>
                  <Show when={!entry.isDir}>
                    <span class={styles.fileSize}>{formatSize(entry.size)}</span>
                  </Show>
                </div>
              )}
            </For>
          </div>
        </Show>
      </Show>
    </div>
  )
}
