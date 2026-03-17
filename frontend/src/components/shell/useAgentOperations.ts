import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import type { createAgentStore } from '~/stores/agent.store'
import type { createChatStore } from '~/stores/chat.store'
import type { createControlStore } from '~/stores/control.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore, Tab } from '~/stores/tab.store'
import type { PermissionMode } from '~/utils/controlResponse'

import { workspaceClient } from '~/api/clients'
import * as workerRpc from '~/api/workerRpc'
import { showWarnToast } from '~/components/common/Toast'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { WorktreeAction } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { getInnerMessage, parseMessageContent } from '~/lib/messageParser'
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
  setShowNewAgentDialog: (show: boolean) => void
  setNewAgentLoading: (loading: boolean) => void
  setShowResumeDialog: (show: boolean) => void
  persistLayout?: () => void
  focusEditor?: () => void
  forceScrollToBottom?: () => void
}

export function useAgentOperations(props: UseAgentOperationsProps) {
  /** Look up the workerId for a given agent from the agent store. */
  const getAgentWorkerId = (agentId: string): string => {
    return props.agentStore.state.agents.find(a => a.id === agentId)?.workerId ?? ''
  }

  // Open a new agent in the given workspace
  const openAgentInWorkspace = async (workspaceId: string, workerId: string, workingDir: string, sessionId?: string) => {
    try {
      const title = `Agent ${nextTabNumber(props.tabStore.state.tabs, TabType.AGENT, 'Agent')}`
      const resp = await workerRpc.openAgent(workerId, {
        workspaceId,
        agentProvider: AgentProvider.CLAUDE_CODE,
        model: DEFAULT_MODEL,
        title,
        systemPrompt: '',
        workerId,
        workingDir,
        ...(sessionId ? { agentSessionId: sessionId } : {}),
      })
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
        // Register tab with hub.
        workspaceClient.addTab({
          workspaceId,
          tab: { tabType: TabType.AGENT, tabId: resp.agent.id, tileId, workerId },
        }).catch(() => {})
        // Focus the editor after the reactive updates propagate to the DOM.
        requestAnimationFrame(() => props.focusEditor?.())
      }
    }
    catch (err) {
      showWarnToast('Failed to open agent', err)
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
      showWarnToast('Failed to resume session', err)
    }
    finally {
      props.setShowResumeDialog(false)
    }
  }

  // Handle control responses (permission grant/deny) for agent prompts
  const handleControlResponse = async (agentId: string, content: Uint8Array) => {
    props.forceScrollToBottom?.()
    try {
      const workerId = getAgentWorkerId(agentId)
      await workerRpc.sendControlResponse(workerId, { agentId, content })
      // Remove from pending after successful send.
      const parsed = JSON.parse(new TextDecoder().decode(content))
      const requestId = parsed?.response?.request_id
      if (requestId) {
        props.controlStore.removeRequest(agentId, requestId)
      }
    }
    catch (err) {
      showWarnToast('Failed to send response', err)
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
    if (agent.supportsModelEffort === false)
      return
    const previous = agent[field] || (field === 'model' ? DEFAULT_MODEL : DEFAULT_EFFORT)
    // Optimistic update
    props.agentStore.updateAgent(agentId, { [field]: value })
    props.settingsLoading.start()
    try {
      await workerRpc.updateAgentSettings(agent.workerId, {
        agentId,
        model: field === 'model' ? value : '',
        effort: field === 'effort' ? value : '',
      })
      props.settingsLoading.stop()
    }
    catch (err) {
      props.agentStore.updateAgent(agentId, { [field]: previous })
      props.settingsLoading.stop()
      showWarnToast(`Failed to change ${field}`, err)
    }
  }

  // Interrupt the active agent's current turn
  const handleInterrupt = async () => {
    const agentId = props.agentStore.state.activeAgentId
    if (!agentId)
      return
    try {
      const workerId = getAgentWorkerId(agentId)
      await workerRpc.sendAgentMessage(workerId, {
        agentId,
        content: buildInterruptRequest(),
      })
    }
    catch (err) {
      showWarnToast('Failed to interrupt', err)
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
      await workerRpc.sendAgentMessage(agent.workerId, {
        agentId,
        content: buildSetPermissionModeRequest(mode),
      })
      props.settingsLoading.stop()
    }
    catch (err) {
      // Revert on failure
      props.agentStore.updateAgent(agentId, { permissionMode: previousMode })
      props.settingsLoading.stop()
      showWarnToast('Failed to change permission mode', err)
    }
  }

  // Retry a failed message delivery.
  // Always re-sends via sendAgentMessage (which auto-starts the agent
  // if needed), then removes the old failed message.
  const handleRetryMessage = async (agentId: string, messageId: string) => {
    try {
      const workerId = getAgentWorkerId(agentId)
      const message = props.chatStore.getMessages(agentId).find(m => m.id === messageId)
      if (!message)
        return
      const parsed = parseMessageContent(message)
      const inner = getInnerMessage(parsed)
      const content = inner?.content
      if (typeof content !== 'string')
        return

      await workerRpc.sendAgentMessage(workerId, { agentId, content })
      // Success: delete the old failed message. The new one arrives via WatchEvents.
      if (messageId.startsWith('local-')) {
        props.chatStore.removeMessage(agentId, messageId)
      }
      else {
        await workerRpc.deleteAgentMessage(workerId, { agentId, messageId })
        props.chatStore.removeMessage(agentId, messageId)
      }
    }
    catch (err) {
      showWarnToast('Retry failed', err)
    }
  }

  // Delete a failed message
  const handleDeleteMessage = async (agentId: string, messageId: string) => {
    if (messageId.startsWith('local-')) {
      // Local optimistic message: just remove from the local store.
      props.chatStore.removeMessage(agentId, messageId)
      return
    }
    try {
      const workerId = getAgentWorkerId(agentId)
      await workerRpc.deleteAgentMessage(workerId, { agentId, messageId })
      props.chatStore.removeMessage(agentId, messageId)
    }
    catch (err) {
      showWarnToast('Failed to delete message', err)
    }
  }

  // Close an agent
  const handleCloseAgent = async (agentId: string, worktreeAction: WorktreeAction = WorktreeAction.UNSPECIFIED) => {
    try {
      const workerId = getAgentWorkerId(agentId)
      props.controlStore.clearAgent(agentId)
      if (workerId) {
        await workerRpc.closeAgent(workerId, { agentId, worktreeAction })
      }
      props.agentStore.removeAgent(agentId)
      props.tabStore.removeTab(TabType.AGENT, agentId)
      // Unregister tab from hub.
      const ws = props.activeWorkspace()
      if (ws) {
        workspaceClient.removeTab({ workspaceId: ws.id, tabType: TabType.AGENT, tabId: agentId }).catch(() => {})
      }
    }
    catch (err) {
      showWarnToast('Failed to close agent', err)
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
