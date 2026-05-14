import type { Accessor } from 'solid-js'
import type { TabContext } from './tabContext'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore } from '~/stores/tab.store'
import type { TerminalTab } from '~/stores/tab.types'

import { createEffect, createSignal, on } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { showWarnToast } from '~/components/common/Toast'
import { toastCloseFailure } from '~/components/shell/closeFailureToast'
import { disposeTerminalInstance } from '~/components/terminal/TerminalView'
import { WorktreeAction } from '~/generated/leapmux/v1/common_pb'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { resolveOptimisticGitInfo, tabKey } from '~/stores/tab.helpers'

export interface UseTerminalOperationsProps {
  org: { orgId: () => string }
  tabStore: ReturnType<typeof createTabStore>
  layoutStore: ReturnType<typeof createLayoutStore>
  activeWorkspace: Accessor<Workspace | null>
  isActiveWorkspaceMutatable: Accessor<boolean>
  getCurrentTabContext: () => Pick<TabContext, 'workerId' | 'workingDir'>
  setShowNewTerminalDialog: (v: boolean) => void
  setNewTerminalLoading: (v: boolean) => void
  setNewShellLoading: (v: boolean) => void
}

export function useTerminalOperations(props: UseTerminalOperationsProps) {
  const [availableShells, setAvailableShells] = createSignal<string[]>([])
  const [defaultShell, setDefaultShell] = createSignal('')

  /** Load available shells on demand (e.g. when the new-terminal dialog opens). */
  const loadAvailableShells = () => {
    const ws = props.activeWorkspace()
    if (!ws)
      return
    const ctx = props.getCurrentTabContext()
    if (!ctx.workerId)
      return
    workerRpc.listAvailableShells(ctx.workerId, { orgId: props.org.orgId(), workspaceId: ws.id, workerId: ctx.workerId })
      .then((resp) => {
        setAvailableShells(resp.shells)
        setDefaultShell(resp.defaultShell)
      })
      .catch(() => {
        setAvailableShells([])
        setDefaultShell('')
      })
  }

  // Load available shells once per workerId change so the tabbar dropdown is populated.
  createEffect(on(
    () => props.getCurrentTabContext().workerId,
    (workerId) => {
      if (workerId)
        loadAvailableShells()
    },
  ))

  const handleOpenTerminal = async (shellStartDir?: string) => {
    if (!props.isActiveWorkspaceMutatable())
      return
    const ws = props.activeWorkspace()
    if (!ws)
      return
    const ctx = props.getCurrentTabContext()
    if (!ctx.workerId || !ctx.workingDir) {
      props.setShowNewTerminalDialog(true)
      return
    }
    props.setNewTerminalLoading(true)
    try {
      // Title left out: the worker picks "Terminal <Name>" server-side
      // and returns it in the response. One pool, one place — see
      // worker/service/tab_names.go.
      const resp = await workerRpc.openTerminal(ctx.workerId, {
        orgId: props.org.orgId(),
        workspaceId: ws.id,
        cols: 80,
        rows: 25,
        workingDir: ctx.workingDir,
        shell: '',
        workerId: ctx.workerId,
        shellStartDir: shellStartDir ?? '',
      })

      const tileId = props.layoutStore.focusedTileId()
      const afterKey = props.tabStore.getActiveTabKeyForTile(tileId)
      // The real git branch / origin arrive later via TerminalStatusChange
      // (phase 1 of the async startup reports the post-mutation gitStatus).
      // Seed optimistically from the active tab so the sidebar doesn't flash
      // the new tab under the workspace before phase 1 completes.
      const newTab: TerminalTab = { type: TabType.TERMINAL, id: resp.terminalId, title: resp.title, tileId, workerId: ctx.workerId, workingDir: ctx.workingDir, shellStartDir: shellStartDir ?? ctx.workingDir, status: TerminalStatus.STARTING }
      const seed = resolveOptimisticGitInfo(props.tabStore.activeTab(), newTab)
      props.tabStore.addTab({ ...newTab, ...seed }, { afterKey })
      props.tabStore.setActiveTabForTile(tileId, TabType.TERMINAL, resp.terminalId)
    }
    catch (err) {
      showWarnToast('Failed to open terminal', err)
    }
    finally {
      props.setNewTerminalLoading(false)
    }
  }

  const handleOpenTerminalWithShell = async (shell: string) => {
    if (!props.isActiveWorkspaceMutatable())
      return
    const ws = props.activeWorkspace()
    if (!ws)
      return
    const ctx = props.getCurrentTabContext()
    if (!ctx.workerId || !ctx.workingDir) {
      props.setShowNewTerminalDialog(true)
      return
    }
    props.setNewShellLoading(true)
    try {
      const resp = await workerRpc.openTerminal(ctx.workerId, {
        orgId: props.org.orgId(),
        workspaceId: ws.id,
        cols: 80,
        rows: 25,
        workingDir: ctx.workingDir,
        shell,
        workerId: ctx.workerId,
      })

      const tileId = props.layoutStore.focusedTileId()
      const afterKey = props.tabStore.getActiveTabKeyForTile(tileId)
      const newTab: TerminalTab = { type: TabType.TERMINAL, id: resp.terminalId, title: resp.title, tileId, workerId: ctx.workerId, workingDir: ctx.workingDir, status: TerminalStatus.STARTING }
      const seed = resolveOptimisticGitInfo(props.tabStore.activeTab(), newTab)
      props.tabStore.addTab({ ...newTab, ...seed }, { afterKey })
      props.tabStore.setActiveTabForTile(tileId, TabType.TERMINAL, resp.terminalId)
    }
    catch (err) {
      showWarnToast('Failed to open terminal', err)
    }
    finally {
      props.setNewShellLoading(false)
    }
  }

  const handleTerminalInput = async (terminalId: string, data: Uint8Array) => {
    try {
      const ws = props.activeWorkspace()
      const tab = props.tabStore.getTerminalTab(terminalId)
      if (!ws || tab?.status !== TerminalStatus.READY)
        return
      await workerRpc.sendInput(tab.workerId ?? '', { orgId: props.org.orgId(), workspaceId: ws.id, terminalId, data })
    }
    catch {
      // ignore input errors
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
    const ws = props.activeWorkspace()
    if (!ws)
      return
    const workerId = props.tabStore.getTerminalTab(terminalId)?.workerId ?? ''
    workerRpc.updateTerminalTitle(workerId, {
      orgId: props.org.orgId(),
      workspaceId: ws.id,
      terminalId,
      title,
    }).catch(() => {})
    titleLastSent.set(terminalId, Date.now())
  }

  const handleTerminalTitleChange = (terminalId: string, title: string) => {
    props.tabStore.updateTabTitle(TabType.TERMINAL, terminalId, title)

    // Debounced backend sync
    const existing = titleTimers.get(terminalId)
    if (existing)
      clearTimeout(existing)

    const last = titleLastSent.get(terminalId) ?? 0
    const elapsed = Date.now() - last
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
    const activeKey = props.tabStore.state.activeTabKey
    const bellKey = tabKey({ type: TabType.TERMINAL, id: terminalId })
    if (activeKey !== bellKey) {
      props.tabStore.setNotification(TabType.TERMINAL, terminalId, true)
    }
  }

  const handleTerminalResize = async (terminalId: string, cols: number, rows: number) => {
    try {
      const ws = props.activeWorkspace()
      const tab = props.tabStore.getTerminalTab(terminalId)
      if (!ws || !tab)
        return
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
      await workerRpc.resizeTerminal(tab.workerId ?? '', { orgId: props.org.orgId(), workspaceId: ws.id, terminalId, cols, rows })
    }
    catch {
      // ignore resize errors
    }
  }

  // Close a terminal.
  //
  // Symmetric to handleCloseAgent: store mutations run synchronously;
  // the worker close RPC and Hub unregister are fire-and-forget with
  // failure surfaced via toast.
  const handleTerminalClose = (terminalId: string, worktreeAction: WorktreeAction = WorktreeAction.KEEP) => {
    const workerId = props.tabStore.getTerminalTab(terminalId)?.workerId ?? ''
    const ws = props.activeWorkspace()

    // Synchronous: tab disappears immediately, then release the xterm
    // instance (WebGL context, listeners). TerminalView's per-view
    // ownership tracking only releases ids on unmount — explicit close
    // must dispose here so we don't leak instances when the user closes
    // a terminal whose tile is still on-screen.
    props.tabStore.removeTab(TabType.TERMINAL, terminalId)
    disposeTerminalInstance(terminalId)

    // Background: PTY close, DB close, optional worktree removal.
    if (workerId && ws) {
      workerRpc.closeTerminal(workerId, {
        orgId: props.org.orgId(),
        workspaceId: ws.id,
        terminalId,
        worktreeAction,
      })
        .then(resp => toastCloseFailure(resp.result))
        .catch((err) => {
          showWarnToast('Failed to close terminal', err)
        })
    }

    // `tabStore.removeTab` above emitted the TombstoneTab op via the
    // CRDT bridge; the hub broadcasts it to peer clients via
    // /ws/orgevents.
    void terminalId
  }

  return {
    // Signals
    availableShells,
    defaultShell,
    loadAvailableShells,

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
