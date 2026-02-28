import type { CallOptions } from '@connectrpc/connect'
import type { Accessor } from 'solid-js'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore } from '~/stores/tab.store'
import type { createTerminalStore } from '~/stores/terminal.store'

import { createEffect, createSignal } from 'solid-js'
import { gitClient, terminalClient } from '~/api/clients'
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
  apiCallTimeout: () => CallOptions
}

export function useTerminalOperations(props: UseTerminalOperationsProps) {
  const [availableShells, setAvailableShells] = createSignal<string[]>([])
  const [defaultShell, setDefaultShell] = createSignal('')

  // Load available shells when active tab's worker changes
  createEffect(() => {
    const ws = props.activeWorkspace()
    if (!ws)
      return
    const ctx = props.getCurrentTabContext()
    if (!ctx.workerId) {
      setAvailableShells([])
      setDefaultShell('')
      return
    }
    terminalClient.listAvailableShells({ orgId: props.org.orgId(), workspaceId: ws.id, workerId: ctx.workerId })
      .then((resp) => {
        setAvailableShells(resp.shells)
        setDefaultShell(resp.defaultShell)
      })
      .catch(() => {
        setAvailableShells([])
        setDefaultShell('')
      })
  })

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
      const resp = await terminalClient.openTerminal({
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
      props.tabStore.addTab({ type: TabType.TERMINAL, id: resp.terminalId, title, tileId, workerId: ctx.workerId, workingDir: ctx.workingDir })
      props.tabStore.setActiveTabForTile(tileId, TabType.TERMINAL, resp.terminalId)
      props.persistLayout?.()
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
      const resp = await terminalClient.openTerminal({
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
      props.tabStore.addTab({ type: TabType.TERMINAL, id: resp.terminalId, title, tileId, workerId: ctx.workerId, workingDir: ctx.workingDir })
      props.tabStore.setActiveTabForTile(tileId, TabType.TERMINAL, resp.terminalId)
      props.persistLayout?.()
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
      await terminalClient.sendInput({ orgId: props.org.orgId(), workspaceId: ws.id, terminalId, data })
    }
    catch {
      // ignore input errors
    }
  }

  const handleTerminalTitleChange = (terminalId: string, title: string) => {
    props.terminalStore.updateTerminalTitle(terminalId, title)
    props.tabStore.updateTabTitle(TabType.TERMINAL, terminalId, title)
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
      await terminalClient.resizeTerminal({ orgId: props.org.orgId(), workspaceId: ws.id, terminalId, cols, rows })
    }
    catch {
      // ignore resize errors
    }
  }

  const handleTerminalClose = async (terminalId: string) => {
    try {
      const ws = props.activeWorkspace()
      if (!ws)
        return
      const resp = await terminalClient.closeTerminal({ orgId: props.org.orgId(), workspaceId: ws.id, terminalId })
      // Auto-handle worktree cleanup if the pre-close check stored a choice.
      if (resp.worktreeCleanupPending && resp.worktreeId) {
        if (props.pendingWorktreeChoice() === 'remove') {
          gitClient.forceRemoveWorktree({ worktreeId: resp.worktreeId }, props.apiCallTimeout()).catch(() => {})
        }
        else {
          gitClient.keepWorktree({ worktreeId: resp.worktreeId }).catch(() => {})
        }
      }
    }
    catch {
      // Ignore errors (e.g. terminal already exited or not tracked by worker)
    }
    finally {
      props.terminalStore.removeTerminal(terminalId)
      props.tabStore.removeTab(TabType.TERMINAL, terminalId)
    }
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
