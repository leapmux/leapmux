import type { Accessor } from 'solid-js'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore } from '~/stores/tab.store'
import type { createTerminalStore } from '~/stores/terminal.store'

import { createEffect, createSignal, on } from 'solid-js'
import { workspaceClient } from '~/api/clients'
import * as workerRpc from '~/api/workerRpc'
import { showToast } from '~/components/common/Toast'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'

import { nextTabNumber } from './useAgentOperations'

export interface UseTerminalOperationsProps {
  org: { orgId: () => string }
  tabStore: ReturnType<typeof createTabStore>
  terminalStore: ReturnType<typeof createTerminalStore>
  layoutStore: ReturnType<typeof createLayoutStore>
  activeWorkspace: Accessor<Workspace | null>
  isActiveWorkspaceMutatable: Accessor<boolean>
  getCurrentTabContext: () => { workerId: string, workingDir: string }
  setShowNewTerminalDialog: (v: boolean) => void
  setNewTerminalLoading: (v: boolean) => void
  setNewShellLoading: (v: boolean) => void
  pendingWorktreeChoice: () => 'keep' | 'remove' | null
  persistLayout?: () => void
}

export function useTerminalOperations(props: UseTerminalOperationsProps) {
  const [availableShells, setAvailableShells] = createSignal<string[]>([])
  const [defaultShell, setDefaultShell] = createSignal('')

  /** Get workerId for a terminal from the terminal store. */
  const getTerminalWorkerId = (terminalId: string): string => {
    return props.terminalStore.state.terminals.find(t => t.id === terminalId)?.workerId ?? ''
  }

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
    if (!ctx.workerId) {
      props.setShowNewTerminalDialog(true)
      return
    }
    props.setNewTerminalLoading(true)
    try {
      const title = `Terminal ${nextTabNumber(props.tabStore.state.tabs, TabType.TERMINAL, 'Terminal')}`
      const resp = await workerRpc.openTerminal(ctx.workerId, {
        orgId: props.org.orgId(),
        workspaceId: ws.id,
        cols: 80,
        rows: 24,
        workingDir: ctx.workingDir,
        shell: '',
        workerId: ctx.workerId,
        shellStartDir: shellStartDir ?? '',
      })

      const tileId = props.layoutStore.focusedTileId()
      props.terminalStore.addTerminal({ id: resp.terminalId, workspaceId: ws.id, workerId: ctx.workerId, workingDir: ctx.workingDir, shellStartDir: shellStartDir ?? ctx.workingDir })
      props.tabStore.addTab({ type: TabType.TERMINAL, id: resp.terminalId, title, tileId, workerId: ctx.workerId, workingDir: ctx.workingDir, gitBranch: resp.gitBranch || undefined, gitOriginUrl: resp.gitOriginUrl || undefined })
      props.tabStore.setActiveTabForTile(tileId, TabType.TERMINAL, resp.terminalId)
      props.persistLayout?.()
      // Register tab with hub.
      workspaceClient.addTab({
        workspaceId: ws.id,
        tab: { tabType: TabType.TERMINAL, tabId: resp.terminalId, tileId, workerId: ctx.workerId },
      }).catch(() => {})
      // Persist initial title to backend so it survives restarts.
      workerRpc.updateTerminalTitle(ctx.workerId, {
        orgId: props.org.orgId(),
        workspaceId: ws.id,
        terminalId: resp.terminalId,
        title,
      }).catch(() => {})
    }
    catch (err) {
      showToast(err instanceof Error ? err.message : 'Failed to open terminal', 'danger')
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
    if (!ctx.workerId) {
      props.setShowNewTerminalDialog(true)
      return
    }
    props.setNewShellLoading(true)
    try {
      const title = `Terminal ${nextTabNumber(props.tabStore.state.tabs, TabType.TERMINAL, 'Terminal')}`
      const resp = await workerRpc.openTerminal(ctx.workerId, {
        orgId: props.org.orgId(),
        workspaceId: ws.id,
        cols: 80,
        rows: 24,
        workingDir: ctx.workingDir,
        shell,
        workerId: ctx.workerId,
      })

      const tileId = props.layoutStore.focusedTileId()
      props.terminalStore.addTerminal({ id: resp.terminalId, workspaceId: ws.id, workerId: ctx.workerId, workingDir: ctx.workingDir })
      props.tabStore.addTab({ type: TabType.TERMINAL, id: resp.terminalId, title, tileId, workerId: ctx.workerId, workingDir: ctx.workingDir, gitBranch: resp.gitBranch || undefined, gitOriginUrl: resp.gitOriginUrl || undefined })
      props.tabStore.setActiveTabForTile(tileId, TabType.TERMINAL, resp.terminalId)
      props.persistLayout?.()
      // Register tab with hub.
      workspaceClient.addTab({
        workspaceId: ws.id,
        tab: { tabType: TabType.TERMINAL, tabId: resp.terminalId, tileId, workerId: ctx.workerId },
      }).catch(() => {})
      // Persist initial title to backend so it survives restarts.
      workerRpc.updateTerminalTitle(ctx.workerId, {
        orgId: props.org.orgId(),
        workspaceId: ws.id,
        terminalId: resp.terminalId,
        title,
      }).catch(() => {})
    }
    catch (err) {
      showToast(err instanceof Error ? err.message : 'Failed to open terminal', 'danger')
    }
    finally {
      props.setNewShellLoading(false)
    }
  }

  const handleTerminalInput = async (terminalId: string, data: Uint8Array) => {
    try {
      const ws = props.activeWorkspace()
      if (!ws || !props.terminalStore.hasTerminal(terminalId) || props.terminalStore.isExited(terminalId))
        return
      const workerId = getTerminalWorkerId(terminalId)
      await workerRpc.sendInput(workerId, { orgId: props.org.orgId(), workspaceId: ws.id, terminalId, data })
    }
    catch {
      // ignore input errors
    }
  }

  // Debounce backend title updates: at most once per 10 seconds per terminal.
  const titleTimers = new Map<string, ReturnType<typeof setTimeout>>()
  const titleLastSent = new Map<string, number>()

  const sendTitleToBackend = (terminalId: string, title: string) => {
    const ws = props.activeWorkspace()
    if (!ws)
      return
    const workerId = getTerminalWorkerId(terminalId)
    workerRpc.updateTerminalTitle(workerId, {
      orgId: props.org.orgId(),
      workspaceId: ws.id,
      terminalId,
      title,
    }).catch(() => {})
    titleLastSent.set(terminalId, Date.now())
  }

  const handleTerminalTitleChange = (terminalId: string, title: string) => {
    props.terminalStore.updateTerminalTitle(terminalId, title)
    props.tabStore.updateTabTitle(TabType.TERMINAL, terminalId, title)

    // Debounced backend sync
    const existing = titleTimers.get(terminalId)
    if (existing)
      clearTimeout(existing)

    const last = titleLastSent.get(terminalId) ?? 0
    const elapsed = Date.now() - last
    const delay = Math.max(0, 10_000 - elapsed)
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
    const bellKey = `terminal:${terminalId}`
    if (activeKey !== bellKey) {
      props.tabStore.setNotification(TabType.TERMINAL, terminalId, true)
    }
  }

  const handleTerminalResize = async (terminalId: string, cols: number, rows: number) => {
    try {
      const ws = props.activeWorkspace()
      if (!ws || !props.terminalStore.hasTerminal(terminalId) || props.terminalStore.isExited(terminalId))
        return
      const workerId = getTerminalWorkerId(terminalId)
      await workerRpc.resizeTerminal(workerId, { orgId: props.org.orgId(), workspaceId: ws.id, terminalId, cols, rows })
    }
    catch {
      // ignore resize errors
    }
  }

  const handleTerminalClose = async (terminalId: string) => {
    const ws = props.activeWorkspace()
    try {
      if (!ws)
        return
      const workerId = getTerminalWorkerId(terminalId)
      const resp = await workerRpc.closeTerminal(workerId, { orgId: props.org.orgId(), workspaceId: ws.id, terminalId })
      // Auto-handle worktree cleanup if the pre-close check stored a choice.
      if (resp.worktreeCleanupPending && resp.worktreeId) {
        if (props.pendingWorktreeChoice() === 'remove') {
          workerRpc.forceRemoveWorktree(workerId, { worktreeId: resp.worktreeId }).catch(() => {})
        }
        else {
          workerRpc.keepWorktree(workerId, { worktreeId: resp.worktreeId }).catch(() => {})
        }
      }
    }
    catch {
      // Ignore errors (e.g. terminal already exited or not tracked by worker)
    }
    finally {
      props.terminalStore.removeTerminal(terminalId)
      props.tabStore.removeTab(TabType.TERMINAL, terminalId)
      // Unregister tab from hub.
      if (ws) {
        workspaceClient.removeTab({ workspaceId: ws.id, tabType: TabType.TERMINAL, tabId: terminalId }).catch(() => {})
      }
    }
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
