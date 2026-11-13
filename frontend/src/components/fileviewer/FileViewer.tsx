import type { Component } from 'solid-js'
import type { UnsupportedReason } from './UnsupportedFileView'
import type { ComparedVersions, ShownVersionDiff, VersionRead } from './versionDiff'
import type { ComputedDiff } from '~/components/chat/diff/diffCompute'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import type { GitFileStatusEntry } from '~/generated/proto/leapmux/v1/common_pb'
import type { FileViewMode } from '~/lib/fileType'
import type { FileDiffBase, FileDisplayMode, FileViewMode as TabFileViewMode } from '~/stores/tab.types'
import { batch, createComputed, createEffect, createMemo, createSignal, Match, on, onCleanup, Show, Switch } from 'solid-js'
import { isTauriApp } from '~/api/platformBridge'
import { workerFile, workerGit } from '~/api/workerRpc'
import { diffAdded, diffRemoved } from '~/components/chat/diff/diffStyles.css'
import { requestComputedDiff } from '~/components/chat/diff/diffWorkerClient'
import { createRenderReadinessScope } from '~/components/chat/renderReadiness'
import { FileActionsMenu } from '~/components/common/FileActionsMenu'
import { createFileSaveActions } from '~/components/common/fileSaveActions'
import { showWarnToast } from '~/components/common/Toast'
import { usePreferences } from '~/context/PreferencesContext'
import { FILE_READ_LIMIT_BYTES } from '~/generated/contracts/file-read'
import { GitFileStatusCode } from '~/generated/proto/leapmux/v1/common_pb'
import { GitFileRef } from '~/generated/proto/leapmux/v1/git_pb'
import { PREFIX_FILE_SCROLL, sessionStorageGet, sessionStorageRemove, sessionStorageSet } from '~/lib/browserStorage'
import { formatErrorMessage } from '~/lib/errors'
import { detectFileViewModeFromExt, isImageExt, isLikelyBinaryExt, isSvgExt } from '~/lib/fileType'
import { formatBytes } from '~/lib/formatBytes'
import { MAX_FILE_IMAGE_BYTES } from '~/lib/imageBlocks'
import { basename, detectFlavor, extname } from '~/lib/paths'
import { readWorkerFile } from '~/lib/readWorkerFile'
import { DiffModeToolbar } from './DiffModeToolbar'
import * as styles from './FileViewer.css'
import { TOOLBAR_CLEARANCE_PX } from './FileViewer.css'
import { HexView } from './HexView'
import { ImageFileView } from './ImageFileView'
import { MarkdownFileView } from './MarkdownFileView'
import { TextFileView } from './TextFileView'
import { UnsupportedFileView } from './UnsupportedFileView'
import { ABSENT_VERSION, compareVersions, shownVersionDiff } from './versionDiff'
import { VersionDiffView } from './VersionDiffView'
import { ViewToggle } from './ViewToggle'

/** The limit of each git read: the HEAD and staged versions stop where the working copy does. */
const GIT_READ_LIMIT = BigInt(FILE_READ_LIMIT_BYTES)

/** What the viewer loads for the view mode of the tab: the unified and split views load one diff of a base (see diffSourceMode). */
type DiffSourceMode = 'working' | 'head' | 'staged' | 'diff:head-vs-working' | 'diff:head-vs-staged'

/**
 * What a ref or diff mode loaded:
 *
 * - `ref`: the version that a ref mode shows, null when the file does not exist at the ref.
 * - `diff`: the two versions that a diff mode compares.
 * - `binary`: nothing, for a file whose extension marks it binary. The diff mode
 *   states its git status instead.
 * - `failed`: the reason that a read failed.
 */
type GitModeContent
  = | { kind: 'ref', version: VersionRead | null }
    | { kind: 'diff', versions: ComparedVersions }
    | { kind: 'binary' }
    | { kind: 'failed', message: string }

/** The content of one ref or diff mode load, with the file and the mode that it read. */
interface GitModeLoad {
  workerId: string
  filePath: string
  mode: DiffSourceMode
  content: GitModeContent
}

/**
 * The diff that diff mode draws: none (another mode, a binary file, or a version
 * that has not loaded), computing in the diff worker, or ready.
 */
