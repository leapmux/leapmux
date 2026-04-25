import type { DetectedEditor } from '~/api/platformBridge'
import { platformBridge } from '~/api/platformBridge'
import { KEY_PREFERRED_EDITOR, safeGetJson, safeSetJson } from './browserStorage'
import { createIdentityCache } from './identityCache'
import { createInflightCache } from './inflightCache'

/** Stable IDs the frontend has icons / display logic for. Must match Go-side spec table. */
export const SUPPORTED_EDITOR_IDS = [
  'vscode',
  'vscode-insiders',
  'vscodium',
  'cursor',
  'windsurf',
  'sublime-text',
  'zed',
  'intellij-idea-ultimate',
  'intellij-idea-community',
  'webstorm',
  'goland',
  'rustrover',
  'pycharm-professional',
  'pycharm-community',
  'phpstorm',
  'rubymine',
  'clion',
  'rider',
  'datagrip',
  'android-studio',
  'fleet',
  'xcode',
  'notepad-plus-plus',
] as const

export type EditorId = typeof SUPPORTED_EDITOR_IDS[number]

let cached: DetectedEditor[] | null = null
const inflight = createInflightCache<'editors', DetectedEditor[]>()

// Reuse the previously-seen object reference for any editor whose id +
// displayName are unchanged. Solid's `<For>` then only unmounts editors
// that disappeared and mounts editors that arrived — instead of
// rebuilding all 23 menu items + their inline SVG icons on every refresh.
// See `lib/identityCache.ts` for why this matters.
const editorIdentity = createIdentityCache<DetectedEditor>({
  keyOf: e => e.id,
})

/**
 * Detected editors are cached per-process. The Go sidecar caches detection
 * the first time it's asked, so re-asking the Tauri command is also cheap,
 * but skipping the IPC round trip keeps the dropdown snappy.
 *
 * Pass `refresh: true` to invalidate both caches (frontend in-memory + Go
 * sidecar) and re-probe the filesystem. Used by the "Refresh editor list"
 * action after the user installs/uninstalls an editor.
 */
export async function loadDetectedEditors(refresh = false): Promise<DetectedEditor[]> {
  if (refresh) {
    cached = null
    inflight.clear()
  }
  if (cached !== null)
    return cached
  return inflight.run('editors', async () => {
    const list = await platformBridge.listEditors(refresh)
    cached = editorIdentity.stabilize(list)
    return cached
  })
}

/** Reset the in-memory cache. Test-only helper; not exported via barrel. */
export function _resetEditorCacheForTests(): void {
  cached = null
  inflight.clear()
  editorIdentity.clear()
}

export function getPreferredEditorId(): string | undefined {
  return safeGetJson<string>(KEY_PREFERRED_EDITOR)
}

export function setPreferredEditorId(id: string): void {
  safeSetJson(KEY_PREFERRED_EDITOR, id)
}

/**
 * Pick the editor to launch from a fresh detection list: the user's MRU if
 * still detected, otherwise the first available — and persist the new MRU
 * so subsequent invocations are stable. Returns undefined when the list is
 * empty (callers can decide whether to also clear in-memory MRU state).
 *
 * Used by both the keyboard-shortcut launch path and the post-refresh
 * fallback inside the menu component. Centralized here so they cannot
 * disagree about which editor a "default launch" picks.
 */
export function resolvePreferredEditor(editors: DetectedEditor[]): DetectedEditor | undefined {
  if (editors.length === 0)
    return undefined
  const mru = getPreferredEditorId()
  const target = editors.find(e => e.id === mru) ?? editors[0]
  if (target.id !== mru)
    setPreferredEditorId(target.id)
  return target
}
