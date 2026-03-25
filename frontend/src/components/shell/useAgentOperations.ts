import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import type { createAgentStore } from '~/stores/agent.store'
import type { createAgentSessionStore } from '~/stores/agentSession.store'
import type { createChatStore } from '~/stores/chat.store'
import type { createControlStore } from '~/stores/control.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore, Tab } from '~/stores/tab.store'
import type { PermissionMode } from '~/utils/controlResponse'

import { createEffect, createSignal, on } from 'solid-js'
import { workspaceClient } from '~/api/clients'
import * as workerRpc from '~/api/workerRpc'
import { getProviderPlugin } from '~/components/chat/providers'
import { CODEX_EXTRA_COLLABORATION_MODE, DEFAULT_CODEX_COLLABORATION_MODE } from '~/components/chat/providers/codex'
import { optionGroupDefaultValue, optionGroupLabel } from '~/components/chat/settingsShared'
import { showWarnToast } from '~/components/common/Toast'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { WorktreeAction } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createLogger } from '~/lib/logger'
import { getInnerMessage, parseMessageContent } from '~/lib/messageParser'
import { defaultEffortForProvider, defaultModelForProvider } from '~/utils/controlResponse'

const logger = createLogger('useAgentOperations')

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
  agentSessionStore: ReturnType<typeof createAgentSessionStore>
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
  const [availableProviders, setAvailableProviders] = createSignal<AgentProvider[]>([])

  const loadAvailableProviders = () => {
    const ctx = props.getCurrentTabContext()
    if (!ctx.workerId)
      return
    workerRpc.listAvailableProviders(ctx.workerId)
      .then((resp) => {
        setAvailableProviders([...resp.providers])
      })
      .catch(() => {
        setAvailableProviders([])
      })
  }

  createEffect(on(
    () => props.getCurrentTabContext().workerId,
    (workerId) => {
      if (workerId)
        loadAvailableProviders()
    },
  ))

  /** Look up the workerId for a given agent from the agent store. */
  const getAgentWorkerId = (agentId: string): string => {
    return props.agentStore.state.agents.find(a => a.id === agentId)?.workerId ?? ''
  }

  const defaultPermissionModeForAgent = (provider: AgentProvider): PermissionMode => {
    return getProviderPlugin(provider)?.defaultPermissionMode ?? 'default'
  }

  // Open a new agent in the given workspace
  const openAgentInWorkspace = async (workspaceId: string, workerId: string, workingDir: string, sessionId?: string, agentProvider: AgentProvider = AgentProvider.CLAUDE_CODE) => {
    try {
      const title = `Agent ${nextTabNumber(props.tabStore.state.tabs, TabType.AGENT, 'Agent')}`
      const resp = await workerRpc.openAgent(workerId, {
        workspaceId,
        agentProvider,
        model: '',
        title,
        systemPrompt: '',
        workerId,
        workingDir,
        ...(agentProvider === AgentProvider.CODEX ? { extraSettings: { [CODEX_EXTRA_COLLABORATION_MODE]: DEFAULT_CODEX_COLLABORATION_MODE } } : {}),
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
          agentProvider: resp.agent.agentProvider,
        })
        props.tabStore.setActiveTabForTile(tileId, TabType.AGENT, resp.agent.id)
        props.agentStore.setActiveAgent(resp.agent.id)
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

  // Open a new agent in the active workspace (for click handlers).
  // When providerOverride is given (from per-provider TabBar buttons),
  // the agent is created directly. When omitted (from dialog or empty
  // tab actions), falls back to Claude Code.
  const handleOpenAgent = async (providerOverride?: AgentProvider) => {
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
    const provider = providerOverride ?? AgentProvider.CLAUDE_CODE
    props.setNewAgentLoading(true)
    try {
      await openAgentInWorkspace(ws.id, ctx.workerId, ctx.workingDir, undefined, provider)
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
      const parsed = JSON.parse(new TextDecoder().decode(content))
      const requestId = parsed?.response?.request_id
        ?? (parsed?.id != null ? String(parsed.id) : undefined)

      await workerRpc.sendControlResponse(workerId, {
        agentId,
        content,
      })

      if (requestId)
        props.controlStore.removeRequest(agentId, requestId)
    }
    catch (err) {
      showWarnToast('Failed to send response', err)
    }
  }

  // Change model or effort for the given agent (requires agent restart)
  const handleModelOrEffortChange = async (agentId: string, field: 'model' | 'effort', value: string) => {
    const agent = props.agentStore.state.agents.find(a => a.id === agentId)
    if (!agent)
      return
    if (!agent.availableModels || agent.availableModels.length === 0)
      return
    const previous = agent[field] || (field === 'model'
      ? defaultModelForProvider(agent.agentProvider)
      : defaultEffortForProvider(agent.agentProvider))
    // Optimistic update
    props.agentStore.updateAgent(agentId, { [field]: value })
    props.settingsLoading.start()
    try {
      await workerRpc.updateAgentSettings(agent.workerId, {
        agentId,
        settings: { [field]: value },
      })
      props.settingsLoading.stop()
    }
    catch (err) {
      props.agentStore.updateAgent(agentId, { [field]: previous })
      props.settingsLoading.stop()
      showWarnToast(`Failed to change ${field}`, err)
    }
  }

  // Interrupt the given agent's current turn
  const handleInterrupt = async (agentId: string) => {
    try {
      const agent = props.agentStore.state.agents.find(a => a.id === agentId)
      const workerId = getAgentWorkerId(agentId)
      const plugin = agent ? getProviderPlugin(agent.agentProvider) : undefined
      if (!plugin?.buildInterruptContent) {
        logger.error('No interrupt handler for provider', agent?.agentProvider)
        return
      }

      const sessionId = agent?.agentSessionId || ''
      const turnId = props.agentSessionStore.getInfo(agentId).codexTurnId || ''
      const content = plugin.buildInterruptContent(sessionId, turnId)
      if (!content)
        return

      await workerRpc.sendAgentRawMessage(workerId, { agentId, content })
    }
    catch (err) {
      showWarnToast('Failed to interrupt', err)
    }
  }

  // Change permission mode for the given agent.
  // Dispatches through the provider plugin — each provider handles this
  // differently (Claude Code: control_request, Codex: UpdateAgentSettings).
  const handlePermissionModeChange = async (agentId: string, mode: PermissionMode) => {
    const agent = props.agentStore.state.agents.find(a => a.id === agentId)
    if (!agent)
      return
    const previousMode = (agent.permissionMode || defaultPermissionModeForAgent(agent.agentProvider)) as PermissionMode
    props.agentStore.updateAgent(agentId, { permissionMode: mode })
    props.settingsLoading.start()
    try {
      const plugin = getProviderPlugin(agent.agentProvider)
      if (plugin?.changePermissionMode) {
        await plugin.changePermissionMode(agent.workerId, agentId, mode)
      }
      else {
        logger.error('No changePermissionMode handler for provider', agent.agentProvider)
      }
      props.settingsLoading.stop()
    }
    catch (err) {
      props.agentStore.updateAgent(agentId, { permissionMode: previousMode })
      props.settingsLoading.stop()
      showWarnToast('Failed to change permission mode', err)
    }
  }

  // Change an option-group setting stored in extraSettings.
  const handleOptionGroupSettingChange = async (
    agentId: string,
    field: string,
    value: string,
    defaultValue: string,
    errorLabel: string,
  ) => {
    const agent = props.agentStore.state.agents.find(a => a.id === agentId)
    if (!agent)
      return
    const previous = agent.extraSettings?.[field] || defaultValue
    props.agentStore.updateAgent(agentId, { extraSettings: { ...(agent.extraSettings || {}), [field]: value } })
    props.settingsLoading.start()
    try {
      await workerRpc.updateAgentSettings(agent.workerId, {
        agentId,
        settings: { extraSettings: { [field]: value } },
      })
      props.settingsLoading.stop()
    }
    catch (err) {
      const current = props.agentStore.state.agents.find(a => a.id === agentId)
      props.agentStore.updateAgent(agentId, { extraSettings: { ...(current?.extraSettings || {}), [field]: previous } })
      props.settingsLoading.stop()
      showWarnToast(`Failed to change ${errorLabel}`, err)
    }
  }

  const handleOptionGroupChange = (agentId: string, key: string, value: string) => {
    const agent = props.agentStore.state.agents.find(a => a.id === agentId)
    const defaultValue = optionGroupDefaultValue(agent?.availableOptionGroups, key) || value
    const label = optionGroupLabel(agent?.availableOptionGroups, key)
    handleOptionGroupSettingChange(agentId, key, value, defaultValue, label)
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
    availableProviders,
    loadAvailableProviders,
    openAgentInWorkspace,
    handleOpenAgent,
    handleResumeAgent,
    handleControlResponse,
    handleModelOrEffortChange,
    handleInterrupt,
    handlePermissionModeChange,
    handleOptionGroupChange,
    handleRetryMessage,
    handleDeleteMessage,
    handleCloseAgent,
  }
}

export type AgentOperations = ReturnType<typeof useAgentOperations>
