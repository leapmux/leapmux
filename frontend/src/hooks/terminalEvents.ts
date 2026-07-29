/**
 * The terminal lifecycle transitions, as module-level units.
 *
 * Both arms of `handleTerminalEvent` -- the active workspace's and the
 * background one's -- route through these. Which workspace a terminal lives in
 * decides whether there is an xterm instance to write `data` to, and nothing
 * else: status and git fields drive the SIDEBAR, which renders every workspace
 * at once. Re-stating a subset in one arm is how the status half went missing.
 */
import type { TerminalStatusChange } from '~/generated/leapmux/v1/terminal_pb'
import type { TerminalTab } from '~/stores/tab.types'
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { gitTabFieldsDiffer, toGitTabFields } from '~/stores/tab.helpers'

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
  existingTab: TerminalTab | undefined,
  terminalId: string,
  sc: TerminalStatusChange,
): void {
  // Git branch / origin / toplevel are carried on every post-phase-0
  // STARTING broadcast. Update the tab whenever the probe answered at all, so
  // a reconnect or a late worktree-creation refreshes the badge. Whether it
  // answered is `toGitTabFields`' call, not this site's — re-stating the test
  // here is how the three producers drifted apart in the first place.
  const next = toGitTabFields(sc.gitBranch, sc.gitOriginUrl, sc.gitToplevel, sc.gitIsWorktree)
  if (existingTab && next && gitTabFieldsDiffer(existingTab, next))
    metadata.patch(terminalId, next)
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
