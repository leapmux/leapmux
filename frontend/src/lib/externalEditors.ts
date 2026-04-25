import type { DetectedEditor } from '~/api/platformBridge'
import { platformBridge } from '~/api/platformBridge'
import { KEY_PREFERRED_EDITOR, safeGetJson, safeSetJson } from './browserStorage'
import { createIdentityCache } from './identityCache'

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
let pending: Promise<DetectedEditor[]> | null = null

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
    pending = null
  }
  if (cached !== null)
    return cached
  if (pending !== null)
    return pending
  pending = platformBridge.listEditors(refresh).then((list) => {
    cached = editorIdentity.stabilize(list)
    pending = null
    return cached
  }).catch((err) => {
    // Reset so a later caller can retry; surface the error to the awaiter.
    pending = null
    throw err
  })
  return pending
}

/** Reset the in-memory cache. Test-only helper; not exported via barrel. */
export function _resetEditorCacheForTests(): void {
  cached = null
  pending = null
  editorIdentity.clear()
}

export function getPreferredEditorId(): string | undefined {
  return safeGetJson<string>(KEY_PREFERRED_EDITOR)
}

export function setPreferredEditorId(id: string): void {
  safeSetJson(KEY_PREFERRED_EDITOR, id)
}
