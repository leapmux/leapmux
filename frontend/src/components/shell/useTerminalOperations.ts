import type { Accessor } from 'solid-js'
import type { TabContext } from './tabContext'
import type { CloseTabResult } from '~/generated/leapmux/v1/common_pb'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import type { ToggleDialogState } from '~/hooks/createDialogState'
import type { createLayoutStore } from '~/stores/layout.store'
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabSelectionStore } from '~/stores/tabSelection.store'

import type { TabView } from '~/stores/tabView'
import * as workerRpc from '~/api/workerRpc'
import { showWarnToast } from '~/components/common/Toast'
import { awaitCloseResult, warnWorktreeUnreachable } from '~/components/shell/closeResultToast'
import { disposeTerminalInstance } from '~/components/terminal/TerminalView'
import { WorktreeAction } from '~/generated/leapmux/v1/common_pb'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { useAvailableShells } from '~/hooks/useAvailableShells'
import { createInflightCache } from '~/lib/inflightCache'
import { monotonicNow } from '~/lib/monotonicNow'
import { DEFAULT_TERMINAL_COLS, DEFAULT_TERMINAL_ROWS } from '~/lib/terminal'
import { openedTerminalMetadata, resolveOptimisticGitInfo, tabKey } from '~/stores/tab.helpers'
import { emitRemoveTab } from '~/stores/tabOps'
import { openTabInFocusedTile } from './openTabInFocusedTile'

// xterm emits Enter as a single CR byte (0x0D) on a non-modifier press.
// We gate the EXITED-tab restart flow on exactly that one byte so a stray
// keystroke (or the autorepeat from a held key) doesn't fire a restart.
const ENTER_KEY_CR = 0x0D

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
      try {
        await workerRpc.sendInput(tab.workerId ?? '', { terminalId, data })
      }
      catch {
        // ignore input errors
      }
      return
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

  // Throttle backend title updates: at most once per 500 ms per terminal.
  // Kept short so a title set right before a shell exit (Ctrl+D) reaches
  // the worker before the close handler persists meta to DB; otherwise
  // the post-restart restore would show the stale pre-update title.
  const TITLE_THROTTLE_MS = 500
  const titleTimers = new Map<string, ReturnType<typeof setTimeout>>()
  const titleLastSent = new Map<string, number>()

  const sendTitleToBackend = (terminalId: string, title: string) => {
    const workerId = props.view.getTerminalTab(terminalId)?.workerId ?? ''
    workerRpc.updateTerminalTitle(workerId, {
      terminalId,
      title,
    }).catch(() => {})
    titleLastSent.set(terminalId, monotonicNow())
  }

  const handleTerminalTitleChange = (terminalId: string, title: string) => {
    props.metadata.patch(terminalId, { title })

    // Debounced backend sync
    const existing = titleTimers.get(terminalId)
    if (existing)
      clearTimeout(existing)

    // Missing entry = never sent: treat as fully elapsed so the first
    // update fires immediately (performance.now() starts near 0, so a
    // `?? 0` sentinel would incorrectly delay early-session titles).
    const last = titleLastSent.get(terminalId)
    const elapsed = last === undefined ? TITLE_THROTTLE_MS : monotonicNow() - last
    const delay = Math.max(0, TITLE_THROTTLE_MS - elapsed)
    if (delay === 0) {
      sendTitleToBackend(terminalId, title)
    }
    else {
      titleTimers.set(terminalId, setTimeout(() => {
        titleTimers.delete(terminalId)
        sendTitleToBackend(terminalId, title)
      }, delay))
    }
  }

  const handleTerminalBell = (terminalId: string) => {
    // Only notify if this terminal's tab is not active
    const activeKey = props.selection.activeKeyForWorkspace(props.activeWorkspace()?.id ?? '')
    const bellKey = tabKey({ type: TabType.TERMINAL, id: terminalId })
    if (activeKey !== bellKey) {
      props.metadata.patch(terminalId, { hasNotification: true })
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
    // so Solid flushes `liveTabIdSet` and the `retainOnly` sweep before the
    // next statement runs. Disposing after it would fire the screen sink for
    // a tab the sweep had just reclaimed, re-creating its metadata row —
    // carrying a full serialized scrollback — with nothing left to evict it
    // until some other tab happened to be created or closed. Capturing at all
    // is pointless here: the tab is being destroyed, so the bytes have no
    // future reader.
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
    handleTerminalTitleChange,
    handleTerminalBell,
    handleTerminalResize,
    handleTerminalClose,
  }
}

export type TerminalOperations = ReturnType<typeof useTerminalOperations>
