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
import { clearAttachments } from '~/components/chat/attachments'
import { CODEX_EXTRA_COLLABORATION_MODE, DEFAULT_CODEX_COLLABORATION_MODE } from '~/components/chat/providers/codex'
import { getProviderPlugin } from '~/components/chat/providers/registry'
import { optionGroupDefaultValue, optionGroupLabel } from '~/components/chat/settingsShared'
import { showWarnToast } from '~/components/common/Toast'
import { toastCloseFailure } from '~/components/shell/closeFailureToast'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { WorktreeAction } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { base64ToUint8Array } from '~/lib/base64'
import { createLogger } from '~/lib/logger'
import { getInnerMessage, parseMessageContent } from '~/lib/messageParser'
import { getMruProviders, touchMruProvider } from '~/lib/mruAgentProviders'
import { resolveOptimisticGitInfo } from '~/stores/tab.store'
import { defaultEffortForProvider, defaultModelForProvider } from '~/utils/controlResponse'
import '~/components/chat/providers'

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
  setNewAgentLoadingProvider: (provider: AgentProvider | null) => void
  persistLayout?: () => void
  focusEditor?: () => void
  forceScrollToBottom?: () => void
}

export function useAgentOperations(props: UseAgentOperationsProps) {
  const [availableProviders, setAvailableProviders] = createSignal<AgentProvider[] | undefined>(undefined)

  const loadAvailableProviders = () => {
    const ctx = props.getCurrentTabContext()
    if (!ctx.workerId)
      return
    workerRpc.listAvailableProviders(ctx.workerId)
      .then((resp) => {
        setAvailableProviders([...resp.providers])
      })
      .catch((err) => {
        // Keep the previous list — a transient refresh failure shouldn't
        // erase a correct list the user was relying on, and conflating
        // failure with "backend said none" would masquerade as an empty
        // provider list once the backend stops forcing a CLAUDE_CODE
        // fallback.
        showWarnToast('Failed to load available agent providers', err)
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
    return props.agentStore.getById(agentId)?.workerId ?? ''
  }

  const resolvePreferredProvider = (): AgentProvider | null => {
    const available = availableProviders() ?? []
    if (available.length === 0)
      return null

    const activeTab = props.tabStore.activeTab()
    if (activeTab?.type === TabType.AGENT && activeTab.agentProvider && available.includes(activeTab.agentProvider))
      return activeTab.agentProvider

    const mru = getMruProviders().find(p => available.includes(p))
    if (mru)
      return mru

    return available[0] ?? null
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
        const afterKey = props.tabStore.getActiveTabKeyForTile(tileId)
        props.agentStore.addAgent(resp.agent)
        // Seed git branch / origin from the active tab when both resolve to
        // the same directory; the authoritative values arrive later on the
        // agent's first status update.
        const newTab = {
          type: TabType.AGENT,
          id: resp.agent.id,
          title,
          tileId,
          workerId: resp.agent.workerId,
          workingDir: resp.agent.workingDir,
          agentProvider: resp.agent.agentProvider,
        }
        const seed = resolveOptimisticGitInfo(props.tabStore.activeTab(), newTab)
        props.tabStore.addTab({ ...newTab, ...seed }, { afterKey })
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
  // the agent is created directly. Otherwise prefer the active agent
  // tab's provider, then the MRU provider, then the first available one.
  const handleOpenAgent = async (providerOverride?: AgentProvider) => {
    if (!props.isActiveWorkspaceMutatable())
      return
    const ws = props.activeWorkspace()
    if (!ws)
      return
    const ctx = props.getCurrentTabContext()
    if (!ctx.workerId || !ctx.workingDir) {
      props.setShowNewAgentDialog(true)
      return
    }
    const provider = providerOverride ?? resolvePreferredProvider()
    if (provider === null) {
      props.setShowNewAgentDialog(true)
      return
    }
    props.setNewAgentLoadingProvider(provider)
    try {
      await openAgentInWorkspace(ws.id, ctx.workerId, ctx.workingDir, undefined, provider)
      touchMruProvider(provider)
    }
    finally {
      props.setNewAgentLoadingProvider(null)
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
    const agent = props.agentStore.getById(agentId)
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
      const agent = props.agentStore.getById(agentId)
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
    const agent = props.agentStore.getById(agentId)
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
    const agent = props.agentStore.getById(agentId)
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
      const current = props.agentStore.getById(agentId)
      props.agentStore.updateAgent(agentId, { extraSettings: { ...(current?.extraSettings || {}), [field]: previous } })
      props.settingsLoading.stop()
      showWarnToast(`Failed to change ${errorLabel}`, err)
    }
  }

  const handleOptionGroupChange = (agentId: string, key: string, value: string) => {
    const agent = props.agentStore.getById(agentId)
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

      // Recover attachments from the failed message (base64-encoded data).
      const rawAttachments = Array.isArray(inner?.attachments)
        ? inner.attachments as Array<{ filename?: string, mime_type?: string, data?: string }>
        : []
      const attachments = rawAttachments
        .filter(a => a.data)
        .map(a => ({
          filename: a.filename ?? '',
          mimeType: a.mime_type ?? '',
          data: base64ToUint8Array(a.data!),
        }))

      props.chatStore.clearMessageError(messageId)
      await workerRpc.sendAgentMessage(workerId, {
        agentId,
        content,
        ...(attachments.length > 0 ? { attachments } : {}),
      })
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
      props.chatStore.setMessageError(messageId, 'Failed to deliver')
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

  // Close an agent.
  //
  // All store mutations run synchronously so the UI updates the moment
  // the caller returns. The worker close RPC and Hub unregister are
  // fire-and-forget; failures are surfaced via toast without blocking
  // the UI or rolling back the local state — the tab is already gone.
  const handleCloseAgent = (agentId: string, worktreeAction: WorktreeAction = WorktreeAction.KEEP) => {
    const workerId = getAgentWorkerId(agentId)

    // Synchronous local cleanup: the tab disappears immediately.
    props.controlStore.clearAgent(agentId)
    clearAttachments(agentId)
    props.agentStore.removeAgent(agentId)
    props.tabStore.removeTab(TabType.AGENT, agentId)

    // Background: kill the subprocess, DB-close the agent, optionally
    // remove the worktree. Partial failures come back as a non-empty
    // failure_message on the response.
    if (workerId) {
      workerRpc.closeAgent(workerId, { agentId, worktreeAction })
        .then(resp => toastCloseFailure(resp.result))
        .catch((err) => {
          showWarnToast('Failed to close agent', err)
        })
    }

    // Unregister tab from hub (runs in parallel with the worker close).
    const ws = props.activeWorkspace()
    if (ws) {
      workspaceClient.removeTab({ workspaceId: ws.id, tabType: TabType.AGENT, tabId: agentId }).catch(() => {})
    }
  }

  return {
    availableProviders,
    loadAvailableProviders,
    openAgentInWorkspace,
    handleOpenAgent,
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
