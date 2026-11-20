import type { BuildInfo } from '~/lib/buildEnv'
import type { TrailingDebounced } from '~/lib/debounce'
import { showWarnToastWithLoggedCause } from '~/components/common/Toast'
import {
  LAUNCH_VISIBILITY_HIDDEN,
  LAUNCH_VISIBILITY_MINIMIZED,
  LAUNCH_VISIBILITY_NORMAL,
  TAURI_EVENT_SIDECAR_LOG,
  WINDOW_MODE_FULLSCREEN,
  WINDOW_MODE_MAXIMIZED,
  WINDOW_MODE_NORMAL,
} from '~/generated/contracts/desktop'
import { DEFAULT_MAX_MESSAGE_SIZE } from '~/generated/contracts/wire'
import { BrowserAuthKind as ProtoBrowserAuthKind } from '~/generated/proto/leapmux/desktop/v1/frame_pb'
import { arrayBufferToBase64 } from '~/lib/base64'
import { formatHlcWire } from '~/lib/crdt/hlc'
import { trailingDebounce } from '~/lib/debounce'
import { createLogger } from '~/lib/logger'
import { isMac } from '~/lib/shortcuts/platform'

export type PlatformMode = 'web' | 'tauri-desktop-solo' | 'tauri-desktop-distributed' | 'tauri-mobile-distributed'

export interface PlatformCapabilities {
  mode: PlatformMode
  tunnels: boolean
  appControl: boolean
  windowControl: boolean
  systemPermissions: boolean
  localSolo: boolean
}

/**
 * Mutually-exclusive top-level window display state. `window_width`/`height`
 * always hold the last *windowed* geometry, so exiting maximized/fullscreen
 * returns to a sensible size.
 *
 * DERIVED from the generated tokens. All three languages spell this one: the Go
 * sidecar persists the token, the Rust shell matches it at launch, and this
 * module reads and writes it through `save_window_geometry`. It was three
 * hand-written mirrors before, and the Rust copy's own comment admitted it.
 */
export type WindowMode
  = | typeof WINDOW_MODE_NORMAL
    | typeof WINDOW_MODE_MAXIMIZED
    | typeof WINDOW_MODE_FULLSCREEN

export interface DesktopConfig {
  mode: '' | 'solo' | 'distributed'
  hub_url: string
  window_width: number
  window_height: number
  window_mode: WindowMode
}

/**
 * The state the main window starts in, decided by the Rust shell.
 *
 * The shell owns the decision because it is the only side that knows both the
 * launch arguments (a login launch carries `--autostart`) and the cached tray
 * setting, and it knows them before the webview exists. It reports the value
 * ONCE; a later `getStartupInfo` -- the one a mode switch triggers when the
 * launcher remounts -- answers `normal`.
 *
 * DERIVED from the generated tokens rather than retyped, for the reason the
 * Desktop preference types are: the emitted constants carry `as const`, so a
 * token renamed in contracts/desktop.json fails the type check here instead of
 * quietly narrowing to a value the shell no longer sends.
 */
export type LaunchVisibility
  = | typeof LAUNCH_VISIBILITY_NORMAL
    | typeof LAUNCH_VISIBILITY_MINIMIZED
    | typeof LAUNCH_VISIBILITY_HIDDEN

export interface StartupInfo {
  config: DesktopConfig
  buildInfo: BuildInfo
  launchVisibility: LaunchVisibility
}

/** Wire format from sidecar (snake_case JSON). */
interface StartupInfoWire {
  config: DesktopConfig
  build_info: {
    version: string
    commit_hash: string
    commit_time: string
    build_time: string
    branch: string
  }
  launch_visibility: string
}

/**
 * The five resolved Desktop preferences, as the Rust shell receives them.
 *
 * ONE payload rather than five commands: the shell decides a close, a minimize
 * and a login launch from the whole set together (a minimize goes to the tray
 * only while a tray exists), so five commands would let it act on a
 * half-applied set.
 *
 * The three enum members carry the tokens from contracts/desktop.json, and the
 * shell matches them against the same generated constants — no boolean is
 * derived at this boundary, because `hideOnClose = onClose === 'tray'` is
 * exactly the line that gets inverted.
 */
export interface DesktopBehavior {
  trayEnabled: boolean
  trayOnClose: string
  trayOnMinimize: string
  startOnLogin: boolean
  startMinimized: string
}

/**
 * Something the operating system refused, and which choice it belongs to.
 *
 * Two of the five can fail outside the app: a Linux desktop with no
 * status-icon library cannot show a tray, and an operating system can refuse a
 * login item. `setting` is the FIELD NAME of the choice that failed, from the
 * payload above -- the shell reuses that vocabulary rather than inventing a
 * second one for the same five things, so the caller can place the message on
 * the row that owns the field.
 */
export interface DesktopBehaviorRefusal {
  /**
   * `Extract`, not a bare union: only these two choices can be refused, and
   * the intersection with `keyof DesktopBehavior` is what keeps the name tied
   * to the payload. Renaming the field there narrows this to `never`, and
   * every consumer stops compiling rather than addressing a row that is gone.
   */
  setting: Extract<keyof DesktopBehavior, 'trayEnabled' | 'startOnLogin'>
  message: string
}

/**
 * Narrow a `setDesktopBehavior` rejection into the refusals it carries.
 *
 * A LIST, because the two refusable choices fail independently: a Linux desktop
 * with no status-icon library can also be one whose operating system declines a
 * login item, and each message belongs on its own row.
 *
 * Empty for a rejection that carries no recognizable shape (the IPC layer
 * itself failing, say). Such a rejection belongs to no row, and printing a
 * transport error beside a toggle explains nothing. An entry the shape check
 * refuses is dropped rather than failing the whole list, so one unknown
 * `setting` cannot hide a refusal beside it.
 */
export function parseDesktopBehaviorRefusals(err: unknown): DesktopBehaviorRefusal[] {
  if (!Array.isArray(err))
    return []
  return err.flatMap((entry) => {
    if (typeof entry !== 'object' || entry === null)
      return []
    const { setting, message } = entry as { setting?: unknown, message?: unknown }
    if (typeof message !== 'string' || message === '')
      return []
    if (setting !== 'trayEnabled' && setting !== 'startOnLogin')
      return []
    return [
      { setting, message },
    ]
  })
}

