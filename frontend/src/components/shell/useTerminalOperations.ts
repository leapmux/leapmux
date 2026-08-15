import type { Accessor } from 'solid-js'
import type { TabContext } from './tabContext'
import type { CloseTabResult } from '~/generated/leapmux/v1/common_pb'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import type { ToggleDialogState } from '~/hooks/createDialogState'
import type { InputQueueDrain } from '~/lib/inputQueue'
import type { createLayoutStore } from '~/stores/layout.store'
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabSelectionStore } from '~/stores/tabSelection.store'

import type { TabView } from '~/stores/tabView'
import { apiLoadingTimeoutMs } from '~/api/transport'
import * as workerRpc from '~/api/workerRpc'
import { showWarnToast } from '~/components/common/Toast'
import { awaitCloseResult, warnWorktreeUnreachable } from '~/components/shell/closeResultToast'
import { disposeTerminalInstance } from '~/components/terminal/TerminalView'
import { WorktreeAction } from '~/generated/leapmux/v1/common_pb'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { useAvailableShells } from '~/hooks/useAvailableShells'
import { createInflightCache } from '~/lib/inflightCache'
import { createSharedInputQueues } from '~/lib/inputQueue'
import { DEFAULT_TERMINAL_COLS, DEFAULT_TERMINAL_ROWS, ENTER_KEY_CR } from '~/lib/terminal'
import { openedTerminalMetadata, resolveOptimisticGitInfo } from '~/stores/tab.helpers'
import { emitRemoveTab } from '~/stores/tabOps'
import { openTabInFocusedTile } from './openTabInFocusedTile'

// xterm emits Enter as a single CR byte on a non-modifier press.
// We gate the EXITED-tab restart flow on exactly that one byte so a stray
// keystroke (or the autorepeat from a held key) doesn't fire a restart.
// The byte itself lives in ~/lib/terminal with the other PTY wire constants.

// The pending input queue for each terminal, drained one SendInput at a time
// by the hook below. Created at MODULE scope, like the terminal instances it
// feeds (TerminalView keeps the same module-level map for the same reason):
// the one-in-flight invariant must hold across every hook instance, not per
// instance. An error-boundary remount of the shell creates a fresh hook while
// an old drain still parks on its in-flight RPC; a per-instance queue would
// let the next keystroke start a SECOND concurrent SendInput for the same
// terminal, which is the transposed-write race this queue exists to prevent.
// The mechanism and its unit tests live in ~/lib/inputQueue.
const terminalInputQueues = createSharedInputQueues<string, TerminalInputContext>()

/** What the input drain needs to route one SendInput. */
interface TerminalInputContext {
  workerId?: string
}

export interface UseTerminalOperationsProps {
  view: TabView
  metadata: TabMetadataStore
  selection: TabSelectionStore
  layoutStore: ReturnType<typeof createLayoutStore>
  activeWorkspace: Accessor<Workspace | null>
  isActiveWorkspaceMutatable: Accessor<boolean>
  getCurrentTabContext: () => Pick<TabContext, 'workerId' | 'workingDir'>
  newTerminalDialog: ToggleDialogState
  setNewTerminalLoading: (v: boolean) => void
  setNewShellLoading: (v: boolean) => void
}

