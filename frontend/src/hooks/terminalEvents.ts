/**
 * The terminal lifecycle transitions, as module-level units.
 *
 * Both arms of `handleTerminalEvent` -- the active workspace's and the
 * background one's -- route through these. Which workspace a terminal lives in
 * decides whether there is an xterm instance to write `data` to, and nothing
 * else: status and git fields drive the SIDEBAR, which renders every workspace
 * at once. Re-stating a subset in one arm is how the status half went missing.
 */
import type { TerminalNotification, TerminalProgress, TerminalStatusChange, TerminalTitleChanged } from '~/generated/leapmux/v1/terminal_pb'
import type { createRepoGitStore } from '~/stores/repoGit.store'
import type { TerminalTab } from '~/stores/tab.types'
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { isTabOnScreen } from '~/hooks/watchPlan'
import { notifyOs } from '~/lib/osNotification'
import { protoToRepoGitPatch, repoKeyFromStatus } from '~/stores/repoGit'
import { tabKey } from '~/stores/tab.helpers'

/**
 * The terminal-exit transition, in one place.
 *
 * Both arms that observe a terminal dying write the same three fields, and the
 * two are ~50 lines apart in different branches of the same handler (one for a
 * terminal in a workspace the user is not looking at, one for the active
 * workspace). All three are load-bearing together: the old store's
 * `{...prev, ...fields}` spread let an `undefined` CLEAR `startupMessage`,
 * while `patch` skips undefined -- so a terminal that died DURING startup kept
 * its phase label and never set `contentReady`, and the restart rendered the
 * spinner labelled with the previous attempt's text. A fourth field added to
 * this transition should not have to be found twice.
 */
export function markTerminalExited(metadata: TabMetadataStore, terminalId: string): void {
  metadata.patch(terminalId, {
    terminalStatus: TerminalStatus.EXITED,
    startupMessage: '',
    contentReady: true,
  })
}

/**
 * The terminal `statusChange` transition, in one place.
 *
 * Shared by BOTH arms of `handleTerminalEvent` for the same reason
 * `markTerminalExited` is: which workspace a terminal lives in decides whether
 * there is an xterm instance to write `data` to, and nothing else. Status and
 * git fields drive the SIDEBAR, which renders every workspace at once.
 *
 * The background arm used to patch git fields only, so a STARTING -> READY or
 * STARTUP_FAILED transition for a terminal the user was not looking at never
 * reached `WorkspaceTabTree`'s `data-terminal-status` -- the row kept its
 * startup spinner until the user switched in and the resubscribe's catch-up
 * marker repaired it. `useTabHydrators` could not re-arm it either: `hydrated`
 * is write-once and the tab was not DISCONNECTED.
 */
export function applyTerminalStatusChange(
  metadata: TabMetadataStore,
  repoGitStore: ReturnType<typeof createRepoGitStore>,
  existingTab: TerminalTab | undefined,
  terminalId: string,
  sc: TerminalStatusChange,
): void {
  const workerId = existingTab?.workerId ?? ''
  const patch = protoToRepoGitPatch(workerId, sc.gitStatus)
  const key = workerId ? repoKeyFromStatus(workerId, sc.gitStatus) : undefined
  if (patch && key)
    repoGitStore.upsert(key, patch)
  if (sc.gitStatus?.toplevel)
    metadata.patch(terminalId, { gitToplevel: sc.gitStatus.toplevel })
  switch (sc.status) {
    case TerminalStatus.STARTING:
      if (existingTab && existingTab.status !== TerminalStatus.READY && existingTab.status !== TerminalStatus.STARTING) {
        metadata.patch(terminalId, {
          terminalStatus: TerminalStatus.STARTING,
          // `''`, not `undefined`: `patch` SKIPS undefined by design, so
          // `|| undefined` was a no-op that let the PREVIOUS attempt's
          // phase label survive onto this one. A restart reuses the same
          // terminal id, hence the same metadata row.
          startupMessage: sc.startupMessage || '',
        })
      }
      else if (existingTab?.status === TerminalStatus.STARTING && sc.startupMessage && sc.startupMessage !== existingTab.startupMessage) {
        // Same-status STARTING event with an updated phase label —
        // refresh the overlay text without re-triggering the
        // status-change observers.
        metadata.patch(terminalId, { startupMessage: sc.startupMessage })
      }
      break
    case TerminalStatus.READY:
      // Preserve DISCONNECTED / EXITED — a previously-alive terminal
      // whose worker reconnected should not be dragged back to READY.
      if (existingTab?.status === TerminalStatus.STARTING || existingTab?.status === undefined) {
        // `''`, not `undefined`: `patch` SKIPS undefined so a caller can
        // send a partial row without blanking fields another source owns.
        // Clearing therefore has to be an explicit empty value — the same
        // idiom `buildAgentStatusTabUpdate` uses for these two fields.
        metadata.patch(terminalId, {
          terminalStatus: TerminalStatus.READY,
          startupError: '',
          startupMessage: '',
        })
      }
      break
    case TerminalStatus.STARTUP_FAILED:
      metadata.patch(terminalId, {
        terminalStatus: TerminalStatus.STARTUP_FAILED,
        startupError: sc.startupError ?? '',
        startupMessage: '',
      })
      break
  }
}