type ViewerDiff
  = | { status: 'none' }
    | { status: 'computing' }
    | { status: 'ready', diff: ComputedDiff, versions: ComparedVersions }

/**
 * Whether two answers of the viewer diff are the same: the same status and the
 * same diff of the same versions. A memo that compares with this keeps its value
 * when it runs again with the same answer, so its readers do not run again: the
 * scroll restore among them would move the scroll back to the first change.
 */
function sameViewerDiff(a: ViewerDiff, b: ViewerDiff): boolean {
  if (a.status === 'ready' || b.status === 'ready') {
    return a.status === 'ready' && b.status === 'ready'
      && a.diff === b.diff && a.versions === b.versions
  }
  return a.status === b.status
}

/** Whether the limit cut the content of a read. */
function readTruncated(resp: { content: Uint8Array, totalSize: bigint }): boolean {
  return BigInt(resp.content.length) < resp.totalSize
}

/**
 * A read version as the ref and diff modes take it: at most FILE_READ_LIMIT_BYTES bytes,
 * as each git read returns. The working copy of an image loads up to the larger
 * image limit, and its diff must stop where the git versions stop.
 */
function limitedVersion(resp: { content: Uint8Array, totalSize: bigint }): VersionRead {
  return {
    bytes: resp.content.subarray(0, FILE_READ_LIMIT_BYTES),
    truncated: resp.content.length > FILE_READ_LIMIT_BYTES || readTruncated(resp),
  }
}

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
  const newSide = diffBase === 'head-vs-staged'
    ? 'staged copy'
    : 'working copy'

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
  return scroll !== undefined && Number.isFinite(scroll)
    ? scroll
    : null
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
  // Fed from the same response that supplies the bytes, so the three-dot menu's
  // Modified row costs no round trip of its own.
  const [modTime, setModTime] = createSignal('')
  const [viewMode, setViewMode] = createSignal<FileViewMode>('text')
  // Per-session signal flipped by the "Show anyway" button. Bytes
  // (when not already cached) are fetched lazily.
  const [showAnyway, setShowAnyway] = createSignal(false)
  const [loadingAnyway, setLoadingAnyway] = createSignal(false)
  const isDesktop = isTauriApp()
  const prefs = usePreferences()

  // The file and the view mode, each a memo of its value. The tile reads each
  // prop from the whole tab object (see TileRenderer), so a patch of any field
  // of the tab, such as a switch between unified and split, re-reads every prop
  // with its old value too. The loads, the scroll restore and the child views
  // below read these memos, which change only with a value, so such a patch
  // reloads nothing, scrolls nothing, and draws nothing again: an image view
  // that read the raw path would create its blob URL again and flash blank.
  const workerIdValue = createMemo(() => props.workerId)
  const filePathValue = createMemo(() => props.filePath)
  const fileViewModeValue = createMemo(() => props.fileViewMode)

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
  const displayMode = (): FileDisplayMode => (props.displayMode as FileDisplayMode | undefined) ?? 'render'

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
  const oversizeImage = createMemo(() => isImagePath() && totalSize() > MAX_FILE_IMAGE_BYTES)

  // The last ref or diff mode load. One signal holds both versions of a diff, so
  // a reader never sees one version of a load beside the other of an older one.
  const [gitModeLoad, setGitModeLoad] = createSignal<GitModeLoad | null>(null)

  const isDiffMode = () => fileViewModeValue() === 'unified-diff' || fileViewModeValue() === 'split-diff'

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
  const fetchFirstChunk = (workerId: string, filePath: string, signal?: AbortSignal) =>
    workerFile.readFile(workerId, {
      path: filePath,
      limit: BigInt(FILE_READ_LIMIT_BYTES),
    }, signal
      ? { signal }
      : undefined)

  // Load working copy content.
  createEffect(on(
    () => [
      workerIdValue(),
      filePathValue(),
    ] as const,
    async ([workerId, filePath]) => {
      let resolveBytes!: (bytes: Uint8Array | null) => void
      currentBytesPromise = new Promise<Uint8Array | null>((r) => {
        resolveBytes = r
      })
      // A newer path or an unmount ends this load. The abort stops its RPCs,
      // and `active` turns the rejection that the abort causes into a stale
      // result: `active` is cleared BEFORE the abort, so each handler below
      // ignores that rejection as it ignores a late answer.
      const controller = new AbortController()
      let active = true
      onCleanup(() => {
        active = false
        controller.abort()
        resolveBytes(null)
      })

      setLoading(true)
      setError(null)
      setContent(null)
      // Cleared with the content: a stale modification time beside a different
      // file's bytes is worse than an absent row.
      setModTime('')
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
          const statResp = await workerFile.statFile(workerId, { path: filePath }, { signal: controller.signal })
          if (!active)
            return
          batch(() => {
            setTotalSize(Number(statResp.info?.size ?? 0n))
            setModTime(statResp.info?.modTime ?? '')
            setViewMode('binary')
            setLoading(false)
          })
          resolveBytes(null)
          return
        }

        // Images use the shared byte limit and paging helper. Text keeps its smaller preview limit.
        // Oversized images return metadata only, so the viewer does not fetch bytes that it cannot display.
        const readResp = isImageExt(fileExt)
          ? await readWorkerFile({
              workerId,
              path: filePath,
              maxBytes: MAX_FILE_IMAGE_BYTES,
              signal: controller.signal,
            })
          : await fetchFirstChunk(workerId, filePath, controller.signal)
        if (!active)
          return
        const respTotalSize = Number(readResp.totalSize)
        // Oversize image: server skipped the bytes; show the unsupported
        // card with size and no preview.
        if (isImageExt(fileExt) && respTotalSize > MAX_FILE_IMAGE_BYTES) {
          batch(() => {
            setTotalSize(respTotalSize)
            setModTime(readResp.modTime)
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
          setModTime(readResp.modTime)
          setViewMode(detectFileViewModeFromExt(fileExt, bytes))
        })
        resolveBytes(bytes)
      }
      catch (err) {
        if (active)
          setError(formatErrorMessage(err, 'Failed to load file'))
        resolveBytes(null)
      }
      finally {
        if (active)
          setLoading(false)
      }
    },
  ))

  // Diff source mode — collapses `unified-diff` / `split-diff` into a
  // single value keyed by `fileDiffBase` so toggling the view (unified
  // vs. split) doesn't re-fire the fetch effect. The diff bytes only
  // depend on workerId / filePath / diffBase; the view choice is
  // render-only.
  const diffSourceMode = createMemo<DiffSourceMode>(() => {
    const fv = fileViewModeValue()
    if (!fv || fv === 'working')
      return 'working'
    if (fv === 'head' || fv === 'staged')
      return fv
    const base = props.fileDiffBase === 'head-vs-staged'
      ? 'head-vs-staged'
      : 'head-vs-working'
    return `diff:${base}`
  })
  /** Whether the view shows the working copy as it is, with no git version. */
  const isWorkingMode = () => diffSourceMode() === 'working'

  /**
   * What the current ref or diff mode loaded, or undefined while it loads and in
   * working mode. A load of another file or mode is not the current one.
   */
  const gitModeContent = createMemo((): GitModeContent | undefined => {
    const load = gitModeLoad()
    return load !== null
      && load.workerId === props.workerId
      && load.filePath === props.filePath
      && load.mode === diffSourceMode()
      ? load.content
      : undefined
  })
  /** Whether a ref or diff mode waits for its versions. */
  const diffLoading = () => !isWorkingMode() && gitModeContent() === undefined

  // Load the versions of the ref or diff mode.
  createEffect(on(
    () => [
      workerIdValue(),
      filePathValue(),
      diffSourceMode(),
    ] as const,
    async ([workerId, filePath, mode]) => {
      // The same lifetime rule as the working-copy load: a newer mode or path,
      // or an unmount, aborts this run's reads, and `active` makes every
      // handler below ignore their late answers and the abort's rejection.
      // Without it, a HEAD read that answered after a switch to the staged
      // copy replaced the staged text on screen.
      const controller = new AbortController()
      const rpcOpts = { signal: controller.signal }
      let active = true
      onCleanup(() => {
        active = false
        controller.abort()
      })

      // Working mode shows no git version. Drop the last load, which holds up
      // to two versions of FILE_READ_LIMIT_BYTES.
      if (mode === 'working') {
        setGitModeLoad(null)
        return
      }
      // An empty path waits for the real one, as the working-copy load does. The
      // worker rejects a read of it, and the pane would state that failure.
      if (!workerId || !filePath)
        return

      const store = (content: GitModeContent) => {
        if (active)
          setGitModeLoad({ workerId, filePath, mode, content })
      }
      /** The version at `ref`, or null when the file does not exist there. */
      const readGitVersion = async (ref: GitFileRef): Promise<VersionRead | null> => {
        const resp = await workerGit.readGitFile(workerId, { path: filePath, ref, limit: GIT_READ_LIMIT }, rpcOpts)
        return resp.exists
          ? limitedVersion(resp)
          : null
      }
      // head-vs-working: prefer the working-copy bytes that the load effect reads,
      // rather than a second readFile of the same range. It awaits that load, so
      // the bytes are stable. It reads afresh when that load has no bytes: a
      // binary file, an image above the image limit, or a failed load.
      const readWorkingVersion = async (): Promise<VersionRead> => {
        const bytes = await currentBytesPromise
        return limitedVersion(bytes
          ? {
              content: bytes,
              totalSize: BigInt(totalSize()),
            }
          : await fetchFirstChunk(workerId, filePath, controller.signal))
      }

      try {
        if (mode === 'head' || mode === 'staged') {
          const version = await readGitVersion(mode === 'head'
            ? GitFileRef.HEAD
            : GitFileRef.STAGED)
          store({ kind: 'ref', version })
          return
        }
        // Binary files render the git-status-driven indicator
        // instead of decoded-text DiffView — skip the fetch entirely
        // for the common (extension-based) binary case. Unknown-
        // extension binaries still pay the wasted fetch since the
        // sniff runs in the load effect, but the render path won't
        // display the decoded garbage.
        if (isLikelyBinaryExt(extname(filePath))) {
          store({ kind: 'binary' })
          return
        }
        // HEAD ref runs in parallel with whatever the "new" side
        // needs — they target the same worker but don't depend on
        // each other.
        const headRead = readGitVersion(GitFileRef.HEAD)
        // Handled here as well as at the await below. When the new side
        // fails first, or this run is superseded, the function leaves
        // before it awaits the HEAD read, and the abort above then rejects
        // that read with nobody listening.
        headRead.catch(() => {})
        const newVersion = mode === 'diff:head-vs-staged'
          ? (await readGitVersion(GitFileRef.STAGED)) ?? ABSENT_VERSION
          : await readWorkingVersion()
        const oldVersion = (await headRead) ?? ABSENT_VERSION
        store({
          kind: 'diff',
          versions: compareVersions(oldVersion, newVersion),
        })
      }
      catch (err) {
        // A superseded run's failure, the abort included, describes reads that
        // nobody waits for, and `store` ignores it.
        store({
          kind: 'failed',
          message: formatErrorMessage(err, 'Failed to read the file from git'),
        })
      }
    },
  ))

  const isTruncated = () => content() !== null && content()!.length < totalSize()
  /** Whether the read limit cut a version that the current ref or diff mode shows. */
  const gitVersionTruncated = () => {
    const loaded = gitModeContent()
    switch (loaded?.kind) {
      case 'ref':
        return loaded.version?.truncated === true
      case 'diff':
        return loaded.versions.oldTruncated || loaded.versions.newTruncated
      default:
        return false
    }
  }
  /** Whether a version that the current mode shows is cut at FILE_READ_LIMIT_BYTES. */
  const shownVersionTruncated = () => isWorkingMode()
    ? isTruncated()
    : gitVersionTruncated()

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
    && isWorkingMode()
    && !showAnyway()
    && cardReason() !== null

  /**
   * Whether the status bar offers the return to the download view. That view
   * belongs to working mode, so the bar of a ref or diff mode offers nothing.
   */
  const downloadViewReturnShown = () => isWorkingMode() && showAnyway() && cardReason() !== null

  /**
   * Whether the status bar has something to say. In working mode it states the
   * size and any truncation. In the ref and diff modes it states only a
   * truncation, because the size it holds is the working copy's.
   */
  const statusBarShown = () => {
    if (loading() || error())
      return false
    if (!isWorkingMode())
      return gitVersionTruncated()
    return !shouldShowUnsupported() && (totalSize() > 0 || isTruncated())
  }

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
          setModTime(readResp.modTime)
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

  const showToolbar = () => fileViewModeValue() !== undefined
  const showFloatingActions = () => !loading() && !error()
  const hasTopChrome = () => showToolbar() || showFloatingActions()

  // Markdown and SVG-image viewers expose a render/source/split toggle
  // beside the FileActionsMenu in the top-right floating slot. Other
  // modes show just the actions menu.
  const showViewToggle = () =>
    !loading() && !error() && isWorkingMode()
    && (viewMode() === 'markdown' || (viewMode() === 'image' && isSvgExt(ext())))

  // The diff of the two versions, which the diff worker computes (see
  // diffWorkerClient). A binary file that draws its notice diffs nothing. The
  // versions hold one input object for the whole load, so each run asks for the
  // diff of the same object.
  const viewerDiff = createMemo((): ViewerDiff => {
    const loaded = gitModeContent()
    if (loaded?.kind !== 'diff' || binaryDiffMessage() !== null)
      return { status: 'none' }
    const diff = requestComputedDiff(loaded.versions.input)
    return diff === undefined
      ? { status: 'computing' }
      : { status: 'ready', diff, versions: loaded.versions }
  }, { status: 'none' }, { equals: sameViewerDiff })
  /**
   * Where the diff view reports that it has not mounted every line yet. It
   * mounts its lines in slices of main-thread work (see DiffView), and a scroll
   * restore before the last line would clamp to a shorter page. The view also
   * reports here that it replaces its layout: it mounts the layout of a new view
   * mode (unified or split) behind the one that it shows. The viewer keeps the
   * old layout in view meanwhile, so the switch shows no loading state, but the
   * scroll restore waits for the new layout.
   */
  const diffReadiness = createRenderReadinessScope()
  /**
   * Whether the current ref or diff mode has nothing to draw yet: its versions
   * load, the diff worker computes their diff, or the diff view mounts its lines.
   * The loading indicator and the scroll restore wait for this one answer.
   */
  const gitModePending = () => diffLoading() || viewerDiff().status === 'computing' || diffReadiness.pending()
  /** What diff mode draws once the diff is ready: the diff stops where a read of one version stops. */
  const shownDiff = createMemo((): ShownVersionDiff | undefined => {
    const current = viewerDiff()
    return current.status === 'ready'
      ? shownVersionDiff(current.diff, current.versions)
      : undefined
  })
  /** The version that a ref mode shows, null when the file does not exist at the ref, or undefined in another state. */
  const refVersion = (): { version: VersionRead | null } | undefined => {
    const loaded = gitModeContent()
    return loaded?.kind === 'ref'
      ? loaded
      : undefined
  }
  /** The reason that the read of a ref or diff mode failed, or undefined. */
  const gitReadFailure = (): string | undefined => {
    const loaded = gitModeContent()
    return loaded?.kind === 'failed'
      ? loaded.message
      : undefined
  }

  const diffViewPref = (): DiffViewPreference =>
    fileViewModeValue() === 'split-diff'
      ? 'split'
      : 'unified'

  let contentRef: HTMLDivElement | undefined

  // Include the view mode in the scroll key so each mode has independent scroll.
  //
  // Derived per read, NOT captured at setup. `filePath` is emphatically not
  // stable for this component's lifetime: the pane is keyed on the TAB ID (see
  // TileRenderer's `tileFileTabIds`), so a file tab that arrives from the CRDT
  // before its path does -- a reload, a peer's open, `leapmux control tab open
  // --type=file` -- mounts this component with `filePath=''` and is patched
  // afterwards by the hydrator. Freezing the prefix there gave every such tab on
  // one worker the SAME key, so their scroll offsets overwrote each other.
  // (Keying rows on the `Tab` object used to remount the pane on that patch,
  // which is what made a setup-time read accidentally correct.)
  const scrollStoragePrefix = () => `${PREFIX_FILE_SCROLL}${props.workerId}:${props.filePath}`
  const scrollStorageKey = () => `${scrollStoragePrefix()}:${props.fileViewMode ?? 'working'}`

  // Save scroll position when the view mode changes or on unmount.
  // Values are TTL-wrapped via `sessionStorageSet` so stale entries get
  // dropped by `runCleanup` on next app load.
  /**
   * The scroll key of the mode whose content the viewer shows, or undefined
   * from a change of the key until the restore below runs for the new key.
   */
  let shownScrollKey: string | undefined
  /** Save the scroll position of the mode whose content the viewer shows. */
  const saveScrollPosition = () => {
    if (shownScrollKey === undefined || !contentRef)
      return
    if (contentRef.scrollTop > 0)
      sessionStorageSet(shownScrollKey, contentRef.scrollTop)
    else
      sessionStorageRemove(shownScrollKey)
  }
  onCleanup(saveScrollPosition)
  // Save the scroll position of the mode that the reader leaves while its
  // content is still in place: a computation runs before the DOM changes. The
  // next mode can hide its content while it loads or while its diff mounts, and
  // the container then scrolls to the top.
  createComputed(on(scrollStorageKey, (key, previousKey) => {
    if (key === previousKey)
      return
    saveScrollPosition()
    shownScrollKey = undefined
  }, { defer: true }))

  // Restore saved scroll position or scroll to first diff entry, once the content
  // of the mode shows: its versions loaded, its diff mounted, and a new diff
  // layout swapped in.
  createEffect(on(
    () => [
      loading(),
      gitModePending(),
      diffReadiness.replacing(),
      isDiffMode(),
      shownDiff()?.diff.hunks.length ?? 0,
      fileViewModeValue(),
    ] as const,
    ([fileLoading, gitPending, layoutSwitching, diffMode, hunkCount, _fvMode]) => {
      if (fileLoading || gitPending || layoutSwitching || !contentRef)
        return

      const key = scrollStorageKey()
      shownScrollKey = key

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
        const diffElements = [
          ...container.querySelectorAll(`.${diffAdded}, .${diffRemoved}`),
        ]
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
          mode={fileViewModeValue()!}
          {...(props.fileDiffBase !== undefined
            ? { diffBase: props.fileDiffBase }
            : {})}
          {...(props.hasStagedAndUnstaged !== undefined
            ? { hasStagedAndUnstaged: props.hasStagedAndUnstaged }
            : {})}
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
            workerId={workerIdValue()}
            path={filePathValue()}
            flavor={flavor()}
            {...(props.rootPath !== undefined
              ? { rootPath: props.rootPath }
              : {})}
            {...(props.homeDir !== undefined
              ? { homeDir: props.homeDir }
              : {})}
            // The size the viewer already loaded, so its own three-dot menu
            // shows the Size row the identical menu shows in the sidebar tree.
            // Omitted when 0, because 0 here means "not loaded yet", not "an
            // empty file" -- the signal starts at 0 before the read resolves.
            {...(totalSize() > 0
              ? {
                  size: totalSize(),
                }
              : {})}
            {...(modTime()
              ? {
                  modTime: modTime(),
                }
              : {})}
            {...(props.onMention !== undefined
              ? {
                  onMention: () => props.onMention?.(),
                }
              : {})}
            triggerClass={styles.viewToggleButton}
            triggerTestId="file-actions-trigger"
            actions={saveActions}
          />
        </div>
      </Show>
      <div
        class={styles.content}
        classList={{
          [styles.hasFloatingToolbar]: hasTopChrome(),
        }}
        data-testid="file-viewer-content"
        ref={contentRef}
      >
        <Show when={loading() || gitModePending()}>
          <div class={styles.loadingState} data-testid="file-viewer-loading">Loading...</div>
        </Show>
        <Show when={error()}>
          <div class={styles.errorState}>{error()}</div>
        </Show>
        <Show when={!loading() && !error()}>
          {/* Ref mode: show HEAD or staged content as text */}
          <Show when={refVersion()}>
            {ref => (
              <Show
                when={ref().version}
                fallback={<div class={styles.errorState}>File does not exist at this ref</div>}
              >
                {version => (
                  <TextFileView
                    content={version().bytes}
                    filePath={filePathValue()}
                    totalSize={version().bytes.length}
                    {...(props.onQuote !== undefined
                      ? { onQuote: props.onQuote }
                      : {})}
                  />
                )}
              </Show>
            )}
          </Show>

          {/* Diff mode: binary indicator OR unified/split text diff. */}
          <Show when={isDiffMode() && !diffLoading() && binaryDiffMessage() !== null}>
            <div class={styles.errorState} data-testid="binary-diff-indicator">
              {binaryDiffMessage()}
            </div>
          </Show>
          {/* A binary file draws its git status, which needs no read, so the failure of its read states nothing. */}
          <Show when={binaryDiffMessage() === null && gitReadFailure()}>
            {message => <div class={styles.errorState}>{message()}</div>}
          </Show>
          <Show when={shownDiff()}>
            {shown => (
              <VersionDiffView
                shown={shown()}
                view={diffViewPref()}
                filePath={filePathValue()}
                readLimit={FILE_READ_LIMIT_BYTES}
                readiness={diffReadiness}
              />
            )}
          </Show>

          {/* Working mode or no mode: show normal file content. */}
          <Show when={isWorkingMode()}>
            <Show
              when={showAnyway()}
              fallback={(
                <Switch>
                  <Match when={shouldShowUnsupported()}>
                    <UnsupportedFileView
                      filePath={filePathValue()}
                      flavor={flavor()}
                      totalSize={totalSize()}
                      reason={cardReason()!}
                      op={saveActions.op()}
                      loadingAnyway={loadingAnyway()}
                      canShowAnyway={totalSize() > 0}
                      onDownload={saveActions.handleDownload}
                      onShowAnyway={handleShowAnyway}
                      {...(isDesktop
                        ? {
                            desktop: {
                              onSaveAs: saveActions.handleSaveAs,
                              onSaveToDownloads: saveActions.handleSaveToDownloads,
                              revealAfterDownload: prefs.revealAfterDownload(),
                              onRevealAfterDownloadChange: prefs.setRevealAfterDownload,
                            },
                          }
                        : {})}
                    />
                  </Match>
                  <Match when={viewMode() === 'text' && content()}>
                    <TextFileView
                      content={content()!}
                      filePath={filePathValue()}
                      totalSize={totalSize()}
                      {...(props.onQuote !== undefined
                        ? { onQuote: props.onQuote }
                        : {})}
                    />
                  </Match>
                  <Match when={viewMode() === 'markdown' && content()}>
                    <MarkdownFileView
                      content={content()!}
                      filePath={filePathValue()}
                      totalSize={totalSize()}
                      mode={displayMode()}
                      {...(props.onQuote !== undefined
                        ? { onQuote: props.onQuote }
                        : {})}
                    />
                  </Match>
                  <Match when={viewMode() === 'image' && content()}>
                    <ImageFileView
                      content={content()!}
                      filePath={filePathValue()}
                      totalSize={totalSize()}
                      mode={displayMode()}
                      {...(props.onQuote !== undefined
                        ? { onQuote: props.onQuote }
                        : {})}
                    />
                  </Match>
                </Switch>
              )}
            >
              <Show when={cardReason() !== null && content()}>
                <HexView
                  content={content()!}
                  totalSize={totalSize()}
                  topOffset={hasTopChrome()
                    ? TOOLBAR_CLEARANCE_PX
                    : 0}
                />
              </Show>
            </Show>
          </Show>
        </Show>
      </div>
      <Show when={statusBarShown()}>
        <div
          class={styles.statusBar}
          classList={{
            [styles.statusBarInteractive]: downloadViewReturnShown(),
          }}
        >
          <Show when={downloadViewReturnShown()}>
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
          <Show when={shownVersionTruncated()}>
            <span class={styles.truncationWarning}>
              {`Truncated at ${formatBytes(FILE_READ_LIMIT_BYTES)}`}
            </span>
          </Show>
          {/* The size of the working copy, which only working mode shows. */}
          <Show when={isWorkingMode() && totalSize() > 0}>
            <span class={styles.statusMeta}>{formatBytes(totalSize())}</span>
          </Show>
        </div>
      </Show>
    </div>
  )
}
