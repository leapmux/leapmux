import type { Component } from 'solid-js'
import type { UnsupportedReason } from './UnsupportedFileView'
import type { ViewMode } from '~/components/fileviewer/ViewToggle'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import type { GitFileStatusEntry } from '~/generated/leapmux/v1/common_pb'
import type { FileViewMode } from '~/lib/fileType'
import type { FileDiffBase, FileViewMode as TabFileViewMode } from '~/stores/tab.types'
import { batch, createEffect, createMemo, createSignal, Match, on, onCleanup, Show, Switch } from 'solid-js'
import { isTauriApp } from '~/api/platformBridge'
import * as workerRpc from '~/api/workerRpc'
import { DiffView, rawDiffToHunks } from '~/components/chat/diff'
import { diffAdded, diffRemoved } from '~/components/chat/diff/diffStyles.css'
import { FileActionsMenu } from '~/components/common/FileActionsMenu'
import { createFileSaveActions } from '~/components/common/fileSaveActions'
import { showWarnToast } from '~/components/common/Toast'
import { usePreferences } from '~/context/PreferencesContext'
import { GitFileStatusCode } from '~/generated/leapmux/v1/common_pb'
import { GitFileRef } from '~/generated/leapmux/v1/git_pb'
import { PREFIX_FILE_SCROLL, sessionStorageGet, sessionStorageRemove, sessionStorageSet } from '~/lib/browserStorage'
import { detectFileViewModeFromExt, isImageExt, isLikelyBinaryExt, isSvgExt } from '~/lib/fileType'
import { formatBytes } from '~/lib/formatBytes'
import { basename, detectFlavor, extname } from '~/lib/paths'
import { DiffModeToolbar } from './DiffModeToolbar'
import * as styles from './FileViewer.css'
import { TOOLBAR_CLEARANCE_PX } from './FileViewer.css'
import { HexView } from './HexView'
import { ImageFileView } from './ImageFileView'
import { MarkdownFileView } from './MarkdownFileView'
import { TextFileView } from './TextFileView'
import { UnsupportedFileView } from './UnsupportedFileView'
import { ViewToggle } from './ViewToggle'

const MAX_FILE_SIZE = 256 * 1024 // 256 KiB

/**
 * Build the binary-file diff message from the file's git-status entry.
 *
 * We can't byte-compare directly — `readFile` only delivers the first
 * 256 KiB of the working copy and we'd report a false "identical" on
 * anything that diverges later. Git already tracks this comparison
 * accurately for the index/working tree and is the authoritative
 * source.
 *
 * Returns `null` when there's no status entry (file is not in the
 * repo, or unchanged from HEAD) — callers treat that as "no diff to
 * render". Otherwise returns a sentence describing the change in
 * terms of the diff base.
 */
function binaryDiffMessageFromGit(
  entry: GitFileStatusEntry | undefined,
  diffBase: FileDiffBase | undefined,
  name: string,
): string | null {
  if (!entry)
    return null
  const newSide = diffBase === 'head-vs-staged' ? 'staged copy' : 'working copy'

  if (diffBase === 'head-vs-staged') {
    // HEAD vs INDEX: only the staged-side codes matter.
    switch (entry.stagedStatus) {
      case GitFileStatusCode.UNSPECIFIED:
        return null
      case GitFileStatusCode.ADDED:
        return `Binary file ${name} was added in the ${newSide}.`
      case GitFileStatusCode.DELETED:
        return `Binary file ${name} was deleted in the ${newSide}.`
      default:
        return `Binary file ${name} differs in the ${newSide}.`
    }
  }
  // head-vs-working (default): HEAD vs the on-disk file. Combine both
  // status sides — staged-ADDED means the file is newly created in
  // INDEX (and thus working), unstaged-DELETED means the working file
  // is gone even if INDEX still has it.
  if (entry.unstagedStatus === GitFileStatusCode.UNTRACKED
    || entry.stagedStatus === GitFileStatusCode.ADDED) {
    return `Binary file ${name} was added in the ${newSide}.`
  }
  if (entry.unstagedStatus === GitFileStatusCode.DELETED
    || (entry.stagedStatus === GitFileStatusCode.DELETED
      && entry.unstagedStatus === GitFileStatusCode.UNSPECIFIED)) {
    return `Binary file ${name} was deleted in the ${newSide}.`
  }
  return `Binary file ${name} differs in the ${newSide}.`
}

