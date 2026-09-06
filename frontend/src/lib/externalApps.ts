import type { Accessor } from 'solid-js'
import type { ExternalApp } from '~/api/platformBridge'
import type { ExternalAppId } from '~/generated/contracts/external-apps'
import { createSignal } from 'solid-js'
import { platformBridge } from '~/api/platformBridge'
import { showWarnToastWithLoggedCause } from '~/components/common/Toast'
import { EXTERNAL_APP_KIND_BY_ID } from '~/generated/contracts/external-apps'
import { createIdentityCache } from './identityCache'
import { createInflightCache } from './inflightCache'

// Reuse the previously-seen object reference for any app whose fields are
// unchanged. Solid's `<For>` then only unmounts apps that disappeared and
// mounts apps that arrived — instead of rebuilding all 24 menu items + their
// inline SVG icons on every refresh. See `lib/identityCache.ts` for why this
// matters.
const appIdentity = createIdentityCache<ExternalApp>({
  keyOf: a => a.id,
})

// The detected applications are ONE fact about the machine, so one signal holds
// them and every surface reads it.
//
// A per-consumer copy was the bug: each menu kept its own resource, and
// "Refresh app list" re-fetched only the copy belonging to the menu the user
// clicked in. The title bar's split button probes once and never again, so a
// refresh from a sidebar row never reached it — it went on naming an editor the
// user had just uninstalled, and launching it, for the life of the page.
const [detectedApps, setDetectedApps] = createSignal<ExternalApp[]>([])

/** Every detected application, in the order the sidecar reported them. */
export const externalApps: Accessor<ExternalApp[]> = detectedApps

const inflight = createInflightCache<'apps', ExternalApp[]>()
let loaded = false

// Which probe the signal belongs to. A refresh advances it, and a load that
// started under an older generation must not write its answer: that probe
// describes the machine as it WAS, and letting it land last would silently undo
// the refresh the user asked for.
let generation = 0

/**
 * Probe the machine, once per process, and publish the result to
 * {@link externalApps}.
 *
 * The Go sidecar caches detection the first time it is asked, so re-asking the
 * Tauri command is also cheap, but skipping the IPC round trip keeps the
 * dropdown snappy. Concurrent callers share one round trip.
 *
 * Pass `refresh: true` to invalidate both caches (this module and the Go
 * sidecar) and re-probe the filesystem, which is what the "Refresh app list"
 * action does after the user installs or uninstalls an editor.
 */
export async function loadExternalApps(refresh = false): Promise<ExternalApp[]> {
  if (refresh) {
    loaded = false
    generation++
    // `clear` does NOT cancel the factories already running; the generation
    // check below is what stops one of them from writing.
    inflight.clear()
  }
  if (loaded)
    return detectedApps()
  const started = generation
  return inflight.run('apps', async () => {
    const list = appIdentity.stabilize(await platformBridge.listExternalApps(refresh))
    if (started === generation) {
      loaded = true
      setDetectedApps(list)
    }
    return list
  })
}

/** Reset the module state. Test-only helper; not exported via barrel. */
export function _resetExternalAppCacheForTests(): void {
  loaded = false
  generation++
  inflight.clear()
  appIdentity.clear()
  setDetectedApps([])
}

/**
 * Whether `app` is the operating system's own file manager.
 *
 * The ONE place that answers it. The menus group by the answer, so an inline
 * comparison at each of those sites would be a rule spelled four times.
 *
 * Read from the generated contract table rather than from the wire: the kind
 * is a compile-time fact, and the sidecar reports only ids its own spec table
 * holds -- which a Go table test compares against this same contract in both
 * directions, so the two cannot name different sets. An id the table does not
 * know answers false, which is the safe direction: the menu then treats it as
 * an ordinary application instead of as an always-present group.
 */
export function isFileManager(app: ExternalApp | undefined): boolean {
  if (!app)
    return false
  return EXTERNAL_APP_KIND_BY_ID[app.id as ExternalAppId] === 'EXTERNAL_APP_KIND_FILE_MANAGER'
}

/**
 * One sentence a person can act on, from a rejected launch.
 *
 * The sidecar says WHY it could not launch — `launch Zed: exit status 1: The
 * application cannot be found` — and that reason is the whole point of the
 * window `startAndWatch` watches. It arrives as a plain STRING, because Tauri
 * rejects with the `Err(String)` the Rust command returned, and
 * `formatErrorMessage` answers its fallback alone for anything that is not an
 * `Error`. So the reason reached the log and never the screen.
 */
export function launchFailureMessage(displayName: string, err: unknown): string {
  const reason = typeof err === 'string'
    ? err.trim()
    : err instanceof Error ? err.message.trim() : ''
  return reason ? `Could not open ${displayName}: ${reason}` : `Could not open ${displayName}`
}

/**
 * Open `dir` in one application and report a refusal.
 *
 * The ONE launch path. Every surface reaches it: the four menus through
 * `useExternalApps`, and the keyboard shortcut directly, because that command
 * runs outside a reactive owner and cannot hold the hook. Both used to spell
 * the call, the message and the "surface it, do not only log it" rule for
 * themselves, so a rule added to one missed the other.
 *
 * Reporting is not optional. A failed launch looks exactly like an application
 * opening behind this window, so silence leaves the user with no way to tell
 * the two apart.
 *
 * Pinning stays with the CALLER, because the two paths differ there and should:
 * a menu received an explicit pick and remembers it, while the shortcut
 * received none and persists only the fallback it resolved.
 */
export function launchExternalApp(id: string, displayName: string, dir: string): void {
  platformBridge.openInExternalApp(id, dir).catch((err: unknown) => {
    showWarnToastWithLoggedCause(launchFailureMessage(displayName, err), err)
  })
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
 * With no usable pin the fallback prefers an EDITOR. The file manager leads the
 * detected list on every platform and is always present, so taking the first
 * entry would make it the answer for every user who never picked one -- and
 * the keyboard shortcut would open Finder on a machine with three editors
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
