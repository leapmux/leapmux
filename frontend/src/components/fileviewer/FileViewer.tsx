import type { Component } from 'solid-js'
import type { FileViewMode } from '~/lib/fileType'
import { createEffect, createSignal, on, Show } from 'solid-js'
import { fileClient } from '~/api/clients'
import { detectFileViewMode, isImageExtension } from '~/lib/fileType'
import * as styles from './FileViewer.css'
import { HexView } from './HexView'
import { ImageFileView } from './ImageFileView'
import { MarkdownFileView } from './MarkdownFileView'
import { TextFileView } from './TextFileView'

const MAX_FILE_SIZE = 256 * 1024 // 256 KiB

function formatSize(bytes: number): string {
  if (bytes < 1024)
    return `${bytes} B`
  const kb = bytes / 1024
  if (kb < 1024)
    return `${kb.toFixed(1)} KiB`
  const mb = kb / 1024
  return `${mb.toFixed(1)} MiB`
}

export const FileViewer: Component<{
  workerId: string
  filePath: string
  displayMode?: string
  onDisplayModeChange?: (mode: string) => void
}> = (props) => {
  const [loading, setLoading] = createSignal(true)
  const [error, setError] = createSignal<string | null>(null)
  const [content, setContent] = createSignal<Uint8Array | null>(null)
  const [totalSize, setTotalSize] = createSignal(0)
  const [viewMode, setViewMode] = createSignal<FileViewMode>('text')
  const [imageTooLarge, setImageTooLarge] = createSignal(false)

  createEffect(on(
    () => [props.workerId, props.filePath] as const,
    async ([workerId, filePath]) => {
      setLoading(true)
      setError(null)
      setContent(null)
      setImageTooLarge(false)

      try {
        // First stat the file to get its size
        const statResp = await fileClient.statFile({ workerId, path: filePath })
        const fileSize = Number(statResp.info?.size ?? 0n)
        setTotalSize(fileSize)

        // For images, check size before reading
        if (isImageExtension(filePath)) {
          if (fileSize > MAX_FILE_SIZE) {
            setImageTooLarge(true)
            setViewMode('image')
            setLoading(false)
            return
          }
        }

        // Read the file content (up to 256 KiB)
        const readResp = await fileClient.readFile({
          workerId,
          path: filePath,
          limit: BigInt(MAX_FILE_SIZE),
        })
        const bytes = new Uint8Array(readResp.content)
        setContent(bytes)
        setTotalSize(Number(readResp.totalSize))
        setViewMode(detectFileViewMode(filePath, bytes))
      }
      catch (err) {
        setError(err instanceof Error ? err.message : 'Failed to load file')
      }
      finally {
        setLoading(false)
      }
    },
  ))

  const isTruncated = () => content() !== null && content()!.length < totalSize()

  return (
    <div class={styles.container}>
      <div class={styles.content}>
        <Show when={loading()}>
          <div class={styles.loadingState}>Loading...</div>
        </Show>
        <Show when={error()}>
          <div class={styles.errorState}>{error()}</div>
        </Show>
        <Show when={!loading() && !error()}>
          <Show when={imageTooLarge()}>
            <div class={styles.imageSizeError}>
              {`Image too large to preview (${formatSize(totalSize())})`}
            </div>
          </Show>
          <Show when={viewMode() === 'text' && content()}>
            <TextFileView
              content={content()!}
              filePath={props.filePath}
              totalSize={totalSize()}
            />
          </Show>
          <Show when={viewMode() === 'markdown' && content()}>
            <MarkdownFileView
              content={content()!}
              filePath={props.filePath}
              totalSize={totalSize()}
              displayMode={props.displayMode}
              onDisplayModeChange={props.onDisplayModeChange}
            />
          </Show>
          <Show when={viewMode() === 'image' && content() && !imageTooLarge()}>
            <ImageFileView
              content={content()!}
              filePath={props.filePath}
              totalSize={totalSize()}
              displayMode={props.displayMode}
              onDisplayModeChange={props.onDisplayModeChange}
            />
          </Show>
          <Show when={viewMode() === 'binary' && content()}>
            <HexView
              content={content()!}
              totalSize={totalSize()}
            />
          </Show>
        </Show>
      </div>
      <Show when={!loading() && !error() && (totalSize() > 0 || isTruncated())}>
        <div class={styles.statusBar}>
          <Show when={isTruncated()}>
            <span class={styles.truncationWarning}>
              {`Truncated at ${formatSize(MAX_FILE_SIZE)}`}
            </span>
          </Show>
          <Show when={totalSize() > 0}>
            <span class={styles.statusMeta}>{formatSize(totalSize())}</span>
          </Show>
        </div>
      </Show>
    </div>
  )
}
