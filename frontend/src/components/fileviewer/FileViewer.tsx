import type { Component } from 'solid-js'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import type { FileViewMode } from '~/lib/fileType'
import type { FileDiffBase, FileViewMode as TabFileViewMode } from '~/stores/tab.store'
import { createEffect, createSignal, on, Show } from 'solid-js'
import { fileClient, gitClient } from '~/api/clients'
import { apiCallTimeout } from '~/api/transport'
import { DiffView, rawDiffToHunks } from '~/components/chat/diffUtils'
import { GitFileRef } from '~/generated/leapmux/v1/git_pb'
import { detectFileViewMode, isImageExtension } from '~/lib/fileType'
import { DiffModeToolbar } from './DiffModeToolbar'
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
  onQuote?: (text: string, startLine?: number, endLine?: number) => void
  onMention?: () => void
  /** Tab-level diff mode. */
  fileViewMode?: TabFileViewMode
  fileDiffBase?: FileDiffBase
  hasStagedAndUnstaged?: boolean
  onFileViewModeChange?: (mode: TabFileViewMode) => void
  onFileDiffBaseChange?: (base: FileDiffBase) => void
}> = (props) => {
  const [loading, setLoading] = createSignal(true)
  const [error, setError] = createSignal<string | null>(null)
  const [content, setContent] = createSignal<Uint8Array | null>(null)
  const [totalSize, setTotalSize] = createSignal(0)
  const [viewMode, setViewMode] = createSignal<FileViewMode>('text')
  const [imageTooLarge, setImageTooLarge] = createSignal(false)

  // Diff mode state
  const [diffOldContent, setDiffOldContent] = createSignal<string | null>(null)
  const [diffNewContent, setDiffNewContent] = createSignal<string | null>(null)
  const [diffLoading, setDiffLoading] = createSignal(false)

  const isDiffMode = () => props.fileViewMode === 'unified-diff' || props.fileViewMode === 'split-diff'
  const isRefMode = () => props.fileViewMode === 'head' || props.fileViewMode === 'staged'

  // Load working copy content.
  createEffect(on(
    () => [props.workerId, props.filePath] as const,
    async ([workerId, filePath]) => {
      setLoading(true)
      setError(null)
      setContent(null)
      setImageTooLarge(false)

      try {
        const statResp = await fileClient.statFile({ workerId, path: filePath })
        const fileSize = Number(statResp.info?.size ?? 0n)
        setTotalSize(fileSize)

        if (isImageExtension(filePath)) {
          if (fileSize > MAX_FILE_SIZE) {
            setImageTooLarge(true)
            setViewMode('image')
            setLoading(false)
            return
          }
        }

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

  // Load diff content when in diff or ref mode.
  createEffect(on(
    () => [props.workerId, props.filePath, props.fileViewMode, props.fileDiffBase] as const,
    async ([workerId, filePath, fvMode, diffBase]) => {
      if (!fvMode || fvMode === 'working') {
        setDiffOldContent(null)
        setDiffNewContent(null)
        return
      }

      setDiffLoading(true)
      try {
        if (fvMode === 'head' || fvMode === 'staged') {
          const ref = fvMode === 'head' ? GitFileRef.HEAD : GitFileRef.STAGED
          const resp = await gitClient.readGitFile({ workerId, path: filePath, ref }, apiCallTimeout())
          if (resp.exists) {
            setDiffOldContent(new TextDecoder().decode(resp.content))
          }
          else {
            setDiffOldContent(null)
          }
        }
        else if (fvMode === 'unified-diff' || fvMode === 'split-diff') {
          // Fetch HEAD version.
          const headResp = await gitClient.readGitFile({
            workerId,
            path: filePath,
            ref: GitFileRef.HEAD,
          }, apiCallTimeout())
          const headContent = headResp.exists ? new TextDecoder().decode(headResp.content) : ''
          setDiffOldContent(headContent)

          // Fetch the "new" version.
          if (diffBase === 'head-vs-staged') {
            const stagedResp = await gitClient.readGitFile({
              workerId,
              path: filePath,
              ref: GitFileRef.STAGED,
            }, apiCallTimeout())
            setDiffNewContent(stagedResp.exists ? new TextDecoder().decode(stagedResp.content) : '')
          }
          else {
            // head-vs-working: use the working copy content already loaded.
            const bytes = content()
            setDiffNewContent(bytes ? new TextDecoder().decode(bytes) : '')
          }
        }
      }
      catch {
        // If git read fails, clear diff content.
        setDiffOldContent(null)
        setDiffNewContent(null)
      }
      finally {
        setDiffLoading(false)
      }
    },
  ))

  const isTruncated = () => content() !== null && content()!.length < totalSize()

  const showToolbar = () => props.fileViewMode !== undefined

  const diffHunks = () => {
    const old = diffOldContent()
    const nw = diffNewContent()
    if (old === null || nw === null)
      return []
    return rawDiffToHunks(old, nw)
  }

  const diffViewPref = (): DiffViewPreference =>
    props.fileViewMode === 'split-diff' ? 'split' : 'unified'

  return (
    <div class={styles.container}>
      <Show when={showToolbar()}>
        <DiffModeToolbar
          mode={props.fileViewMode!}
          diffBase={props.fileDiffBase}
          hasStagedAndUnstaged={props.hasStagedAndUnstaged}
          onModeChange={mode => props.onFileViewModeChange?.(mode)}
          onDiffBaseChange={base => props.onFileDiffBaseChange?.(base)}
        />
      </Show>
      <div class={styles.content}>
        <Show when={loading() || diffLoading()}>
          <div class={styles.loadingState}>Loading...</div>
        </Show>
        <Show when={error()}>
          <div class={styles.errorState}>{error()}</div>
        </Show>
        <Show when={!loading() && !error()}>
          {/* Ref mode: show HEAD or staged content as text */}
          <Show when={isRefMode() && !diffLoading() && diffOldContent() !== null}>
            <TextFileView
              content={new TextEncoder().encode(diffOldContent()!)}
              filePath={props.filePath}
              totalSize={diffOldContent()!.length}
              onQuote={props.onQuote}
              onMention={props.onMention}
            />
          </Show>
          <Show when={isRefMode() && !diffLoading() && diffOldContent() === null}>
            <div class={styles.errorState}>File does not exist at this ref</div>
          </Show>

          {/* Diff mode: show unified or split diff */}
          <Show when={isDiffMode() && !diffLoading() && diffOldContent() !== null && diffNewContent() !== null}>
            <DiffView
              hunks={diffHunks()}
              view={diffViewPref()}
              filePath={props.filePath}
              originalFile={diffOldContent() ?? undefined}
            />
          </Show>

          {/* Working mode or no mode: show normal file content */}
          <Show when={!isDiffMode() && !isRefMode()}>
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
                onQuote={props.onQuote}
                onMention={props.onMention}
              />
            </Show>
            <Show when={viewMode() === 'markdown' && content()}>
              <MarkdownFileView
                content={content()!}
                filePath={props.filePath}
                totalSize={totalSize()}
                displayMode={props.displayMode}
                onDisplayModeChange={props.onDisplayModeChange}
                onQuote={props.onQuote}
                onMention={props.onMention}
              />
            </Show>
            <Show when={viewMode() === 'image' && content() && !imageTooLarge()}>
              <ImageFileView
                content={content()!}
                filePath={props.filePath}
                totalSize={totalSize()}
                displayMode={props.displayMode}
                onDisplayModeChange={props.onDisplayModeChange}
                onQuote={props.onQuote}
                onMention={props.onMention}
              />
            </Show>
            <Show when={viewMode() === 'binary' && content()}>
              <HexView
                content={content()!}
                totalSize={totalSize()}
              />
            </Show>
          </Show>
        </Show>
      </div>
      <Show when={!loading() && !error() && !isDiffMode() && !isRefMode() && (totalSize() > 0 || isTruncated())}>
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