export interface DesktopRuntimeState {
  shellMode: 'launcher' | 'solo' | 'distributed'
  connected: boolean
  hubUrl: string
  capabilities: PlatformCapabilities
}

/** The ceremony that the system browser runs for the desktop's session. */
export type BrowserAuthKind = 'sign-in' | 'step-up'

export interface BrowserAuthStart {
  ceremonyId: string
  authorizeUrl: string
}

/**
 * One proxied HTTP round trip, as the desktop sidecar's raw response envelope
 * decodes it: the status and headers from its meta block, and the body bytes
 * sliced out of the envelope buffer.
 */
export interface ProxyHttpResponse {
  status: number
  headers: Record<string, string[]>
  body: ArrayBuffer
}

/**
 * The unary proxy response envelope, shared with the Rust shell's
 * `encode_proxy_response_envelope` (desktop/rust/src/main.rs):
 *
 *     [u32 LE meta_len][meta JSON][body]
 *
 * The meta JSON is `{"status": <int>, "headers": {<name>: [<value>, ...]}}`.
 * The 4-byte length prefix matches contracts/wire.json's
 * `framing.lengthPrefixBytes` (this envelope reads it LITTLE-endian, unlike
 * that contract's big-endian WebSocket frame prefix), and the meta block is
 * budgeted by the same contract's `maxMessageSizeBytes` frame budget. The
 * body follows the meta block verbatim -- no base64 anywhere on this path,
 * which is the whole point: for multi-MB payloads the encode/decode
 * round-trip was the dominant cost of the desktop transport.
 */
export const PROXY_ENVELOPE_META_PREFIX_BYTES = 4

/** Encode one proxied response into `[u32 LE meta_len][meta JSON][body]`. */
export function encodeProxyResponseEnvelope(resp: ProxyHttpResponse): ArrayBuffer {
  const meta = new TextEncoder().encode(JSON.stringify({
    status: resp.status,
    headers: resp.headers,
  }))
  if (meta.byteLength > DEFAULT_MAX_MESSAGE_SIZE)
    throw new Error('proxy response meta exceeds the frame budget')
  const out = new Uint8Array(PROXY_ENVELOPE_META_PREFIX_BYTES + meta.byteLength + resp.body.byteLength)
  new DataView(out.buffer).setUint32(0, meta.byteLength, true)
  out.set(meta, PROXY_ENVELOPE_META_PREFIX_BYTES)
  out.set(new Uint8Array(resp.body), PROXY_ENVELOPE_META_PREFIX_BYTES + meta.byteLength)
  return out.buffer
}

/**
 * Decode the raw envelope `proxy_http` resolves with, slicing the body out of
 * the buffer. The prefix and the meta block are validated before either is
 * trusted: the length is read little-endian, checked against the frame budget
 * (contracts/wire.json `maxMessageSizeBytes`) and against the bytes actually
 * present, so a corrupt prefix cannot make the slice read past the buffer.
 */
export function decodeProxyResponseEnvelope(bytes: ArrayBuffer): ProxyHttpResponse {
  if (bytes.byteLength < PROXY_ENVELOPE_META_PREFIX_BYTES)
    throw new Error('proxy response envelope is truncated')
  const metaLen = new DataView(bytes).getUint32(0, true)
  if (metaLen > DEFAULT_MAX_MESSAGE_SIZE)
    throw new Error('proxy response meta exceeds the frame budget')
  const bodyStart = PROXY_ENVELOPE_META_PREFIX_BYTES + metaLen
  if (bodyStart > bytes.byteLength)
    throw new Error('proxy response envelope is truncated')
  const meta: { status?: unknown, headers?: unknown } = JSON.parse(
    new TextDecoder().decode(new Uint8Array(bytes, PROXY_ENVELOPE_META_PREFIX_BYTES, metaLen)),
  )
  return {
    status: typeof meta.status === 'number'
      ? meta.status
      : 200,
    headers: (meta.headers ?? {}) as Record<string, string[]>,
    body: bytes.slice(bodyStart),
  }
}

export interface TunnelConfig {
  workerId: string
  type: 'port_forward' | 'socks5'
  targetAddr: string
  targetPort: number
  bindAddr: string
  bindPort: number
}

export interface TunnelInfo {
  id: string
  workerId: string
  type: 'port_forward' | 'socks5'
  bindAddr: string
  bindPort: number
  targetAddr: string
  targetPort: number
}

/**
 * One application the desktop machine can open a directory in.
 *
 * The id and the name are all the sidecar sends. What the application IS is a
 * compile-time fact of `contracts/external-apps.json`, which the browser reads
 * from its own generated table -- ask it through `~/lib/externalApps`'s
 * `isFileManager`, never by comparing an id here.
 */
export interface ExternalApp {
  id: string
  displayName: string
}

// Tagged unions the UI consumes. Mirror CliPathStatusResponse and
// CliInstallSymlinkResponse in proto/leapmux/desktop/v1/frame.proto.
export type CliPathTargetKind = 'absent' | 'symlink' | 'regular_file' | 'unknown'

export type CliPathStatus
  = | { state: 'ok', bundled: string }
    | { state: 'missing', bundled: string, target: string, targetKind: CliPathTargetKind }
    | { state: 'mismatch', bundled: string, resolved: string, targetKind: CliPathTargetKind }
    | { state: 'unavailable' }

export type CliInstallResult
  = | { result: 'ok' }
    | { result: 'needs_sudo', command: string }
    | { result: 'already_exists_real_file', path: string }
    | { result: 'parent_missing', path: string, command: string }
    | { result: 'io_error', message: string }

interface CliPathStatusPayload {
  state: number
  bundled: string
  resolved: string
  target: string
  targetKind: number
}

interface CliInstallSymlinkPayload {
  result: number
  command: string
  path: string
  message: string
}

// Numeric codes mirror the proto enums in frame.proto. The Tauri commands
// return raw i32 (prost-generated enums aren't serde-Serialize), so we
// translate here once at the boundary.
const PathState = { OK: 1, MISSING: 2, MISMATCH: 3 } as const
const TargetKind = { ABSENT: 1, SYMLINK: 2, REGULAR_FILE: 3 } as const
const InstallResultCode = {
  OK: 1,
  NEEDS_SUDO: 2,
  ALREADY_EXISTS_REAL_FILE: 3,
  PARENT_MISSING: 4,
  IO_ERROR: 5,
} as const

