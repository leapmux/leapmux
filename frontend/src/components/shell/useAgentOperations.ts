import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import type { createAgentStore } from '~/stores/agent.store'
import type { createChatStore } from '~/stores/chat.store'
import type { createControlStore } from '~/stores/control.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore, Tab } from '~/stores/tab.store'
import type { PermissionMode } from '~/utils/controlResponse'

import { agentClient, gitClient } from '~/api/clients'
import { agentCallTimeout, apiCallTimeout } from '~/api/transport'
import { showToast } from '~/components/common/Toast'
import { AgentStatus } from '~/generated/leapmux/v1/agent_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { buildInterruptRequest, buildSetPermissionModeRequest, DEFAULT_EFFORT, DEFAULT_MODEL } from '~/utils/controlResponse'

/** Find the smallest unused number for auto-naming tabs (gap-filling). */
export function nextTabNumber(tabs: Tab[], type: TabType, prefix: string): number {
  const used = new Set<number>()
  for (const tab of tabs) {
    if (tab.type === type && tab.title) {
      const match = tab.title.match(new RegExp(`^${prefix} (\\d+)$`))
      if (match)
        used.add(Number(match[1]))
    }
  }
  let n = 1
  while (used.has(n))
    n++
  return n
}

export interface UseAgentOperationsProps {
  agentStore: ReturnType<typeof createAgentStore>
  chatStore: ReturnType<typeof createChatStore>
  controlStore: ReturnType<typeof createControlStore>
  tabStore: ReturnType<typeof createTabStore>
  layoutStore: ReturnType<typeof createLayoutStore>
  settingsLoading: { start: () => void, stop: () => void }
  isActiveWorkspaceMutatable: () => boolean
  activeWorkspace: () => Workspace | null
  getCurrentTabContext: () => { workerId: string, workingDir: string }
  pendingWorktreeChoice: () => 'keep' | 'remove' | null
  setShowNewAgentDialog: (show: boolean) => void
  setNewAgentLoading: (loading: boolean) => void
  setShowResumeDialog: (show: boolean) => void
  persistLayout?: () => void
  focusEditor?: () => void
  forceScrollToBottom?: () => void
}

