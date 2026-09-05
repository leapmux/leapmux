import type { ExternalApp } from '~/api/platformBridge'
import { platformBridge } from '~/api/platformBridge'
import { ExternalAppKind } from '~/generated/proto/leapmux/desktop/v1/frame_pb'
import { createIdentityCache } from './identityCache'
import { createInflightCache } from './inflightCache'

let cached: ExternalApp[] | null = null
const inflight = createInflightCache<'apps', ExternalApp[]>()

// Reuse the previously-seen object reference for any app whose id +
// displayName are unchanged. Solid's `<For>` then only unmounts apps
// that disappeared and mounts apps that arrived — instead of
// rebuilding all 24 menu items + their inline SVG icons on every refresh.
// See `lib/identityCache.ts` for why this matters.
const appIdentity = createIdentityCache<ExternalApp>({
  keyOf: a => a.id,
})

/**
 * Detected applications are cached per-process. The Go sidecar caches detection
 * the first time it's asked, so re-asking the Tauri command is also cheap,
 * but skipping the IPC round trip keeps the dropdown snappy.
 *
 * Pass `refresh: true` to invalidate both caches (frontend in-memory + Go
 * sidecar) and re-probe the filesystem. Used by the "Refresh app list"
 * action after the user installs/uninstalls an editor.
 */
export async function loadExternalApps(refresh = false): Promise<ExternalApp[]> {
  if (refresh) {
    cached = null
    inflight.clear()
  }
  if (cached !== null)
    return cached
  return inflight.run('apps', async () => {
    const list = await platformBridge.listExternalApps(refresh)
    cached = appIdentity.stabilize(list)
    return cached
  })
}

/** Reset the in-memory cache. Test-only helper; not exported via barrel. */
export function _resetExternalAppCacheForTests(): void {
  cached = null
  inflight.clear()
  appIdentity.clear()
}

/**
 * Whether `app` is the operating system's own file manager.
 *
 * Asked of the KIND the sidecar sent, never of the id: the menus group by it,
 * and the repository block drops its "Open in ..." row when the remembered
 * app is the file manager, because "Reveal in file manager" sits right above
 * and says almost the same thing. An id test would have to be repeated at
 * every one of those sites, and would miss a kind added later.
 */
export function isFileManager(app: ExternalApp | undefined): boolean {
  return app?.kind === ExternalAppKind.FILE_MANAGER
}

// NO storage accessors for the remembered app live here. The pin has one
// owner — the reactive preference in `~/context/PreferencesContext` — and a
// second, non-reactive reader/writer beside it is what put the app menu
// and the app a launch actually opened out of step. `resolvePreferredExternalApp`
// takes the pin and the writer as arguments for the same reason.

/**
 * Pick the application to launch from a fresh detection list: the current pin
 * if it is still detected, otherwise the first available — and persist the new
 * pin so later invocations are stable. Returns undefined when the list is
 * empty (callers can decide whether to also clear their in-memory state).
 *
 * Used by both the keyboard-shortcut launch path and the post-refresh
 * fallback inside the menu component. Centralized here so they cannot
 * disagree about which application a "default launch" picks.
 *
 * The caller supplies BOTH the current pin and the writer, so this function
 * touches no storage at all. Reading storage here while the caller wrote
 * through the reactive preference put the two out of step: another tab's
 * write reached storage but not this tab's signal, so the menu label and
 * the application a launch actually opened disagreed for the life of the page.
 * Both directions now come from the one source the caller already holds.
 */
export function resolvePreferredExternalApp(
  apps: ExternalApp[],
  pinned: string | undefined,
  persist: (id: string) => void,
): ExternalApp | undefined {
  const target = preferredExternalApp(apps, pinned)
  if (target && target.id !== pinned)
    persist(target.id)
  return target
}

/**
 * Which application a "default launch" opens, WITHOUT the write.
 *
 * Module-private: nothing outside needs the pick without the persistence, and
 * a surface that must only NAME the remembered application reads
 * `useExternalApps`'s `preferred` instead, which answers undefined rather than
 * falling back. Split from the writer above so the rule below has one home and
 * a name, not to give a second caller a way in.
 *
 * With no usable pin the fallback prefers an EDITOR. The file manager leads
 * the detected list on every platform and is always present, so taking the
 * first entry would make it the answer for every user who never picked one --
 * and the keyboard shortcut would open Finder on a machine with three editors
 * installed. It is still the answer when nothing else was detected, and an
 * explicit pick of it always wins, because the pin is tried first.
 */
function preferredExternalApp(
  apps: ExternalApp[],
  pinned: string | undefined,
): ExternalApp | undefined {
  if (apps.length === 0)
    return undefined
  const pick = apps.find(a => a.id === pinned)
  if (pick)
    return pick
  return apps.find(a => !isFileManager(a)) ?? apps[0]
}