function decodeTargetKind(n: number): CliPathTargetKind {
  switch (n) {
    case TargetKind.ABSENT: return 'absent'
    case TargetKind.SYMLINK: return 'symlink'
    case TargetKind.REGULAR_FILE: return 'regular_file'
    // UNSPECIFIED (0) or any unknown value collapses to 'unknown' so the
    // dialog defaults to the safer two-click danger button — a permission
    // error reading the target shouldn't silently downgrade.
    default: return 'unknown'
  }
}

function decodeCliPathStatus(p: CliPathStatusPayload): CliPathStatus {
  switch (p.state) {
    case PathState.OK: return { state: 'ok', bundled: p.bundled }
    case PathState.MISSING: return {
      state: 'missing',
      bundled: p.bundled,
      target: p.target,
      targetKind: decodeTargetKind(p.targetKind),
    }
    case PathState.MISMATCH: return {
      state: 'mismatch',
      bundled: p.bundled,
      resolved: p.resolved,
      targetKind: decodeTargetKind(p.targetKind),
    }
    default: return { state: 'unavailable' }
  }
}

function decodeCliInstallResult(p: CliInstallSymlinkPayload): CliInstallResult {
  switch (p.result) {
    case InstallResultCode.OK: return { result: 'ok' }
    case InstallResultCode.NEEDS_SUDO: return { result: 'needs_sudo', command: p.command }
    case InstallResultCode.ALREADY_EXISTS_REAL_FILE: return { result: 'already_exists_real_file', path: p.path }
    case InstallResultCode.PARENT_MISSING: return { result: 'parent_missing', path: p.path, command: p.command }
    case InstallResultCode.IO_ERROR: return { result: 'io_error', message: p.message }
    default: return { result: 'io_error', message: p.message || 'unknown sidecar response' }
  }
}

declare global {
  interface Window {
    __TAURI_INTERNALS__?: unknown
    __leapmux_disconnectDesktop?: () => void
  }
}

let runtimeStateCache: DesktopRuntimeState | null = null

/**
 * Returns the connected Hub's identity, or null outside Tauri and before the
 * shell answers. The remote Hub's URL is what `hubOrigin()` and
 * `ceremoniesRunInBrowser()` read.
 */
export function desktopHub(): { kind: 'solo' } | { kind: 'remote', url: string } | null {
  if (!isTauriApp() || !runtimeStateCache)
    return null
  if (runtimeStateCache.shellMode === 'solo')
    return { kind: 'solo' }
  if (runtimeStateCache.shellMode === 'distributed' && runtimeStateCache.hubUrl)
    return { kind: 'remote', url: runtimeStateCache.hubUrl }
  return null
}
let runtimeStateFetch: Promise<DesktopRuntimeState> | null = null
let sidecarLogListening = false
const mobileUserAgentRegex = /android|iphone|ipad|ipod/i
const log = createLogger('platformBridge')
const sidecarLog = createLogger('sidecar')

function caps(mode: PlatformMode, overrides: Partial<Omit<PlatformCapabilities, 'mode'>> = {}): PlatformCapabilities {
  return {
    mode,
    tunnels: false,
    appControl: false,
    windowControl: false,
    systemPermissions: false,
    localSolo: false,
    ...overrides,
  }
}

const WEB_CAPABILITIES = caps('web')

const MOBILE_CAPABILITIES = caps('tauri-mobile-distributed', {
  appControl: true,
})

const DESKTOP_PENDING_CAPABILITIES = caps('tauri-desktop-distributed')

export function isTauriApp(): boolean {
  return typeof window.__TAURI_INTERNALS__ !== 'undefined'
}

export function isTunnelAvailable(): boolean {
  return getCapabilities().tunnels
}

function isMobileViewport(): boolean {
  return mobileUserAgentRegex.test(navigator.userAgent)
}

let cachedTauriCore: Promise<typeof import('@tauri-apps/api/core')> | null = null
let cachedTauriEvents: Promise<typeof import('@tauri-apps/api/event')> | null = null
let cachedTauriDpi: Promise<typeof import('@tauri-apps/api/dpi')> | null = null
let cachedTauriWindow: Promise<typeof import('@tauri-apps/api/window')> | null = null
let cachedTauriClipboard: Promise<typeof import('@tauri-apps/plugin-clipboard-manager')> | null = null
let cachedTauriOpener: Promise<typeof import('@tauri-apps/plugin-opener')> | null = null
let cachedTauriNotification: Promise<typeof import('@tauri-apps/plugin-notification')> | null = null

function loadTauriCore() {
  return (cachedTauriCore ??= import('@tauri-apps/api/core'))
}

function loadTauriEvents() {
  return (cachedTauriEvents ??= import('@tauri-apps/api/event'))
}

function loadTauriDpi() {
  return (cachedTauriDpi ??= import('@tauri-apps/api/dpi'))
}

function loadTauriWindow() {
  return (cachedTauriWindow ??= import('@tauri-apps/api/window'))
}

function loadTauriClipboard() {
  return (cachedTauriClipboard ??= import('@tauri-apps/plugin-clipboard-manager'))
}

function loadTauriOpener() {
  return (cachedTauriOpener ??= import('@tauri-apps/plugin-opener'))
}

function loadTauriNotification() {
  return (cachedTauriNotification ??= import('@tauri-apps/plugin-notification'))
}

async function tauriInvoke<T>(
  cmd: string,
  args?: import('@tauri-apps/api/core').InvokeArgs,
  options?: import('@tauri-apps/api/core').InvokeOptions,
): Promise<T> {
  const { invoke } = await loadTauriCore()
  return invoke<T>(cmd, args, options)
}

export function resetPlatformRuntimeState(): void {
  runtimeStateCache = null
  runtimeStateFetch = null
}

export function getCapabilities(): PlatformCapabilities {
  if (runtimeStateCache)
    return runtimeStateCache.capabilities
  if (isTauriApp() && isMobileViewport())
    return MOBILE_CAPABILITIES
  if (isTauriApp())
    return DESKTOP_PENDING_CAPABILITIES
  return WEB_CAPABILITIES
}