/** Parse the wrapped scroll-position value, returning null when stale or malformed. */
function readScrollEntry(key: string): number | null {
  const scroll = sessionStorageGet<number>(key)
  return scroll !== undefined && Number.isFinite(scroll) ? scroll : null
}

export const FileViewer: Component<{
  workerId: string
  filePath: string
  displayMode?: string
  onDisplayModeChange?: (mode: string) => void
  onQuote?: (text: string, startLine?: number, endLine?: number) => void
  onMention?: () => void
  /**
   * Worker-root path, threaded through to the FileActionsMenu so its
   * "Copy relative path" item can show. Omit to hide that menu entry.
   */
  rootPath?: string
  /** Worker home directory; used together with `rootPath` for tilde collapse. */
  homeDir?: string
  /** Tab-level diff mode. */
  fileViewMode?: TabFileViewMode
  fileDiffBase?: FileDiffBase
  hasStagedAndUnstaged?: boolean
  /**
   * The file's current git-status entry, if any. Drives two
   * behaviors:
   *   - the Unified/Split toolbar buttons are only shown when this is
   *     defined (a file with no git diff has nothing to render);
   *   - for binary files, the diff verdict (identical/differs/added/
   *     deleted) is derived from this entry instead of attempted byte
   *     comparison (our readFile only sees the first 256 KiB).
   */
  gitFileStatus?: GitFileStatusEntry
  onFileViewModeChange?: (mode: TabFileViewMode) => void
  onFileDiffBaseChange?: (base: FileDiffBase) => void
}> = (props) => {
  const [loading, setLoading] = createSignal(true)
  const [error, setError] = createSignal<string | null>(null)
  const [content, setContent] = createSignal<Uint8Array | null>(null)
  const [totalSize, setTotalSize] = createSignal(0)
  const [viewMode, setViewMode] = createSignal<FileViewMode>('text')
  // Per-session signal flipped by the "Show anyway" button. Bytes
  // (when not already cached) are fetched lazily.
  const [showAnyway, setShowAnyway] = createSignal(false)
  const [loadingAnyway, setLoadingAnyway] = createSignal(false)
  const isDesktop = isTauriApp()
  const prefs = usePreferences()

  const flavor = createMemo(() => detectFlavor(props.filePath))
  // Single source of truth for the file extension. Every is*Ext check
  // and every detectFileViewMode call below reads through this memo, so
  // `extname` runs at most once per filePath change instead of once
  // per consumer.
  const ext = createMemo(() => extname(props.filePath))

  // Render/source/split mode for the markdown and SVG-image viewers,
  // lifted up here so the toggle row and the FileActionsMenu can share
  // a single top-right floating slot. The tab store is the source of
  // truth — emit through `onDisplayModeChange` and read back from
  // `props.displayMode` on the next tick.
  const displayMode = (): ViewMode => (props.displayMode as ViewMode | undefined) ?? 'render'

  // Shared save/download workflow — single instance backs both the
  // floating FileActionsMenu and the UnsupportedFileView buttons so
  // their busy state is unified (clicking "Save as" in either disables
  // it in the other).
  const saveActions = createFileSaveActions({
    workerId: () => props.workerId,
    path: () => props.filePath,
    flavor,
  })

  // Derived: an image file too large to preview inline. No bytes are
  // fetched up front for this case; "Show anyway" triggers the first
  // ReadFile call. Split so the extension check only re-runs on
  // filePath change — the totalSize signal updates more frequently.
  const isImagePath = createMemo(() => isImageExt(ext()))
  const oversizeImage = createMemo(() => isImagePath() && totalSize() > MAX_FILE_SIZE)

  // Diff mode state
  const [diffOldContent, setDiffOldContent] = createSignal<string | null>(null)
  const [diffNewContent, setDiffNewContent] = createSignal<string | null>(null)
  const [diffLoading, setDiffLoading] = createSignal(false)

  const isDiffMode = () => props.fileViewMode === 'unified-diff' || props.fileViewMode === 'split-diff'
  const isRefMode = () => props.fileViewMode === 'head' || props.fileViewMode === 'staged'

  // Whether a diff exists to render. A file with no git-status entry
  // is either outside the repo or unchanged from HEAD — neither case
  // has a meaningful unified/split diff. The toolbar hides those modes
  // and an auto-fallback below switches off any stuck diff selection.
  const diffAvailable = () => Boolean(props.gitFileStatus)

  // Binary-file diff message from git status (computed only when the
  // file is binary AND in a diff mode AND has a status entry). Used
  // by the indicator that replaces DiffView for binary diffs — we
  // can't show a meaningful unified/split view of binary bytes.
  const binaryDiffMessage = createMemo<string | null>(() => {
    if (!isDiffMode() || viewMode() !== 'binary')
      return null
    return binaryDiffMessageFromGit(
      props.gitFileStatus,
      props.fileDiffBase,
      basename(props.filePath, flavor()),
    )
  })

  // If the file has no diff available (no git-status entry), don't
  // leave the tab stuck in a diff mode whose buttons are hidden — fall
  // back to working mode so the user sees the working copy.
  createEffect(() => {
    if (!diffAvailable() && isDiffMode())
      props.onFileViewModeChange?.('working')
  })

  // Promise of the current load's bytes (or null when the load
  // short-circuited — empty workerId/path, binary, or oversize image).
  // Captured by the load effect; the diff effect's head-vs-working
  // branch awaits it to avoid issuing a duplicate readFile for the
  // same range.
  let currentBytesPromise: Promise<Uint8Array | null> = Promise.resolve(null)

  // Shared first-chunk fetch used by the working-copy load, the diff
  // effect's head-vs-working fallback, and "Show anyway".
  const fetchFirstChunk = (workerId: string, filePath: string) =>
    workerRpc.readFile(workerId, {
      workerId,
      path: filePath,
      limit: BigInt(MAX_FILE_SIZE),
    })

  // Load working copy content.
  createEffect(on(
    () => [props.workerId, props.filePath] as const,
    async ([workerId, filePath]) => {
      let resolveBytes!: (bytes: Uint8Array | null) => void
      currentBytesPromise = new Promise<Uint8Array | null>((r) => {
        resolveBytes = r
      })

      setLoading(true)
      setError(null)
      setContent(null)
      setShowAnyway(false)
      setLoadingAnyway(false)

      // Tab rehydration on refresh momentarily mounts FileViewer with
      // an empty workerId/filePath before the workspace-restore RPC
      // fills them in. Skip the load here so we don't fire a statFile
      // with `path=""` — the worker's path validator rejects that as
      // "access denied", which then sticks even after props update.
      // Stay in the loading state; the effect re-fires when the real
      // path arrives.
      if (!workerId || !filePath) {
        resolveBytes(null)
        return
      }

      const fileExt = extname(filePath)

      try {
        // Known-binary: only need the size to populate the unsupported
        // card. Never read bytes.
        if (isLikelyBinaryExt(fileExt)) {
          const statResp = await workerRpc.statFile(workerId, { workerId, path: filePath })
          batch(() => {
            setTotalSize(Number(statResp.info?.size ?? 0n))
            setViewMode('binary')
            setLoading(false)
          })
          resolveBytes(null)
          return
        }

        // Single readFile for everything else (text, markdown, source,
        // no-extension, image). `readFile.totalSize` removes the need
        // for an upfront statFile, and `metaOnlyIfTruncated` lets the
        // worker short-circuit oversize images — server returns empty
        // content + totalSize so we don't pay for 256 KiB of bytes
        // we'd refuse to render. Saves one round-trip on every image
        // and never wastes bytes on oversize ones.
        const readResp = await workerRpc.readFile(workerId, {
          workerId,
          path: filePath,
          limit: BigInt(MAX_FILE_SIZE),
          metaOnlyIfTruncated: isImageExt(fileExt),
        })
        const respTotalSize = Number(readResp.totalSize)
        // Oversize image: server skipped the bytes; show the unsupported
        // card with size and no preview.
        if (isImageExt(fileExt) && respTotalSize > MAX_FILE_SIZE) {
          batch(() => {
            setTotalSize(respTotalSize)
            setViewMode('image')
            setLoading(false)
          })
          resolveBytes(null)
          return
        }
        const bytes = new Uint8Array(readResp.content)
        batch(() => {
          setContent(bytes)
          setTotalSize(respTotalSize)
          setViewMode(detectFileViewModeFromExt(fileExt, bytes))
        })
        resolveBytes(bytes)
      }
      catch (err) {
        setError(err instanceof Error ? err.message : 'Failed to load file')
        resolveBytes(null)
      }
      finally {
        setLoading(false)
      }
    },
  ))

  // Diff source mode — collapses `unified-diff` / `split-diff` into a
  // single value keyed by `fileDiffBase` so toggling the view (unified
  // vs. split) doesn't re-fire the fetch effect. The diff bytes only
  // depend on workerId / filePath / diffBase; the view choice is
  // render-only.
  type DiffSourceMode = 'working' | 'head' | 'staged' | 'diff:head-vs-working' | 'diff:head-vs-staged'
  const diffSourceMode = createMemo<DiffSourceMode>(() => {
    const fv = props.fileViewMode
    if (!fv || fv === 'working')
      return 'working'
    if (fv === 'head' || fv === 'staged')
      return fv
    const base = props.fileDiffBase === 'head-vs-staged' ? 'head-vs-staged' : 'head-vs-working'
    return `diff:${base}`
  })

  // Load diff content when in diff or ref mode.
  createEffect(on(
    () => [props.workerId, props.filePath, diffSourceMode()] as const,
    async ([workerId, filePath, mode]) => {
      if (mode === 'working') {
        setDiffOldContent(null)
        setDiffNewContent(null)
        return
      }

      setDiffLoading(true)
      try {
        if (mode === 'head' || mode === 'staged') {
          const ref = mode === 'head' ? GitFileRef.HEAD : GitFileRef.STAGED
          const resp = await workerRpc.readGitFile(workerId, { workerId, path: filePath, ref })
          if (resp.exists) {
            setDiffOldContent(new TextDecoder().decode(resp.content))
          }
          else {
            setDiffOldContent(null)
          }
        }
        else {
          // Binary files render the git-status-driven indicator
          // instead of decoded-text DiffView — skip the fetch entirely
          // for the common (extension-based) binary case. Unknown-
          // extension binaries still pay the wasted fetch since the
          // sniff runs in the load effect, but the render path won't
          // display the decoded garbage.
          if (isLikelyBinaryExt(extname(filePath))) {
            setDiffOldContent(null)
            setDiffNewContent(null)
            return
          }
          // HEAD ref runs in parallel with whatever the "new" side
          // needs — they target the same worker but don't depend on
          // each other.
          const headPromise = workerRpc.readGitFile(workerId, {
            workerId,
            path: filePath,
            ref: GitFileRef.HEAD,
          })
          const decoder = new TextDecoder()
          let newText: string
          if (mode === 'diff:head-vs-staged') {
            const stagedResp = await workerRpc.readGitFile(workerId, {
              workerId,
              path: filePath,
              ref: GitFileRef.STAGED,
            })
            newText = stagedResp.exists ? decoder.decode(stagedResp.content) : ''
          }
          else {
            // head-vs-working: prefer the working-copy bytes loaded by
            // the content-loading effect rather than issuing a second
            // readFile for the same range. Awaits load completion so
            // the bytes are stable. Falls back to a fresh readFile
            // when content() is null — binary files in diff mode, the
            // load effect's early-return path, etc.
            const bytes = await currentBytesPromise
            if (bytes) {
              newText = decoder.decode(bytes)
            }
            else {
              const workingResp = await fetchFirstChunk(workerId, filePath)
              newText = decoder.decode(workingResp.content)
            }
          }
          const headResp = await headPromise
          setDiffOldContent(headResp.exists ? decoder.decode(headResp.content) : '')
          setDiffNewContent(newText)
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

  const cardReason = createMemo<UnsupportedReason | null>(() => {
    if (oversizeImage())
      return 'oversize-image'
    if (viewMode() === 'binary')
      return 'binary'
    return null
  })

  // The unsupported-file view supersedes the inline viewers when the
  // file is non-renderable AND the user hasn't opted in via "Show
  // anyway". Only applies in working mode (diff/ref modes have their
  // own dispatch).
  const shouldShowUnsupported = () =>
    !loading() && !error()
    && (props.fileViewMode === undefined || props.fileViewMode === 'working')
    && !showAnyway()
    && cardReason() !== null

  const handleShowAnyway = async () => {
    // Snapshot worker/path at click time. If the user navigates away
    // during the in-flight readFile (the load effect will re-run and
    // reset `content`), discard our response — writing the old file's
    // bytes into the new file's signal would silently corrupt state.
    const workerId = props.workerId
    const filePath = props.filePath
    const propsStillCurrent = () =>
      props.workerId === workerId && props.filePath === filePath

    if (content() === null) {
      if (loadingAnyway())
        return
      setLoadingAnyway(true)
      try {
        const readResp = await fetchFirstChunk(workerId, filePath)
        if (!propsStillCurrent())
          return
        const bytes = new Uint8Array(readResp.content)
        batch(() => {
          setContent(bytes)
          setTotalSize(Number(readResp.totalSize))
        })
      }
      catch (err) {
        showWarnToast('Failed to load preview', err)
        return
      }
      finally {
        setLoadingAnyway(false)
      }
      if (!propsStillCurrent())
        return
    }
    setShowAnyway(true)
  }

  const showToolbar = () => props.fileViewMode !== undefined
  const showFloatingActions = () => !loading() && !error()
  const hasTopChrome = () => showToolbar() || showFloatingActions()

  // Markdown and SVG-image viewers expose a render/source/split toggle
  // beside the FileActionsMenu in the top-right floating slot. Other
  // modes show just the actions menu.
  const showViewToggle = () =>
    !loading() && !error() && !isDiffMode() && !isRefMode()
    && (viewMode() === 'markdown' || (viewMode() === 'image' && isSvgExt(ext())))

  const diffHunks = createMemo(() => {
    if (!isDiffMode())
      return []
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
  const scrollStoragePrefix = `${PREFIX_FILE_SCROLL}${props.workerId}:${props.filePath}`
  const scrollStorageKey = () => `${scrollStoragePrefix}:${props.fileViewMode ?? 'working'}`

  // Save scroll position when the view mode changes or on unmount.
  // Values are TTL-wrapped via `sessionStorageSet` so stale entries get
  // dropped by `runCleanup` on next app load.
  let lastSavedKey: string | undefined
  const saveScrollPosition = () => {
    const key = lastSavedKey ?? scrollStorageKey()
    if (contentRef && contentRef.scrollTop > 0)
      sessionStorageSet(key, contentRef.scrollTop)
    else
      sessionStorageRemove(key)
  }
  onCleanup(saveScrollPosition)

  // Restore saved scroll position or scroll to first diff entry.
  createEffect(on(
    () => [loading(), diffLoading(), isDiffMode(), diffHunks().length, props.fileViewMode] as const,
    ([fileLoading, dLoading, diffMode, hunkCount, _fvMode]) => {
      if (fileLoading || dLoading || !contentRef)
        return

      // Save scroll for the previous mode before switching.
      const key = scrollStorageKey()
      if (lastSavedKey && lastSavedKey !== key)
        saveScrollPosition()
      lastSavedKey = key

      const savedScroll = readScrollEntry(key)
      if (savedScroll != null) {
        sessionStorageRemove(key)
        requestAnimationFrame(() => {
          if (contentRef)
            contentRef.scrollTop = savedScroll
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
          diffAvailable={diffAvailable()}
          onModeChange={mode => props.onFileViewModeChange?.(mode)}
          onDiffBaseChange={base => props.onFileDiffBaseChange?.(base)}
        />
      </Show>
      <Show when={showFloatingActions()}>
        <div class={styles.floatingTopRight}>
          <Show when={showViewToggle()}>
            <ViewToggle mode={displayMode()} onToggle={m => props.onDisplayModeChange?.(m)} showSplit />
          </Show>
          <FileActionsMenu
            workerId={props.workerId}
            path={props.filePath}
            flavor={flavor()}
            rootPath={props.rootPath}
            homeDir={props.homeDir}
            onMention={props.onMention ? () => props.onMention?.() : undefined}
            triggerClass={styles.viewToggleButton}
            triggerTestId="file-actions-trigger"
            actions={saveActions}
          />
        </div>
      </Show>
      <div
        class={styles.content}
        classList={{ [styles.hasFloatingToolbar]: hasTopChrome() }}
        ref={contentRef}
      >
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

          {/* Diff mode: binary indicator OR unified/split text diff. */}
          <Show when={isDiffMode() && !diffLoading() && binaryDiffMessage() !== null}>
            <div class={styles.errorState} data-testid="binary-diff-indicator">
              {binaryDiffMessage()}
            </div>
          </Show>
          <Show when={isDiffMode() && !diffLoading() && binaryDiffMessage() === null && diffOldContent() !== null && diffNewContent() !== null}>
            <DiffView
              hunks={diffHunks()}
              view={diffViewPref()}
              filePath={props.filePath}
              originalFile={diffOldContent() ?? undefined}
            />
          </Show>

          {/* Working mode or no mode: show normal file content. */}
          <Show when={!isDiffMode() && !isRefMode()}>
            <Show
              when={showAnyway()}
              fallback={(
                <Switch>
                  <Match when={shouldShowUnsupported()}>
                    <UnsupportedFileView
                      filePath={props.filePath}
                      flavor={flavor()}
                      totalSize={totalSize()}
                      reason={cardReason()!}
                      op={saveActions.op()}
                      loadingAnyway={loadingAnyway()}
                      canShowAnyway={totalSize() > 0}
                      onDownload={saveActions.handleDownload}
                      onShowAnyway={handleShowAnyway}
                      desktop={isDesktop
                        ? {
                            onSaveAs: saveActions.handleSaveAs,
                            onSaveToDownloads: saveActions.handleSaveToDownloads,
                            revealAfterDownload: prefs.revealAfterDownload(),
                            onRevealAfterDownloadChange: prefs.setRevealAfterDownload,
                          }
                        : undefined}
                    />
                  </Match>
                  <Match when={viewMode() === 'text' && content()}>
                    <TextFileView
                      content={content()!}
                      filePath={props.filePath}
                      totalSize={totalSize()}
                      onQuote={props.onQuote}
                    />
                  </Match>
                  <Match when={viewMode() === 'markdown' && content()}>
                    <MarkdownFileView
                      content={content()!}
                      filePath={props.filePath}
                      totalSize={totalSize()}
                      mode={displayMode()}
                      onQuote={props.onQuote}
                    />
                  </Match>
                  <Match when={viewMode() === 'image' && content()}>
                    <ImageFileView
                      content={content()!}
                      filePath={props.filePath}
                      totalSize={totalSize()}
                      mode={displayMode()}
                      onQuote={props.onQuote}
                    />
                  </Match>
                </Switch>
              )}
            >
              <Show when={cardReason() !== null && content()}>
                <HexView
                  content={content()!}
                  totalSize={totalSize()}
                  topOffset={hasTopChrome() ? TOOLBAR_CLEARANCE_PX : 0}
                />
              </Show>
            </Show>
          </Show>
        </Show>
      </div>
      <Show when={!loading() && !error() && !isDiffMode() && !isRefMode() && !shouldShowUnsupported() && (totalSize() > 0 || isTruncated())}>
        <div
          class={styles.statusBar}
          classList={{ [styles.statusBarInteractive]: showAnyway() && cardReason() !== null }}
        >
          <Show when={showAnyway() && cardReason() !== null}>
            <button
              type="button"
              class={styles.statusActionButton}
              data-testid="file-show-download-view-button"
              aria-label={`Return to the file download view for ${basename(props.filePath, flavor())}`}
              onClick={() => setShowAnyway(false)}
            >
              Show download view
            </button>
          </Show>
          <Show when={isTruncated()}>
            <span class={styles.truncationWarning}>
              {`Truncated at ${formatBytes(MAX_FILE_SIZE)}`}
            </span>
          </Show>
          <Show when={totalSize() > 0}>
            <span class={styles.statusMeta}>{formatBytes(totalSize())}</span>
          </Show>
        </div>
      </Show>
    </div>
  )
}