export function useAgentOperations(props: UseAgentOperationsProps) {
  // Open a new agent in the given workspace
  const openAgentInWorkspace = async (workspaceId: string, workerId: string, workingDir: string, sessionId?: string) => {
    try {
      const title = `Agent ${nextTabNumber(props.tabStore.state.tabs, TabType.AGENT, 'Agent')}`
      const resp = await agentClient.openAgent({
        workspaceId,
        model: DEFAULT_MODEL,
        title,
        systemPrompt: '',
        workerId,
        workingDir,
        ...(sessionId ? { agentSessionId: sessionId } : {}),
      }, agentCallTimeout(false))
      if (resp.agent) {
        const tileId = props.layoutStore.focusedTileId()
        props.agentStore.addAgent(resp.agent)
        props.tabStore.addTab({
          type: TabType.AGENT,
          id: resp.agent.id,
          title,
          tileId,
          workerId: resp.agent.workerId,
          workingDir: resp.agent.workingDir,
        })
        props.tabStore.setActiveTabForTile(tileId, TabType.AGENT, resp.agent.id)
        props.persistLayout?.()
        // Focus the editor after the reactive updates propagate to the DOM.
        requestAnimationFrame(() => props.focusEditor?.())
      }
    }
    catch (err) {
      showToast(err instanceof Error ? err.message : 'Failed to open agent', 'danger')
    }
  }

  // Open a new agent in the active workspace (for click handlers)
  const handleOpenAgent = async () => {
    if (!props.isActiveWorkspaceMutatable())
      return
    const ws = props.activeWorkspace()
    if (!ws)
      return
    const ctx = props.getCurrentTabContext()
    if (!ctx.workerId) {
      props.setShowNewAgentDialog(true)
      return
    }
    props.setNewAgentLoading(true)
    try {
      await openAgentInWorkspace(ws.id, ctx.workerId, ctx.workingDir)
    }
    finally {
      props.setNewAgentLoading(false)
    }
  }

  // Resume an agent from an existing session ID
  const handleResumeAgent = async (sessionId: string, workerId: string) => {
    if (!props.isActiveWorkspaceMutatable())
      return
    const ws = props.activeWorkspace()
    if (!ws)
      return
    try {
      const ctx = props.getCurrentTabContext()
      await openAgentInWorkspace(ws.id, workerId, ctx.workingDir || '~', sessionId)
    }
    catch (err) {
      showToast(err instanceof Error ? err.message : 'Failed to resume session', 'danger')
    }
    finally {
      props.setShowResumeDialog(false)
    }
  }

  // Handle control responses (permission grant/deny) for agent prompts
  const handleControlResponse = async (agentId: string, content: Uint8Array) => {
    props.forceScrollToBottom?.()
    try {
      const agent = props.agentStore.state.agents.find(a => a.id === agentId)
      const isActive = agent?.status === AgentStatus.ACTIVE
      await agentClient.sendControlResponse({ agentId, content }, agentCallTimeout(isActive))
      // Remove from pending after successful send.
      const parsed = JSON.parse(new TextDecoder().decode(content))
      const requestId = parsed?.response?.request_id
      if (requestId) {
        props.controlStore.removeRequest(agentId, requestId)
      }
    }
    catch (err) {
      showToast(err instanceof Error ? err.message : 'Failed to send response', 'danger')
    }
  }

  // Change model or effort for the active agent (requires agent restart)
  const handleModelOrEffortChange = async (field: 'model' | 'effort', value: string) => {
    const agentId = props.agentStore.state.activeAgentId
    if (!agentId)
      return
    const agent = props.agentStore.state.agents.find(a => a.id === agentId)
    if (!agent)
      return
    const previous = agent[field] || (field === 'model' ? DEFAULT_MODEL : DEFAULT_EFFORT)
    // Optimistic update
    props.agentStore.updateAgent(agentId, { [field]: value })
    props.settingsLoading.start()
    try {
      await agentClient.updateAgentSettings({
        agentId,
        model: field === 'model' ? value : '',
        effort: field === 'effort' ? value : '',
      }, agentCallTimeout(agent.status === AgentStatus.ACTIVE))
      props.settingsLoading.stop()
    }
    catch (err) {
      props.agentStore.updateAgent(agentId, { [field]: previous })
      props.settingsLoading.stop()
      showToast(err instanceof Error ? err.message : `Failed to change ${field}`, 'danger')
    }
  }

  // Interrupt the active agent's current turn
  const handleInterrupt = async () => {
    const agentId = props.agentStore.state.activeAgentId
    if (!agentId)
      return
    try {
      await agentClient.sendAgentMessage({
        agentId,
        content: buildInterruptRequest(),
      }, agentCallTimeout(true))
    }
    catch (err) {
      showToast(err instanceof Error ? err.message : 'Failed to interrupt', 'danger')
    }
  }

  // Change permission mode for the active agent
  const handlePermissionModeChange = async (mode: PermissionMode) => {
    const agentId = props.agentStore.state.activeAgentId
    if (!agentId)
      return
    const agent = props.agentStore.state.agents.find(a => a.id === agentId)
    if (!agent)
      return
    const previousMode = (agent.permissionMode || 'default') as PermissionMode
    // Optimistic update
    props.agentStore.updateAgent(agentId, { permissionMode: mode })
    props.settingsLoading.start()
    try {
      await agentClient.sendAgentMessage({
        agentId,
        content: buildSetPermissionModeRequest(mode),
      }, agentCallTimeout(agent.status === AgentStatus.ACTIVE))
      props.settingsLoading.stop()
    }
    catch (err) {
      // Revert on failure
      props.agentStore.updateAgent(agentId, { permissionMode: previousMode })
      props.settingsLoading.stop()
      showToast(err instanceof Error ? err.message : 'Failed to change permission mode', 'danger')
    }
  }

  // Retry a failed message delivery
  const handleRetryMessage = async (agentId: string, messageId: string) => {
    try {
      const retryAgent = props.agentStore.state.agents.find(a => a.id === agentId)
      await agentClient.retryAgentMessage({ agentId, messageId }, agentCallTimeout(retryAgent?.status === AgentStatus.ACTIVE))
      props.chatStore.clearMessageError(messageId)
    }
    catch (err) {
      showToast(err instanceof Error ? err.message : 'Retry failed', 'danger')
    }
  }

  // Delete a failed message
  const handleDeleteMessage = async (agentId: string, messageId: string) => {
    try {
      await agentClient.deleteAgentMessage({ agentId, messageId })
      props.chatStore.removeMessage(agentId, messageId)
    }
    catch (err) {
      showToast(err instanceof Error ? err.message : 'Failed to delete message', 'danger')
    }
  }

  // Close an agent
  const handleCloseAgent = async (agentId: string) => {
    try {
      props.controlStore.clearAgent(agentId)
      const resp = await agentClient.closeAgent({ agentId })
      props.agentStore.removeAgent(agentId)
      props.tabStore.removeTab(TabType.AGENT, agentId)
      // Auto-handle worktree cleanup if the pre-close check stored a choice.
      if (resp.worktreeCleanupPending && resp.worktreeId) {
        if (props.pendingWorktreeChoice() === 'remove') {
          gitClient.forceRemoveWorktree({ worktreeId: resp.worktreeId }, apiCallTimeout()).catch(() => {})
        }
        else {
          // Default to keep (if somehow no choice was stored)
          gitClient.keepWorktree({ worktreeId: resp.worktreeId }).catch(() => {})
        }
      }
    }
    catch (err) {
      showToast(err instanceof Error ? err.message : 'Failed to close agent', 'danger')
    }
  }

  return {
    openAgentInWorkspace,
    handleOpenAgent,
    handleResumeAgent,
    handleControlResponse,
    handleModelOrEffortChange,
    handleInterrupt,
    handlePermissionModeChange,
    handleRetryMessage,
    handleDeleteMessage,
    handleCloseAgent,
  }
}

export type AgentOperations = ReturnType<typeof useAgentOperations>