interface SidecarLogPayload {
  level: 'debug' | 'info' | 'warn' | 'error'
  time: string
  message: string
  attrs?: string[]
}

function installSidecarLogListener(): void {
  if (sidecarLogListening || !isTauriApp())
    return
  sidecarLogListening = true

  loadTauriEvents().then(({ listen }) => {
    listen<SidecarLogPayload>(TAURI_EVENT_SIDECAR_LOG, (event) => {
      const { level, time, message, attrs } = event.payload
      const args: unknown[] = [time, message]
      if (attrs)
        args.push(...attrs)
      sidecarLog[level](...args)
    })
  })
}

export async function getRuntimeState(): Promise<DesktopRuntimeState> {
  if (!isTauriApp()) {
    return {
      shellMode: 'distributed',
      connected: true,
      hubUrl: window.location.origin,
      capabilities: WEB_CAPABILITIES,
    }
  }

  installSidecarLogListener()

  if (runtimeStateCache)
    return runtimeStateCache
  runtimeStateFetch ??= tauriInvoke<DesktopRuntimeState>('get_runtime_state')
    .then((state) => {
      runtimeStateCache = state
      runtimeStateFetch = null
      return state
    })
  return runtimeStateFetch
}

export async function refreshRuntimeState(): Promise<DesktopRuntimeState> {
  resetPlatformRuntimeState()
  return getRuntimeState()
}

/** Minimal shape of the Tauri window needed to derive the display mode. */
interface WindowModeQueryable {
  isFullscreen: () => Promise<boolean>
  isMaximized: () => Promise<boolean>
}

/**
 * Derive the current mutually-exclusive window mode. Fullscreen takes
 * precedence over maximized (a native-fullscreen window may also report
 * zoomed, but fullscreen is the state that matters for save/restore).
 */
async function readWindowMode(win: WindowModeQueryable): Promise<WindowMode> {
  const [fullscreen, maximized] = await Promise.all([
    win.isFullscreen(),
    win.isMaximized(),
  ])
  return fullscreen
    ? WINDOW_MODE_FULLSCREEN
    : maximized ? WINDOW_MODE_MAXIMIZED : WINDOW_MODE_NORMAL
}

// Disposes the resize saver installed by the most recent restoreWindowGeometry
// call. The saver must outlive the launcher (geometry keeps saving after the
// user connects), so it can't be tied to a component's lifecycle — but the
// launcher can remount (switching connection mode returns to it), which re-runs
// restoreWindowGeometry. Tracking it here lets each restore replace the prior
// saver instead of stacking another resize listener + debounce timer.
let removeGeometrySaver: (() => void) | null = null

/**
 * Narrow the shell's launch decision, failing towards a visible window.
 *
 * The tokens come from the generated contract, not from literals here. This
 * parse answers `normal` for anything it does not recognize, so a token renamed
 * on the Rust side alone would show a window on every login launch that asked
 * to start in the tray -- and no test on either side would notice.
 */
export function parseLaunchVisibility(raw: unknown): LaunchVisibility {
  return raw === LAUNCH_VISIBILITY_HIDDEN || raw === LAUNCH_VISIBILITY_MINIMIZED
    ? raw
    : LAUNCH_VISIBILITY_NORMAL
}

/**
 * Restore the window to the saved geometry + mode on startup and install a
 * resize listener that debounces and saves via the Rust sidecar.
 *
 * Uses `window.innerWidth/Height` (CSS viewport) which matches what
 * Tauri's `setSize()` expects — this avoids the GTK CSD offset issue
 * where `appWindow.innerSize()` includes shadow + header bar.
 */
export async function restoreWindowGeometry(
  width: number,
  height: number,
  mode: WindowMode,
  launch: LaunchVisibility = LAUNCH_VISIBILITY_NORMAL,
): Promise<void> {
  if (!isTauriApp())
    return

  try {
    const { LogicalSize } = await loadTauriDpi()
    const { getCurrentWindow } = await loadTauriWindow()
    const appWindow = getCurrentWindow()

    // Establish the windowed size first so exiting maximized/fullscreen
    // returns to the saved geometry. This runs for a HIDDEN launch too, so the
    // first "Show LeapMux" from the tray produces a correctly sized window.
    if (width > 0 && height > 0)
      await appWindow.setSize(new LogicalSize(width, height))

    // Every launch restores maximized, a HIDDEN one included. `maximize()`
    // sets a window-state flag that an unmapped window keeps, so the first
    // "Show LeapMux" from the tray produces the maximized window the user left
    // behind rather than a small one.
    if (mode === WINDOW_MODE_MAXIMIZED)
      await appWindow.maximize()

    // Show the window now that it's at the correct size. The window starts
    // hidden (visible: false in tauri.conf.json) so the Wayland compositor
    // sees the final size at first map. macOS only performs the fullscreen
    // transition on a visible window, so show before entering fullscreen.
    //
    // A login launch the user asked to start out of the way skips this: with
    // a tray icon the shell decided `hidden`, without one it decided
    // `minimized`, and either way the window must not be raised in front of
    // whatever else they opened.
    if (launch !== 'hidden')
      await appWindow.show()

    if (launch === 'minimized')
      await appWindow.minimize()

    // Fullscreen is the one mode a launch that starts out of the way cannot
    // restore. macOS performs the transition on a VISIBLE window only, and a
    // window that goes straight to the tray or the taskbar is not one.
    let fullscreenDeferred = mode === WINDOW_MODE_FULLSCREEN && launch !== LAUNCH_VISIBILITY_NORMAL
    if (mode === WINDOW_MODE_FULLSCREEN && !fullscreenDeferred)
      await appWindow.setFullscreen(true)

    // Save window geometry + mode on resize / maximize / fullscreen, debounced.
    const saveGeometry = trailingDebounce(async () => {
      try {
        // Hiding to the tray needs no guard here, although it looks like it
        // should. This saver reads the DOM viewport, which an unmapped window
        // does not change, and the one platform that does report a size on the
        // way out (Windows sends 0x0 for a minimize) is already covered:
        // `App.SetWindowSize` ignores a non-positive dimension, so the last
        // windowed geometry survives. A visibility check here would instead
        // add a way to stop saving geometry for good, because a failed or
        // unexpected `isVisible` reads as "not visible".
        const observed = await readWindowMode(appWindow)
        // A launch that could not restore fullscreen must not let the first
        // resize DESTROY it. The window is windowed only because the launch
        // left it that way, so the saved mode is still what the user chose,
        // and reporting the observed `normal` would overwrite it for good.
        // Any other observation means the user took control of the mode, and
        // from then on the observation is the truth.
        if (fullscreenDeferred && observed !== WINDOW_MODE_NORMAL)
          fullscreenDeferred = false
        const nextMode = fullscreenDeferred ? mode : observed
        // innerWidth/Height report the screen size while maximized/fullscreen;
        // send 0 so the sidecar preserves the last windowed dimensions.
        const windowed = observed === WINDOW_MODE_NORMAL
        tauriInvoke('save_window_geometry', {
          width: windowed ? window.innerWidth : 0,
          height: windowed ? window.innerHeight : 0,
          mode: nextMode,
        }).catch(err => log.warn('save_window_geometry failed', err))
      }
      catch (err) {
        log.warn('save_window_geometry failed', err)
      }
    }, 500)
    // Replace the saver from any previous restore so remounts don't leak a
    // resize listener + debounce timer (each closure also pins `appWindow`).
    removeGeometrySaver?.()
    window.addEventListener('resize', saveGeometry)
    removeGeometrySaver = () => {
      window.removeEventListener('resize', saveGeometry)
      saveGeometry.cancel()
    }
  }
  catch (err) {
    log.warn('restoreWindowGeometry failed', err)
  }
}