function isWorkspaceActiveTerminal(
  terminalId: string,
  selection: TabSelectionStore,
  getActiveWorkspaceId: () => string | null,
  view?: { getTerminalTab: (id: string) => { tileId?: string, workspaceId?: string } | undefined },
): boolean {
  const tab = view?.getTerminalTab(terminalId)
  if (tab?.tileId) {
    // Tile-placed: the shared on-screen rule (same source as tabWatchMode).
    return isTabOnScreen(
      { tileId: tab.tileId, workspaceId: tab.workspaceId, type: TabType.TERMINAL, id: terminalId },
      getActiveWorkspaceId(),
      tileId => selection.activeKeyForTile(tileId),
    )
  }
  // No tile placement yet — fall back to the workspace-active key.
  return selection.activeKeyForWorkspace(getActiveWorkspaceId() ?? '') === tabKey({ type: TabType.TERMINAL, id: terminalId })
}

interface TerminalBadgeDeps {
  metadata: TabMetadataStore
  selection: TabSelectionStore
  getActiveWorkspaceId: () => string | null
  view?: { getTerminalTab: (id: string) => { tileId?: string, workspaceId?: string } | undefined }
}

/**
 * Badge a terminal tab when it is not on screen. Returns whether the tab was
 * active (so callers that have a follow-up action for the non-active case —
 * e.g. raising an OS notification — can gate on it without recomputing the
 * on-screen predicate. Both the bell and notification arms share this prelude.
 */
function badgeTerminalIfNotOnScreen(terminalId: string, deps: TerminalBadgeDeps): boolean {
  const active = isWorkspaceActiveTerminal(terminalId, deps.selection, deps.getActiveWorkspaceId, deps.view)
  if (!active)
    deps.metadata.patch(terminalId, { hasNotification: true })
  return active
}

/** NOTIFY-class bell: badge when the tab is not on-screen (tile-active). */
export function handleTerminalBell(terminalId: string, deps: TerminalBadgeDeps): void {
  badgeTerminalIfNotOnScreen(terminalId, deps)
}

/** NOTIFY-class OSC notification: badge + optional OS notification when not focused. */
export function handleTerminalNotification(
  terminalId: string,
  value: TerminalNotification,
  deps: TerminalBadgeDeps,
): void {
  // Skip the desktop notification when the user is already looking at this tab.
  if (badgeTerminalIfNotOnScreen(terminalId, deps))
    return
  const title = value.title || 'Terminal'
  const body = value.body || ''
  notifyOs({ title, body, tag: terminalId })
}

/**
 * NOTIFY-class PTY title from OSC 0/2.
 *
 * Patches only `ptyTitle` so an explicit user rename in `title` keeps winning
 * `tabDisplayLabel`. An empty OSC is a no-op (never clears a rename).
 */
export function handleTerminalTitleChanged(
  terminalId: string,
  value: TerminalTitleChanged,
  metadata: TabMetadataStore,
): void {
  if (!value.title)
    return
  metadata.patch(terminalId, { ptyTitle: value.title })
}

/** NOTIFY-class task progress (OSC 9;4). */
export function handleTerminalProgress(
  terminalId: string,
  value: TerminalProgress,
  metadata: TabMetadataStore,
): void {
  metadata.patch(terminalId, {
    progressState: value.state,
    progressPercent: value.percent,
  })
}