export function useTerminalOperations(props: UseTerminalOperationsProps) {
  // Populates the tab-bar's "new terminal" dropdown. The hook re-fetches
  // on workerId change and skips while the source returns null (worker
  // still resolving).
  const { shells: availableShells, defaultShell } = useAvailableShells(() => {
    const ctx = props.getCurrentTabContext()
    if (!ctx.workerId)
      return null
    return { workerId: ctx.workerId }
  })
  // Dedup concurrent restartTerminal RPCs. Held Enter (autorepeat) would
  // otherwise fire one RPC per keystroke, and the backend rejects every
  // redundant call with FailedPrecondition while the first restart is
  // starting up — yielding a toast burst. The has-check below early-
  // returns on overlapping presses so they never reach the shared
  // promise's eventual rejection (which would multi-toast).
  const restartInflight = createInflightCache<string, void>()

  // One input drain per hook instance, built once. It captures ONLY `view`
  // (not the whole props graph), so the module-level queue below retains a
  // single store reference for as long as one of its bursts is open. The
  // queue takes the drain from the newest enqueue, so a remounted hook takes
  // its own terminals back on the next keystroke.
  const view = props.view
  const terminalInputDrain: InputQueueDrain<string, TerminalInputContext> = {
    // Re-resolve the tab before each batch. Only a tab that is gone, or a
    // terminal that EXITED, stops the drain: a transient status such as
    // DISCONNECTED (a client-derived status that a false-positive sweep
    // can set while the data path still delivers) must not silently
    // discard what the user already typed — the send attempt itself
    // decides, and a failed attempt keeps draining for a reconnect.
    resolve: (id) => {
      const t = view.getTerminalTab(id)
      if (!t || t.status === TerminalStatus.EXITED)
        return undefined
      return { workerId: t.workerId }
    },
    send: async (id, t, batch) => {
      await workerRpc.sendInput(t.workerId ?? '', { terminalId: id, data: batch })
    },
    // The deadline the queue races each send against is the canonical RPC
    // deadline (1.5 × the worker's context deadline, ~/api/transport), for
    // two reasons. It satisfies the sizing rule every channel RPC follows —
    // the client must out-wait the worker's own deadline, or the drain's
    // next batch would run while the previous send can still deliver, which
    // re-opens the transposed-write race the queue exists to close. And it
    // covers legs a per-RPC timeout cannot reach: the RPC timer arms only
    // after the channel is open, so a reconnecting worker would otherwise
    // park the queue on the channel-open chain for far longer.
    sendDeadlineMs: apiLoadingTimeoutMs,
  }

  // Shared open path for both the default-shell quick-action and the
  // shell-picker dropdown. The only call-site differences captured by
  // the args are which loading setter fires, which `shell` is sent, and
  // whether `shellStartDir` is part of the tab seed (the default-shell
  // path remembers the directory so a later restart lands back there;
  // the per-shell path leaves it unset and falls back to workingDir).
  // Title is left out of the request: the worker picks "Terminal <Name>"
  // server-side and returns it in the response (one pool, one place —
  // see worker/service/tab_names.go). Optimistic git seed comes from
  // the active tab so the sidebar doesn't flash the new tab under the
  // workspace before the worker's TerminalStatusChange phase-1 broadcast
  // lands.
  const openTerminalCore = async (
    args: { shell: string, shellStartDir?: string, setLoading: (v: boolean) => void },
  ) => {
    if (!props.isActiveWorkspaceMutatable())
      return
    const ws = props.activeWorkspace()
    if (!ws)
      return
    const ctx = props.getCurrentTabContext()
    if (!ctx.workerId || !ctx.workingDir) {
      props.newTerminalDialog.open()
      return
    }
    args.setLoading(true)
    try {
      const resp = await workerRpc.openTerminal(ctx.workerId, {
        cols: DEFAULT_TERMINAL_COLS,
        rows: DEFAULT_TERMINAL_ROWS,
        workingDir: ctx.workingDir,
        shell: args.shell,
        workerId: ctx.workerId,
        shellStartDir: args.shellStartDir ?? '',
      })

      // The metadata is everything the CRDT has no field for. Shared with the
      // dialog open path so the two cannot disagree about the same moment --
      // they did, and only this one was right.
      const meta = openedTerminalMetadata({
        title: resp.title,
        workingDir: ctx.workingDir,
        shellStartDir: args.shellStartDir,
      })
      const activeTab = props.selection.activeTabForWorkspace(ws.id)
      // `shellStartDir` has to reach the seed resolver: `effectiveGitDir` is
      // `shellStartDir || workingDir`, and the resolver only seeds when the
      // new tab's git dir MATCHES the active tab's. Passing `workingDir`
      // alone made both sides resolve to `ctx.workingDir` -- which comes from
      // the active tab -- so the guard could never reject, and "open a
      // terminal here" on a sibling worktree inherited the wrong repo's
      // branch, origin and diff badges.
      const seed = resolveOptimisticGitInfo(activeTab, {
        shellStartDir: args.shellStartDir,
        workingDir: ctx.workingDir,
      })
      openTabInFocusedTile(
        props,
        { type: TabType.TERMINAL, id: resp.terminalId, workerId: ctx.workerId },
        { ...meta, ...seed },
      )
    }
    catch (err) {
      showWarnToast('Failed to open terminal', err)
    }
    finally {
      args.setLoading(false)
    }
  }

  const handleOpenTerminal = (shellStartDir?: string) =>
    openTerminalCore({ shell: '', shellStartDir: shellStartDir ?? '', setLoading: props.setNewTerminalLoading })

  const handleOpenTerminalWithShell = (shell: string) =>
    openTerminalCore({ shell, setLoading: props.setNewShellLoading })

  const handleTerminalInput = async (terminalId: string, data: Uint8Array) => {
    const tab = props.view.getTerminalTab(terminalId)
    if (!tab)
      return

    if (tab.status === TerminalStatus.READY) {
      return terminalInputQueues.enqueue(terminalId, data, terminalInputDrain)
    }

    // On an exited terminal, the only key that does something is Enter,
    // which restarts the shell. Other input is silently swallowed.
    if (tab.status === TerminalStatus.EXITED) {
      if (data.length !== 1 || data[0] !== ENTER_KEY_CR)
        return
      if (restartInflight.has(terminalId))
        return
      try {
        await restartInflight.run(terminalId, async () => {
          await workerRpc.restartTerminal(tab.workerId ?? '', {
            terminalId,
            cols: tab.cols ?? DEFAULT_TERMINAL_COLS,
            rows: tab.rows ?? DEFAULT_TERMINAL_ROWS,
          })
        })
      }
      catch (err) {
        showWarnToast('Failed to restart terminal', err)
      }
    }
  }

  const handleTerminalResize = async (terminalId: string, cols: number, rows: number) => {
    try {
      const tab = props.view.getTerminalTab(terminalId)
      if (!tab)
        return
      // Mirror the live xterm dims into the tab so a later
      // RestartTerminal sends the user's actual window size, not the
      // dims persisted at last exit. Updated on every fit() (including
      // for EXITED tabs) so the post-exit window shrink/grow is captured.
      if (tab.cols !== cols || tab.rows !== rows)
        props.metadata.patch(terminalId, { cols, rows })
      // Skip the RPC once the PTY can't be the target of a SIGWINCH.
      // xterm's fitAddon.fit() in TerminalView still runs (frontend-only
      // reflow of the existing buffer for users reading dead output);
      // only the worker-side resize is gated. We do NOT gate on
      // status === READY: the ResizeObserver's first fit() fires before
      // the backend broadcasts READY, and the backend stashes that
      // resize so the PTY spawns at the final size.
      if (tab.status === TerminalStatus.EXITED
        || tab.status === TerminalStatus.DISCONNECTED
        || tab.status === TerminalStatus.STARTUP_FAILED) {
        return
      }
      await workerRpc.resizeTerminal(tab.workerId ?? '', { terminalId, cols, rows })
    }
    catch {
      // ignore resize errors
    }
  }

  // Close a terminal.
  //
  // Symmetric to handleAgentClose: store mutations run synchronously;
  // the worker close RPC and Hub unregister are fire-and-forget with
  // failure surfaced via toast.
  const handleTerminalClose = (terminalId: string, worktreeAction: WorktreeAction = WorktreeAction.KEEP): Promise<CloseTabResult | undefined> => {
    const workerId = props.view.getTerminalTab(terminalId)?.workerId ?? ''
    // Release the xterm instance (WebGL context, listeners) BEFORE the tab
    // disappears. TerminalView's per-view ownership tracking only releases
    // ids on unmount — explicit close must dispose here so we don't leak
    // instances when the user closes a terminal whose tile is still
    // on-screen.
    //
    // Order matters, and so does `captureScreen: false`. `emitRemoveTab`
    // applies to `speculativeState` synchronously and bumps `pendingVersion`,
    // so Solid flushes `liveTabIdSet` and the `dropTabs` sweep before the
    // next statement runs. Disposing after it would fire the screen sink for
    // a tab the sweep had just reclaimed. The sink writes through
    // `patchExisting`, so that write is now a no-op rather than a resurrected
    // row — but the order still stands, because a no-op capture is a full
    // buffer serialization thrown away. Capturing at all is pointless here:
    // the tab is destroyed, so the bytes have no future reader.
    disposeTerminalInstance(terminalId, { captureScreen: false })
    emitRemoveTab(TabType.TERMINAL, terminalId)

    // `emitRemoveTab` above emitted the TombstoneTab op via the CRDT
    // bridge; the hub broadcasts it to peer clients via /ws/userevents.
    if (!workerId) {
      // Local tab is gone, but with no worker the close RPC can't fire — a
      // REMOVE therefore can't reach the worktree. Surface it instead of
      // letting the caller assume removal happened.
      warnWorktreeUnreachable(worktreeAction)
      return Promise.resolve(undefined)
    }

    // Background: PTY close, DB close, optional worktree removal. The
    // resolved result lets the delete-branch flow report the actual
    // worktree outcome.
    return awaitCloseResult(
      workerRpc.closeTerminal(workerId, {
        terminalId,
        worktreeAction,
      }),
      'Failed to close terminal',
    )
  }

  return {
    // Signals
    availableShells,
    defaultShell,

    // Handlers
    handleOpenTerminal,
    handleOpenTerminalWithShell,
    handleTerminalInput,
    handleTerminalResize,
    handleTerminalClose,
  }
}

export type TerminalOperations = ReturnType<typeof useTerminalOperations>