function tauriFireAndForget(cmd: string, args?: Record<string, unknown>): void {
  if (!isTauriApp())
    return
  tauriInvoke(cmd, args).catch(err => log.warn(`${cmd} failed`, { args, err }))
}

export function quitApp(): void {
  tauriFireAndForget('quit_app')
}

export function zoomInWebview(): void {
  tauriFireAndForget('zoom_in_webview')
}

export function zoomOutWebview(): void {
  tauriFireAndForget('zoom_out_webview')
}

export function resetWebviewZoom(): void {
  tauriFireAndForget('reset_webview_zoom')
}

export function openWebInspector(): void {
  tauriFireAndForget('open_web_inspector')
}

export function setMenuItemAccelerator(itemId: string, accelerator?: string): void {
  tauriFireAndForget('set_menu_item_accelerator', {
    itemId,
    accelerator: accelerator ?? null,
  })
}

async function tauriWindowOp(fn: (win: import('@tauri-apps/api/window').Window) => Promise<void>): Promise<void> {
  if (!isTauriApp())
    return
  const { getCurrentWindow } = await loadTauriWindow()
  await fn(getCurrentWindow())
}

/**
 * Reads a PNG image from the OS clipboard via the Tauri clipboard-manager
 * plugin. Returns null when not running in Tauri, when the clipboard holds
 * no image, or when encoding fails.
 *
 * Needed because WebKitGTK (Tauri's webview on Linux) delivers an empty
 * `DataTransfer` for image pastes, so the standard `paste` event can't
 * recover the bytes from the web layer alone.
 *
 * The plugin's `Image.rgba()` returns raw RGBA pixels; we encode to PNG
 * via a canvas so callers get a directly-uploadable `File`.
 */
export async function readClipboardImage(): Promise<File | null> {
  if (!isTauriApp())
    return null
  const { readImage } = await loadTauriClipboard()
  // Image extends Resource — the Rust-side handle must be released
  // explicitly or it stays alive in the resources_table until app exit.
  const image = await readImage().catch(() => null)
  if (!image)
    return null
  try {
    const [rgba, size] = await Promise.all([
      image.rgba(),
      image.size(),
    ])
    if (size.width === 0 || size.height === 0)
      return null
    const canvas = document.createElement('canvas')
    canvas.width = size.width
    canvas.height = size.height
    const ctx = canvas.getContext('2d')
    if (!ctx)
      return null
    // Alias the plugin's Uint8Array as Uint8ClampedArray (zero-copy). The
    // cast is needed because the plugin's buffer is typed ArrayBufferLike,
    // not narrowed to ArrayBuffer as ImageData expects.
    const clamped = new Uint8ClampedArray(rgba.buffer, rgba.byteOffset, rgba.byteLength) as Uint8ClampedArray<ArrayBuffer>
    const imageData = new ImageData(clamped, size.width, size.height)
    ctx.putImageData(imageData, 0, 0)
    const blob = await new Promise<Blob | null>((resolve) => {
      canvas.toBlob(resolve, 'image/png')
    })
    if (!blob)
      return null
    // Filename is overwritten downstream by useChatAttachments.addFiles via
    // nextPastedImageName; only the MIME survives.
    return new File([blob], 'pasted-image.png', { type: 'image/png' })
  }
  catch (err) {
    log.warn('readClipboardImage failed', err)
    return null
  }
  finally {
    await image.close().catch(err => log.warn('clipboard image close failed', err))
  }
}

/**
 * Streaming save handle returned by `fileSaveOpen` / `fileSaveOpenDialog`.
 * `id` is the Rust-side registry id (a monotonic u64); `path` is the
 * absolute path that was opened so callers can pass it to
 * `revealInFileManager` after a successful close.
 */
export interface SaveStreamHandle {
  id: number
  path: string
}

/**
 * Open a destination file under the OS Downloads directory and return
 * a streaming handle. The Rust side keeps the `File` open between
 * calls; the caller streams bytes through `fileSaveWrite` and finalizes
 * with `fileSaveCommit` (or `fileSaveAbort` on failure).
 *
 * The Rust command sanitizes `filename` to its basename so callers can't
 * escape the Downloads dir. The filename rides through a header,
 * base64-encoded so non-ASCII names survive the HTTP-style header
 * value restrictions.
 */
export async function fileSaveOpen(filename: string): Promise<SaveStreamHandle> {
  return tauriInvoke<SaveStreamHandle>('file_save_open', undefined, {
    headers: {
      'filename-b64': arrayBufferToBase64(filename),
    },
  })
}

