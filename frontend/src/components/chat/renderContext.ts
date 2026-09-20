// Focused render capabilities, named for the component that reads them.
//
// `RenderContext` (messageRenderers.tsx) is the ORCHESTRATION bag: MessageBubble
// builds one per row and every layer below may hand it down. These interfaces are
// what the shared RESULT components accept instead -- each states the capabilities
// its component actually uses, so a result body cannot reach the whole service bag
// (the background-task store, the message resolver, the live progress channel) and
// a component's real dependencies read from its props.
//
// Reactive members are GETTERS or functions, never plain fields: an assembly that
// spreads a context freezes the values of one pass, and a row that streams would
// draw the output it held when the button was built.

import type { MessageRenderCache } from './messageRenderCache'
import type { MessageUiKey } from './messageUiKeys'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import type { ImageResultSource } from '~/lib/imageBlocks'
import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import type { ToolProgressEntry } from '~/stores/chatToolProgress'

/**
 * What the markdown and ANSI body renderers need from their surroundings: the
 * caches and the two pause flags that keep a hidden or scroll-critical pass from
 * dispatching syntax work.
 */
export interface MarkdownRenderContext {
  /**
   * Per-row/content-version render-derivation cache shared by visible + premeasure
   * mounts. Carries `| undefined` because a full RenderContext -- whose `renderCache`
   * getter resolves through to undefined while the host is absent -- is a valid
   * markdown context, and undefined is the live "absent for now" state.
   */
  renderCache?: MessageRenderCache | undefined
  /** Hidden premeasurement pass: keep structure, skip timers, workers and highlighting. */
  premeasureMode?: boolean
  /** The visible pass is scroll-critical: defer Shiki/worker syntax jobs until it clears. */
  syntaxHighlightingPaused?: () => boolean
  /** A browser text selection is active: do not replace selected text nodes. */
  textSelectionActive?: () => boolean
  /**
   * The row sits outside the near-viewport band: its upgrades are low-priority.
   * Carries `| undefined` because a full RenderContext -- whose `rowOffscreen`
   * getter resolves through to undefined while the host is absent -- is a valid
   * markdown context, and undefined is the live "absent for now" state.
   */
  rowOffscreen?: (() => boolean) | undefined
}

/** The per-row UI toggles a remount-sensitive renderer keeps through its host. */
export interface MessageUiState {
  get: (key: MessageUiKey) => boolean | undefined
  set: (key: MessageUiKey, value: boolean) => void
}

/** The stored expansion state that a mounted row can read and change. */
export interface MessageUiRenderContext {
  expandAgentThoughts?: boolean
  getMessageUiState?: ((key: MessageUiKey) => boolean | undefined) | undefined
  setMessageUiState?: ((key: MessageUiKey, value: boolean) => void) | undefined
}

/** The directories that a result uses to shorten a displayed path. */
export interface PathRenderContext {
  workingDir?: string | undefined
  homeDir?: string | undefined
}

/**
 * Loading and opening the images one row drew.
 *
 * Assembled where the message and the agent are both in scope, so a renderer only
 * states WHICH image. `deferLoad` gates the read itself: a hidden premeasure pass
 * and an offscreen row both hold the file read until the row is real.
 */
export interface ImageRenderActions {
  /** Read a file-backed image's bytes; `refresh` re-reads past the cache. */
  loadFileImage: (filePath: string, options?: { refresh?: boolean }) => Promise<ImageResultSource | undefined>
  /** The already-read copy of a file-backed image, when one is held. */
  cachedFileImage: (filePath: string) => ImageResultSource | undefined
  /** Open one image this row drew in its own tab. */
  openImage: (image: { index: number, filePath?: string, title?: string }) => void
  /** True while the read must wait (hidden premeasure, offscreen row). */
  deferLoad: () => boolean
  /** True while the pass is a hidden measurement, which decodes inline data eagerly. */
  premeasurePass: () => boolean
}

/**
 * The diff preference a diff body draws by, as a getter: the toolbar may flip it
 * mid-row and the body re-reads rather than holding the value of one pass.
 */
export interface DiffRenderActions {
  view: () => DiffViewPreference
}

/**
 * Resolving a subagent row and opening its transcript, without the store.
 *
 * The background-task REGISTRY is a live store with far more than a recipient chip
 * reads; this states the two operations a row's own rendering needs.
 */
export interface SubagentNavigation {
  /** The registry row a spawn key addresses, or undefined when none is held. */
  row: (registryKey: string) => BackgroundTaskItem | undefined
  /**
   * Open (or activate, or revive) a subagent's tab from its registry row.
   *
   * OPTIONAL, and the recipient chip reads the absence: a host that supplies no
   * opener has nowhere to send a click, so the label stays plain text rather
   * than posing as a control.
   */
  open?: (item: BackgroundTaskItem) => void
}

/** The live output of a call that has not returned, read by the row drawing its tail. */
export interface ToolProgressSource {
  liveTail: () => ToolProgressEntry | undefined
}

/** The capabilities that the common tool-row layout uses. */
export interface ToolLayoutContext extends MarkdownRenderContext {
  createdAt?: string
  onCopyJson?: () => void
  jsonCopied?: () => boolean
  spanColor?: number
  toolProgress?: ToolProgressSource
}

/** The complete, focused capability set that result components can receive. */
export interface ToolResultRenderContext extends ToolLayoutContext, MessageUiRenderContext, PathRenderContext {
  completionHeader?: boolean
  diffView?: () => DiffViewPreference
  onReply?: ((quotedText: string) => void) | undefined
  hasOuterToolbar?: boolean
  subagents?: SubagentNavigation
  images?: ImageRenderActions
}

/**
 * Assemble {@link SubagentNavigation} from the orchestration members its scope
 * holds. THE one assembly: MessageBubble builds its context's capability here, and
 * an isolated mount (a test, a preview) builds the same thing from the same parts,
 * so the two cannot disagree about what the capability reads.
 */
export function subagentsFrom(host: {
  backgroundTask?: (registryKey: string) => BackgroundTaskItem | undefined
  openSubagent?: (item: BackgroundTaskItem) => void
} | undefined): SubagentNavigation | undefined {
  const row = host?.backgroundTask
  const open = host?.openSubagent
  return row !== undefined || open !== undefined
    ? { row: row ?? (() => undefined), ...(open === undefined ? {} : { open }) }
    : undefined
}

/**
 * Assemble {@link ImageRenderActions} from the file-image channel, the open
 * handler and the two load gates. THE one assembly, for the reason
 * {@link subagentsFrom} gives.
 */
export function imageActionsFrom(host: {
  fileImage?: (filePath: string, options?: { refresh?: boolean }) => Promise<ImageResultSource | undefined>
  cachedFileImage?: (filePath: string) => ImageResultSource | undefined
  openImage?: (image: { index: number, filePath?: string, title?: string }) => void
  /** True while the read must wait: hidden premeasure pass or offscreen row. */
  deferLoad?: () => boolean
  /** True while the pass is a hidden measurement. */
  premeasurePass?: () => boolean
} | undefined): ImageRenderActions | undefined {
  const load = host?.fileImage
  const cached = host?.cachedFileImage
  const open = host?.openImage
  const premeasure = host?.premeasurePass
  if (load === undefined && cached === undefined && open === undefined && premeasure === undefined)
    return undefined
  return {
    loadFileImage: load ?? (async () => undefined),
    cachedFileImage: cached ?? (() => undefined),
    openImage: open ?? (() => {}),
    deferLoad: () => host?.deferLoad?.() === true,
    premeasurePass: () => host?.premeasurePass?.() === true,
  }
}
