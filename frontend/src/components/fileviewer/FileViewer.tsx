import type { Component } from 'solid-js'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import type { FileViewMode } from '~/lib/fileType'
import type { FileDiffBase, FileViewMode as TabFileViewMode } from '~/stores/tab.store'
import AtSign from 'lucide-solid/icons/at-sign'
import { createEffect, createMemo, createSignal, on, onCleanup, Show } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { diffAdded, diffRemoved } from '~/components/chat/diffStyles.css'
import { DiffView, rawDiffToHunks } from '~/components/chat/diffUtils'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import { GitFileRef } from '~/generated/leapmux/v1/git_pb'
import { detectFileViewMode, isImageExtension } from '~/lib/fileType'
import { DiffModeToolbar } from './DiffModeToolbar'
import * as styles from './FileViewer.css'
import { TOOLBAR_CLEARANCE_PX } from './FileViewer.css'
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
        const statResp = await workerRpc.statFile(workerId, { workerId, path: filePath })
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

        const readResp = await workerRpc.readFile(workerId, {
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
          const resp = await workerRpc.readGitFile(workerId, { workerId, path: filePath, ref })
          if (resp.exists) {
            setDiffOldContent(new TextDecoder().decode(resp.content))
          }
          else {
            setDiffOldContent(null)
          }
        }
        else if (fvMode === 'unified-diff' || fvMode === 'split-diff') {
          // Fetch HEAD version.
          const headResp = await workerRpc.readGitFile(workerId, {
            workerId,
            path: filePath,
            ref: GitFileRef.HEAD,
          })
          const headContent = headResp.exists ? new TextDecoder().decode(headResp.content) : ''
          setDiffOldContent(headContent)

          // Fetch the "new" version.
          if (diffBase === 'head-vs-staged') {
            const stagedResp = await workerRpc.readGitFile(workerId, {
              workerId,
              path: filePath,
              ref: GitFileRef.STAGED,
            })
            setDiffNewContent(stagedResp.exists ? new TextDecoder().decode(stagedResp.content) : '')
          }
          else {
            // head-vs-working: read working copy from disk to avoid race
            // with the content-loading effect that runs concurrently.
            const workingResp = await workerRpc.readFile(workerId, {
              workerId,
              path: filePath,
              limit: BigInt(MAX_FILE_SIZE),
            })
            setDiffNewContent(new TextDecoder().decode(workingResp.content))
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

  // Show the outer (floating) mention button for all modes except when
  // ViewToggle handles it (markdown and SVG image views have the mention
  // button integrated into their toggle control which already floats).
  const hasViewToggle = () =>
    !isDiffMode() && !isRefMode()
    && (viewMode() === 'markdown' || (viewMode() === 'image' && props.filePath.toLowerCase().endsWith('.svg')))
  const showOuterMention = () => !!props.onMention && !hasViewToggle()

  const diffHunks = createMemo(() => {
    const old = diffOldContent()
    const nw = diffNewContent()
    if (old === null || nw === null)
      return []
    return rawDiffToHunks(old, nw)
  })

  const diffViewPref = (): DiffViewPreference =>
    props.fileViewMode === 'split-diff' ? 'split' : 'unified'

  let contentRef: HTMLDivElement | undefined

  // Include the view mode in the scroll key so each mode has independent scroll.
  // eslint-disable-next-line solid/reactivity -- Read once; workerId and filePath are stable for the component's lifetime
  const scrollStoragePrefix = `leapmux:fileScroll:${props.workerId}:${props.filePath}`
  const scrollStorageKey = () => `${scrollStoragePrefix}:${props.fileViewMode ?? 'working'}`

  // Save scroll position when the view mode changes or on unmount.
  let lastSavedKey: string | undefined
  const saveScrollPosition = () => {
    const key = lastSavedKey ?? scrollStorageKey()
    if (contentRef && contentRef.scrollTop > 0) {
      sessionStorage.setItem(key, String(contentRef.scrollTop))
    }
    else {
      sessionStorage.removeItem(key)
    }
  }
  onCleanup(saveScrollPosition)

  // Restore saved scroll position or scroll to first diff entry.
  createEffect(on(
    () => [loading(), diffLoading(), isDiffMode(), diffHunks().length, props.fileViewMode] as const,
    ([fileLoading, dLoading, diffMode, hunkCount, _fvMode]) => {
      if (fileLoading || dLoading || !contentRef)
        return

      // Save scroll for the previous mode before switching.
      if (lastSavedKey && lastSavedKey !== scrollStorageKey())
        saveScrollPosition()
      lastSavedKey = scrollStorageKey()

      const savedStr = sessionStorage.getItem(scrollStorageKey())
      if (savedStr != null) {
        const scrollTop = Number(savedStr)
        sessionStorage.removeItem(scrollStorageKey())
        requestAnimationFrame(() => {
          if (contentRef)
            contentRef.scrollTop = scrollTop
        })
        return
      }

      if (!diffMode || hunkCount === 0)
        return

      requestAnimationFrame(() => {
        const container = contentRef!
        const diffElements = [...container.querySelectorAll(`.${diffAdded}, .${diffRemoved}`)]
        if (diffElements.length === 0)
          return

        const firstEl = diffElements[0] as HTMLElement
        const lastEl = diffElements.at(-1)! as HTMLElement
        const containerRect = container.getBoundingClientRect()
        const firstRect = firstEl.getBoundingClientRect()
        const lastRect = lastEl.getBoundingClientRect()

        // Calculate the midpoint of the diff area relative to scroll.
        const diffTop = firstRect.top - containerRect.top + container.scrollTop
        const diffBottom = lastRect.bottom - containerRect.top + container.scrollTop
        const diffMid = (diffTop + diffBottom) / 2
        const viewportHeight = container.clientHeight

        // Center the midpoint, but ensure the first diff line stays visible.
        let targetScroll = diffMid - viewportHeight / 2
        targetScroll = Math.min(targetScroll, diffTop)
        targetScroll = Math.max(0, targetScroll)
        container.scrollTop = targetScroll
      })
    },
  ))

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
      <Show when={showOuterMention()}>
        <div class={styles.viewToggle}>
          <Tooltip text="Mention in the chat" ariaLabel>
            <button
              class={styles.viewToggleButton}
              onClick={() => props.onMention?.()}
              data-testid="file-mention-button"
            >
              <Icon icon={AtSign} size="sm" />
            </button>
          </Tooltip>
        </div>
      </Show>
      <div class={`${styles.content}${showToolbar() || showOuterMention() ? ` ${styles.hasFloatingToolbar}` : ''}`} ref={contentRef}>
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
                topOffset={showToolbar() || showOuterMention() ? TOOLBAR_CLEARANCE_PX : 0}
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