/**
 * Show a native save-as dialog, open the chosen path, and return a
 * streaming handle. Returns `null` if the user cancelled the dialog —
 * callers should short-circuit before any worker fetch so a cancelled
 * save doesn't pay the full read cost. Tauri-only.
 */
export async function fileSaveOpenDialog(defaultName: string): Promise<SaveStreamHandle | null> {
  return tauriInvoke<SaveStreamHandle | null>('file_save_open_dialog', undefined, {
    headers: {
      'default-name-b64': arrayBufferToBase64(defaultName),
    },
  })
}

/**
 * Append `chunk` to the file identified by `id`. Bytes ride the Tauri
 * IPC as a raw request body (no JSON / base64 conversion). Per-chunk,
 * so the transient memory peak is bounded to the chunk size across the
 * entire JS-webview-Rust copy chain.
 */
export async function fileSaveWrite(id: number, chunk: Uint8Array): Promise<void> {
  await tauriInvoke<void>('file_save_write', chunk, {
    headers: {
      'handle-id': String(id),
    },
  })
}

/**
 * Finalize the handle identified by `id` by atomic-renaming the temp
 * partial onto the final path. Errors on rename remove the partial so a
 * half-save doesn't land under the user's chosen name.
 */
export async function fileSaveCommit(id: number): Promise<void> {
  await tauriInvoke<void>('file_save_commit', undefined, {
    headers: {
      'handle-id': String(id),
    },
  })
}

/**
 * Discard the handle identified by `id` and remove its partial file.
 * Idempotent against an already-removed handle, so the JS pump's
 * failure path can call this without checking whether the Rust side
 * already cleaned up.
 */
export async function fileSaveAbort(id: number): Promise<void> {
  await tauriInvoke<void>('file_save_abort', undefined, {
    headers: {
      'handle-id': String(id),
    },
  })
}

/**
 * Open the OS file manager (Finder / Explorer / Files) with the given
 * path selected. Best-effort: failures are swallowed since this is a
 * post-save nicety, not a load-bearing operation.
 */
export async function revealInFileManager(path: string): Promise<void> {
  if (!isTauriApp())
    return
  try {
    const { revealItemInDir } = await loadTauriOpener()
    await revealItemInDir(path)
  }
  catch (err) {
    log.warn('revealInFileManager failed', err)
  }
}

/**
 * The schemes `openExternalUrl` will hand to the operating system.
 *
 * The same four the opener plugin's own `allow-default-urls` scope permits, so
 * this gateway refuses in the webview what the Rust side would refuse anyway --
 * with a log line that names the URL, instead of a rejected command.
 */
const EXTERNAL_URL_PROTOCOLS: readonly string[] = ['http:', 'https:', 'mailto:', 'tel:']

/**
 * Open a URL outside the app: the OS default browser under the desktop shell,
 * a new tab in the browser build.
 *
 * The desktop branch invokes the opener plugin, which is the same
 * `plugin:opener|open_url` command that plugin's injected click listener runs
 * for an `<a target="_blank">`. That listener is why a link in the chat
 * transcript reaches the browser, and it keys on a real anchor element in the
 * click's composed path -- so a caller that HAS no anchor (xterm draws a link
 * in its own link layer and dispatches from JavaScript) can only take this
 * route.
 *
 * It reports rather than rejects. A caller fires this from an event handler
 * with no error sink, and a rejection there reaches the global sink as
 * "Something went wrong", which tells the user nothing.
 */
export async function openExternalUrl(url: string): Promise<void> {
  let parsed: URL
  try {
    parsed = new URL(url)
  }
  catch {
    log.warn('refused to open a URL that does not parse', { url })
    return
  }
  if (!EXTERNAL_URL_PROTOCOLS.includes(parsed.protocol)) {
    log.warn('refused to open a URL outside the allowed schemes', { url })
    return
  }

  if (!isTauriApp()) {
    // `noopener` also makes the opened tab's `window.opener` null, so the
    // destination cannot navigate this page. It is why the return value is
    // ignored: `window.open` returns null WHENEVER `noopener` is set, so null
    // says nothing about whether the tab opened and cannot report a refusal.
    window.open(url, '_blank', 'noopener,noreferrer')
    return
  }
  try {
    const { openUrl } = await loadTauriOpener()
    await openUrl(url)
  }
  catch (err) {
    showWarnToastWithLoggedCause('Could not open the link', err)
  }
}

/**
 * Push the resolved Desktop preferences into the shell.
 *
 * It REJECTS rather than swallowing, unlike `tauriFireAndForget`, because two
 * of the things it asks for can fail outside the app: an operating system may
 * refuse a login-item registration, and a Linux desktop may have no
 * status-icon library. Both look like "LeapMux ignores my settings" when they
 * stay silent, so the caller surfaces them.
 */
export async function setDesktopBehavior(behavior: DesktopBehavior): Promise<void> {
  if (!isTauriApp())
    return
  await tauriInvoke<void>('set_desktop_behavior', { behavior })
}

export const windowMinimize = () => tauriWindowOp(w => w.minimize())
export const windowClose = () => tauriWindowOp(w => w.close())
export const windowToggleMaximize = () => tauriWindowOp(w => w.toggleMaximize())
// Leave native fullscreen. The custom titlebar (Linux/Windows) offers no other
// exit affordance once a window is restored into fullscreen, so this backs the
// "Exit Full Screen" control.
export const windowExitFullscreen = () => tauriWindowOp(w => w.setFullscreen(false))

/**
 * Subscribe to window display-mode changes (normal / maximized / fullscreen).
 * Invokes `onChange` with the current mode on attach and whenever Tauri reports
 * a resize — but only when the mode actually changes, so drag-resize doesn't
 * fire an IPC storm. Native fullscreen enter/exit also surfaces as a resize.
 * Returns an unlisten function.
 */
export function observeWindowMode(onChange: (mode: WindowMode) => void): () => void {
  if (!isTauriApp())
    return () => {}

  let disposed = false
  let unlisten: (() => void) | undefined
  let last: WindowMode | undefined
  let refresh: TrailingDebounced | undefined

  const push = (next: WindowMode) => {
    if (disposed || next === last)
      return
    last = next
    onChange(next)
  }

  ;(async () => {
    const { getCurrentWindow } = await loadTauriWindow()
    if (disposed)
      return
    const win = getCurrentWindow()
    try {
      push(await readWindowMode(win))
    }
    catch { /* best-effort */ }
    if (disposed)
      return
    try {
      refresh = trailingDebounce(async () => {
        try {
          push(await readWindowMode(win))
        }
        catch { /* best-effort */ }
      }, 150)
      unlisten = await win.onResized(refresh)
    }
    catch { /* best-effort */ }
    if (disposed) {
      refresh?.cancel()
      unlisten?.()
    }
  })()

  return () => {
    disposed = true
    refresh?.cancel()
    unlisten?.()
  }
}

export const platformBridge = {
  getCapabilities,
  getRuntimeState,
  fileSaveOpen,
  fileSaveOpenDialog,
  fileSaveWrite,
  fileSaveCommit,
  fileSaveAbort,
  revealInFileManager,
  openExternalUrl,
  async connectSolo(): Promise<void> {
    await tauriInvoke('connect_solo')
    await refreshRuntimeState()
  },
  async connectDistributed(hubUrl: string): Promise<void> {
    await tauriInvoke('connect_distributed', { hubUrl })
    await refreshRuntimeState()
  },
  async startBrowserAuth(kind: BrowserAuthKind): Promise<BrowserAuthStart> {
    // The kind crosses as the proto ordinal the sidecar forwards in
    // StartBrowserAuthRequest.kind. The map is exhaustive over the token
    // union, so a new token is a compile error instead of a wrong kind.
    const protoKind: Record<BrowserAuthKind, ProtoBrowserAuthKind> = {
      'sign-in': ProtoBrowserAuthKind.SIGN_IN,
      'step-up': ProtoBrowserAuthKind.STEP_UP,
    }
    return tauriInvoke<BrowserAuthStart>('start_browser_auth', { kind: protoKind[kind] })
  },
  async cancelBrowserAuth(ceremonyId: string): Promise<void> {
    await tauriInvoke('cancel_browser_auth', { ceremonyId })
  },
  /**
   * One proxied HTTP round trip through the desktop sidecar. The request body
   * crosses the Tauri IPC as the raw invoke body (the `fileSaveWrite`
   * pattern); the method, the path and the proxied headers ride InvokeOptions
   * headers (the `fileSaveOpen` pattern), because the invoke-args slot holds
   * the raw body. The proxied headers are arbitrary Unicode pairs and an
   * invoke header value must stay visible ASCII, so the map rides
   * base64-encoded JSON -- the `filename-b64` pattern.
   *
   * Resolves with the raw response envelope (see
   * `decodeProxyResponseEnvelope`), never a JSON body.
   */
  async proxyHttp(method: string, path: string, headers: Record<string, string>, body: ArrayBuffer | Uint8Array | string | null | undefined): Promise<ArrayBuffer> {
    const bytes = body === null || body === undefined
      ? new Uint8Array(0)
      : typeof body === 'string'
        ? new TextEncoder().encode(body)
        : body
    return tauriInvoke<ArrayBuffer>('proxy_http', bytes, {
      headers: {
        method,
        path,
        'headers-b64': arrayBufferToBase64(JSON.stringify(headers)),
      },
    })
  },
  // Returns null off-Tauri or off-macOS so callers can skip the IPC.
  async cliPathStatus(): Promise<CliPathStatus | null> {
    if (!isTauriApp() || !isMac())
      return null
    const payload = await tauriInvoke<CliPathStatusPayload>('cli_path_status')
    return decodeCliPathStatus(payload)
  },
  // `force=true` overwrites a regular (non-symlink) file at the install
  // destination. Symlinks are always replaced regardless of `force`.
  async cliInstallSymlink(force = false): Promise<CliInstallResult> {
    const payload = await tauriInvoke<CliInstallSymlinkPayload>('cli_install_symlink', { force })
    return decodeCliInstallResult(payload)
  },
  // The sidecar checks the id before acting on a close. It runs open and
  // close requests on separate goroutines, so a stale close can arrive late.
  async openChannelRelay(relayId: number, receiver: RelayReceiver): Promise<boolean> {
    return openRelay('open_channel_relay', { relayId }, receiver)
  },
  async sendChannelMessage(data: ArrayBuffer | Uint8Array): Promise<void> {
    await tauriInvoke('send_channel_message', data)
  },
  async closeChannelRelay(relayId: number): Promise<void> {
    await tauriInvoke('close_channel_relay', { relayId })
  },
  // The Go sidecar opens the user-events WebSocket on behalf of the
  // webview. Each attempt replaces the prior subscription so the Hub sends
  // a fresh bootstrap. The sidecar uses relayId to reject superseded opens.
  // A resume cursor crosses IPC as a wire HLC string and an epoch string.
  async openUserEventsRelay(
    relayId: number,
    receiver: RelayReceiver,
    workspaceIds: string[] = [],
    resume?: { hlc: { physical: bigint, logical: bigint, clientId: string }, epoch: bigint } | null,
  ): Promise<boolean> {
    return openRelay('open_userevents_relay', {
      relayId,
      workspaceIds,
      resumeHlc: resume
        ? formatHlcWire(resume.hlc)
        : null,
      resumeEpoch: resume
        ? resume.epoch.toString()
        : null,
    }, receiver)
  },
  async closeUserEventsRelay(relayId: number): Promise<void> {
    await tauriInvoke('close_userevents_relay', { relayId })
  },
  async getStartupInfo(): Promise<StartupInfo> {
    const wire = await tauriInvoke<StartupInfoWire>('get_startup_info')
    return {
      config: wire.config,
      buildInfo: {
        version: wire.build_info.version,
        commitHash: wire.build_info.commit_hash,
        commitTime: wire.build_info.commit_time,
        buildTime: wire.build_info.build_time,
        branch: wire.build_info.branch,
      },
      // A value this build does not know narrows to `normal`, which shows the
      // window. Failing towards a VISIBLE window is the only safe direction:
      // the alternative leaves an app the user cannot reach.
      launchVisibility: parseLaunchVisibility(wire.launch_visibility),
    }
  },
  async checkFullDiskAccess(): Promise<boolean> {
    return tauriInvoke('check_full_disk_access')
  },
  async openNoticeWindow(): Promise<void> {
    await tauriInvoke('open_notice_window')
  },
  async openFullDiskAccessSettings(): Promise<void> {
    await tauriInvoke('open_full_disk_access_settings')
  },
  async restart(): Promise<void> {
    await tauriInvoke('restart_app')
  },
  async switchMode(): Promise<void> {
    await tauriInvoke('switch_mode')
    await refreshRuntimeState()
  },
  async createTunnel(config: TunnelConfig): Promise<TunnelInfo> {
    return tauriInvoke('create_tunnel', { config })
  },
  async deleteTunnel(tunnelId: string): Promise<void> {
    await tauriInvoke('delete_tunnel', { tunnelId })
  },
  async resetTunnels(): Promise<void> {
    // No-op off the desktop: tunnels only exist in the two desktop modes, so
    // there is no sidecar to reset in the browser. Guarding here keeps callers
    // (AuthContext logout / auth-error) mode-agnostic.
    if (!isTauriApp())
      return
    await tauriInvoke('reset_tunnels')
  },
  async listTunnels(): Promise<TunnelInfo[]> {
    return (await tauriInvoke<TunnelInfo[]>('list_tunnels')) ?? []
  },
  async listExternalApps(refresh = false): Promise<ExternalApp[]> {
    if (!isTauriApp())
      return []
    return (await tauriInvoke<ExternalApp[]>('list_external_apps', { refresh })) ?? []
  },
  async openInExternalApp(appId: string, path: string): Promise<void> {
    await tauriInvoke('open_in_external_app', { appId, path })
  },
  async onEvent(event: string, callback: (...args: unknown[]) => void): Promise<() => void> {
    if (!isTauriApp())
      return () => {}
    const { listen } = await loadTauriEvents()
    return listen(event, payload => callback(payload.payload))
  },
  async requestNotificationPermission(): Promise<boolean> {
    if (!isTauriApp())
      return false
    const { isPermissionGranted, requestPermission } = await loadTauriNotification()
    if (await isPermissionGranted())
      return true
    const result = await requestPermission()
    return result === 'granted'
  },
  async showNotification(title: string, body: string, _tag?: string): Promise<void> {
    if (!isTauriApp())
      return
    const { isPermissionGranted, sendNotification } = await loadTauriNotification()
    if (!(await isPermissionGranted()))
      throw new Error('notification permission denied')
    sendNotification({ title, body })
  },
}

interface RelayClosePayload {
  code: number
  reason: string
  wasClean: boolean
}

type RelayMessage = ArrayBuffer | RelayClosePayload

export interface RelayReceiver {
  isAbandoned: () => boolean
  onMessage: (message: RelayMessage) => void
}

async function openRelay(
  command: 'open_channel_relay' | 'open_userevents_relay',
  args: Record<string, unknown>,
  receiver: RelayReceiver,
): Promise<boolean> {
  const { Channel, invoke } = await loadTauriCore()
  if (receiver.isAbandoned())
    return false

  // Attach the handler before invoking the open. The first frame can arrive
  // before the open reply, and the channel does not replay it.
  const channel = new Channel<unknown>((message) => {
    receiver.onMessage(message instanceof ArrayBuffer
      ? message
      : parseRelayClosePayload(message))
  })
  try {
    await invoke(command, { ...args, channel })
    return true
  }
  catch (error) {
    // Tauri drops the callback when a native Channel ends. If IPC rejects
    // before Rust constructs the Channel, only the webview can unregister it.
    const tauri = window.__TAURI_INTERNALS__ as { unregisterCallback: (id: number) => void }
    tauri.unregisterCallback(channel.id)
    throw error
  }
}

export function parseRelayClosePayload(payload: unknown): RelayClosePayload {
  const close = payload as Partial<RelayClosePayload> | null
  return {
    code: typeof close?.code === 'number'
      ? close.code
      : 1006,
    reason: typeof close?.reason === 'string'
      ? close.reason
      : '',
    wasClean: close?.wasClean === true,
  }
}

/**
 * desktopFetch is the unary-only ConnectRPC transport for the desktop
 * sidecar. The user-event subscription is not an RPC — it lives on the
 * `/ws/userevents` WebSocket endpoint (see
 * `frontend/src/components/shell/useUserEvents.ts`). WebSocket
 * negotiates Upgrade and bypasses HTTP/1.1 chunked-stream buffering
 * hazards (corporate proxies, Tauri's buffered fetch), which is why
 * the previous streaming RPC was retired.
 */
export const desktopFetch: typeof globalThis.fetch = async (input, init) => {
  const url = typeof input === 'string'
    ? input
    : (input as Request).url
  const method = init?.method ?? 'POST'
  const headers = Object.fromEntries(new Headers(init?.headers).entries())
  // The body crosses as the raw invoke body, not base64 (see proxyHttp).
  const body = (init?.body ?? null) as ArrayBuffer | Uint8Array | string | null
  const parsed = new URL(url)
  const path = parsed.pathname + parsed.search

  // The proxied request cannot be cancelled once sent, so an abort settles
  // only this call while the request runs on. The listener is removed as
  // soon as either side settles so a late abort cannot reject a call that
  // already has its answer.
  const signal = init?.signal ?? undefined
  if (signal?.aborted)
    throw new DOMException('The operation was aborted.', 'AbortError')
  let onAbort: (() => void) | undefined
  let proxied = platformBridge.proxyHttp(method, path, headers, body)
  if (signal) {
    const aborted = new Promise<never>((_, reject) => {
      onAbort = () => reject(new DOMException('The operation was aborted.', 'AbortError'))
      signal.addEventListener('abort', onAbort)
    })
    proxied = Promise.race([proxied, aborted])
  }
  let envelope: ArrayBuffer
  try {
    envelope = await proxied
  }
  finally {
    if (onAbort)
      signal?.removeEventListener('abort', onAbort)
  }
  const resp = decodeProxyResponseEnvelope(envelope)
  const responseHeaders = Object.entries(resp.headers).flatMap(([name, values]) =>
    values.map(value => [name, value] as [string, string]),
  )
  return new Response(resp.body, {
    status: resp.status,
    headers: responseHeaders,
  })
}
