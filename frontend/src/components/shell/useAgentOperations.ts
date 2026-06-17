import type { TabContext } from './tabContext'
import type { ProviderSettingChange } from '~/components/chat/providers/registry'
import type { CloseTabResult } from '~/generated/leapmux/v1/common_pb'
import type { Workspace } from '~/generated/leapmux/v1/workspace_pb'
import type { ToggleDialogState } from '~/hooks/createDialogState'
import type { createAgentSessionStore } from '~/stores/agentSession.store'
import type { createChatStore } from '~/stores/chat.store'
import type { createControlStore } from '~/stores/control.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore } from '~/stores/tab.store'

import type { PermissionMode } from '~/utils/controlResponse'
import { createEffect, createSignal, on } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { clearAttachments } from '~/components/chat/attachments'
import { openAgentRequestOptions } from '~/components/chat/providers/registry'
import { ACCOUNT_DEFAULT_MODEL, OPTION_ID_EFFORT, OPTION_ID_MODEL, OPTION_ID_PERMISSION_MODE, optionGroupLabel } from '~/components/chat/settingsGroups'
import { showWarnToast } from '~/components/common/Toast'
import { awaitCloseResult, warnWorktreeUnreachable } from '~/components/shell/closeResultToast'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { WorktreeAction } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { base64ToUint8Array } from '~/lib/base64'
import { getInnerMessage, parseMessageContent } from '~/lib/messageParser'
import { getMruProviders, touchMruProvider } from '~/lib/mruAgentProviders'
import { protoToAgentTabFields, resolveOptimisticGitInfo, setOptionValue } from '~/stores/tab.helpers'
import '~/components/chat/providers'

export interface UseAgentOperationsProps {
  agentSessionStore: ReturnType<typeof createAgentSessionStore>
  chatStore: ReturnType<typeof createChatStore>
  controlStore: ReturnType<typeof createControlStore>
  tabStore: ReturnType<typeof createTabStore>
  layoutStore: ReturnType<typeof createLayoutStore>
  settingsLoading: {
    start: (key?: string, axes?: readonly string[]) => void
    stop: (key?: string, axes?: readonly string[]) => void
  }
  isActiveWorkspaceMutatable: () => boolean
  activeWorkspace: () => Workspace | null
  getCurrentTabContext: () => Pick<TabContext, 'workerId' | 'workingDir'>
  newAgentDialog: ToggleDialogState
  setNewAgentLoadingProvider: (provider: AgentProvider | null) => void
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

  /** Look up the workerId for a given agent from the tab store. */
  const getAgentWorkerId = (agentId: string): string => {
    return props.tabStore.getAgentTab(agentId)?.workerId ?? ''
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

  // Open a new agent in the given workspace
  const openAgentInWorkspace = async (workspaceId: string, workerId: string, workingDir: string, sessionId?: string, agentProvider: AgentProvider = AgentProvider.CLAUDE_CODE) => {
    try {
      // Title left empty: the worker picks "Agent <Name>" server-side
      // so CLI and UI paths share one pool (see worker/service/
      // tab_names.go). The response carries the resolved title back.
      const resp = await workerRpc.openAgent(workerId, {
        workspaceId,
        agentProvider,
        workerId,
        workingDir,
        ...openAgentRequestOptions(agentProvider),
        ...(sessionId ? { agentSessionId: sessionId } : {}),
      })
      if (resp.agent) {
        const tileId = props.layoutStore.focusedTileId()
        const afterKey = props.tabStore.getActiveTabKeyForTile(tileId)
        // Build the agent tab from the OpenAgent response. protoToAgent
        // TabFields populates every per-agent column on the tab record
        // and primes the settings-label cache with the agent's catalogs.
        const agentFields = protoToAgentTabFields(resp.agent.workerId, resp.agent)
        // Seed git branch / origin from the active tab when both resolve to
        // the same directory; the authoritative values arrive later on the
        // agent's first status update. Agent tabs have no shellStartDir —
        // effectiveGitDir collapses to workingDir for them.
        const seed = resolveOptimisticGitInfo(props.tabStore.activeTab(), {
          workingDir: agentFields.workingDir,
        })
        props.tabStore.addTab({
          type: TabType.AGENT,
          id: resp.agent.id,
          tileId,
          ...agentFields,
          ...seed,
        }, { afterKey })
        props.tabStore.setActiveTabForTile(tileId, TabType.AGENT, resp.agent.id)
        // `tabStore.addTab` emits the CRDT op batch (SetTabRegister
        // tile_id + position + worker_id) via the bridge so peer
        // clients pick the tab up via /ws/orgevents.
        void workspaceId
        void workerId
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
      props.newAgentDialog.open()
      return
    }
    const provider = providerOverride ?? resolvePreferredProvider()
    if (provider === null) {
      props.newAgentDialog.open()
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

  // Interrupt the given agent's current turn. The worker dispatches
  // the provider-specific signal (Codex turn/cancel, Claude Code
  // interrupt control payload, etc.), so the frontend doesn't have
  // to synthesize provider JSON.
  const handleInterrupt = async (agentId: string) => {
    try {
      const workerId = getAgentWorkerId(agentId)
      await workerRpc.interruptAgent(workerId, { agentId })
    }
    catch (err) {
      showWarnToast('Failed to interrupt', err)
    }
  }

  /**
   * After a MODEL change settles, snap the optimistic model/effort onto what the
   * session actually resolved to, read from the RPC reply's confirmed options. A
   * model switch RELAUNCHES the agent, and the relaunch can land on different values
   * than the optimistic click: the account-default sentinel ("default") resolves to a
   * concrete model, and the relaunch resets effort to the new model's default. Reading
   * the settled values from the reply (rather than a separately-broadcast catalog)
   * removes the cross-channel ordering assumption -- the reply IS the confirmation.
   * Without this, a switch to "default" would freeze the trigger on
   * "Default (recommended)" until an unrelated status push arrived.
   *
   * Scoped to MODEL changes by its sole caller -- the model->effort dependency is
   * legitimate domain knowledge, not a stored-value special-case.
   *
   * Each axis snaps ONLY when it still holds the value this change left it at
   * (`curValues[axis] === expected`). A rapid re-click on the same axis -- or a
   * concurrent effort edit while this model RPC was in flight -- writes a newer
   * optimistic value with its own pending RPC; snapping this reply's (now stale)
   * value over it would revert the user's newer selection out from under that
   * request. This mirrors the per-axis guard the failure-rollback path applies.
   */
  const reconcileSettledModelChange = (
    agentId: string,
    value: string,
    confirmed: Record<string, string>,
    expectedEffort: string | undefined,
  ) => {
    const curValues = props.tabStore.getAgentTab(agentId)?.optionValues || {}
    const settled: Record<string, string> = {}
    // Snap the MODEL only when the optimistic click was the account-default sentinel:
    // a relaunch resolves it to a concrete model. A concrete model the user explicitly
    // picked KEEPS its optimistic value -- an ACP provider (Cursor) applies a model
    // switch LIVE, and snapping it to the confirmed echo is unnecessary churn. The
    // `=== value` guard skips the snap when a newer model re-click already overwrote it.
    if (value === ACCOUNT_DEFAULT_MODEL && confirmed[OPTION_ID_MODEL] && curValues[OPTION_ID_MODEL] === value)
      settled[OPTION_ID_MODEL] = confirmed[OPTION_ID_MODEL]
    // Snap effort to the confirmed (relaunch-reset) value when the agent reports one.
    // Absent for a provider with no effort axis (Cursor) or an effort-less model
    // (Haiku), and the confirmed value already reflects any clamp the CLI applied. The
    // `=== expectedEffort` guard skips the snap when a concurrent effort edit's
    // in-flight RPC already wrote a newer value, which must win over this stale reply.
    if (confirmed[OPTION_ID_EFFORT] && curValues[OPTION_ID_EFFORT] === expectedEffort)
      settled[OPTION_ID_EFFORT] = confirmed[OPTION_ID_EFFORT]
    if (Object.keys(settled).length === 0)
      return
    props.tabStore.updateTab(TabType.AGENT, agentId, {
      optionValues: { ...curValues, ...settled },
    })
  }

  /**
   * Single entry point for any settings panel change. The settings model is now
   * uniform: every change is a map of option-group id -> value (one axis for a plain
   * option pick, several for an action button like Codex "Bypass permissions"). We
   * optimistically write every axis into the tab's one generic `optionValues` map
   * (model/effort/permissionMode and every provider extra alike, keyed by group id) and
   * send ONE updateAgentSettings RPC carrying `{ options: sets }`, so a multi-axis change
   * is applied ATOMICALLY -- the worker can't accept one axis and reject another, leaving
   * the agent half-applied while the optimistic UI shows the full state. The worker
   * decides how to apply it (live vs restart), so the frontend no longer special-cases
   * permission mode or any other axis.
   */
  const handleAgentSettingChange = async (agentId: string, change: ProviderSettingChange) => {
    const { sets } = change
    const keys = Object.keys(sets)
    if (keys.length === 0)
      return
    const agent = props.tabStore.getAgentTab(agentId)
    if (!agent || !agent.workerId)
      return
    // Refuse a change for an agent that has reported no option catalog yet: there is no group to
    // back the optimistic write, so the UI would show a selection nothing can reconcile, and the
    // RPC would target an axis the running session may not validate. The pre-unification model/effort
    // handler refused the same way on an empty availableModels list; programmatic callers (the
    // control-request "& Bypass Permissions" button, the plan-mode toggle) can otherwise reach here
    // with an empty catalog because their visibility is gated on a static provider constant, not the
    // live catalog.
    if (!agent.optionGroups || agent.optionGroups.length === 0)
      return

    // Capture each axis's prior optimistic value so a rollback can restore it exactly --
    // including deleting a key that had none. Writing '' instead would make
    // agentTabOptionGroups treat '' as a real override and blank the group's selection
    // (showing its default) rather than falling through to the catalog's confirmed currentValue.
    const priors = keys.map(key => ({
      key,
      hadPrevious: agent.optionValues != null && key in agent.optionValues,
      previous: agent.optionValues?.[key],
    }))

    // The effort value this change leaves in the store, snapshotted NOW -- before the optimistic
    // write and the in-flight RPC. agent.optionValues is a live store proxy, so reading it after
    // the await would see a concurrent edit's value. A change that also sets effort leaves its
    // requested value; a model-only change leaves effort untouched at its pre-change value.
    // reconcileSettledModelChange snaps effort only while the store still holds this value, so a
    // concurrent effort edit (its own in-flight RPC) wins instead of being clobbered.
    const expectedEffort = OPTION_ID_EFFORT in sets ? sets[OPTION_ID_EFFORT] : agent.optionValues?.[OPTION_ID_EFFORT]

    // Optimistic update -- apply EVERY axis in one updateTab so a multi-axis change shows its
    // combined state atomically. setOptionValue preserves the other axes' optimistic values and
    // enforces the "never store empty" invariant (an empty value deletes the key rather than
    // blanking the group with a spurious '' override).
    let optimistic = agent.optionValues
    for (const key of keys)
      optimistic = setOptionValue(optimistic, key, sets[key])
    props.tabStore.updateTab(TabType.AGENT, agentId, { optionValues: optimistic })

    // Scope the in-flight marker to THIS agent AND to the axes this change touches, so the
    // statusChange handler suppresses optimistic-value overwrites only for these axes on this
    // agent -- another agent's unrelated push, and a server-initiated change to a DIFFERENT axis
    // on this same agent, still apply their own confirmed current values.
    props.settingsLoading.start(agentId, keys)
    let resp: Awaited<ReturnType<typeof workerRpc.updateAgentSettings>>
    try {
      resp = await workerRpc.updateAgentSettings(agent.workerId, {
        agentId,
        settings: { options: { ...sets } },
      })
    }
    catch (err) {
      // Roll back every axis this change set (other axes preserved via the spread). Restore
      // each axis's prior value, or delete its key when it had no optimistic value before --
      // so the group falls back to the catalog's confirmed currentValue instead of a spurious
      // empty override.
      //
      // Roll back an axis only when THIS change's optimistic value is still the current one. A
      // newer change to the same key (a rapid re-click) may have overwritten it while this RPC
      // was in flight; restoring this change's stale `previous` would revert the user's newer
      // selection out from under its own in-flight request.
      const current = props.tabStore.getAgentTab(agentId)
      const rolledBack = { ...(current?.optionValues || {}) }
      let didRollback = false
      for (const { key, hadPrevious, previous } of priors) {
        if (rolledBack[key] !== sets[key])
          continue
        didRollback = true
        if (hadPrevious)
          rolledBack[key] = previous as string
        else
          delete rolledBack[key]
      }
      if (didRollback)
        props.tabStore.updateTab(TabType.AGENT, agentId, { optionValues: rolledBack })
      props.settingsLoading.stop(agentId, keys)
      showWarnToast(`Failed to change ${keys.map(key => optionGroupLabel(agent.optionGroups, key)).join(', ')}`, err)
      return
    }
    // The RPC succeeded. Clear the in-flight marker and reconcile OUTSIDE the rollback
    // guard above: a fault while reconciling a confirmed change must not be mistaken for
    // an RPC failure, which would revert the just-applied value and pop a false error
    // toast. Stop FIRST so a (theoretical) reconcile fault can't strand the spinner until
    // the safety-net timeout; reconcile runs synchronously right after, so no status push
    // can interleave and overwrite the optimistic value before it snaps.
    //
    // A non-model live-apply change is NOT reply-reconciled: an ACP server's
    // set_config_option response may omit the refreshed configOptions, so the reply's
    // confirmed value can lag the just-applied selection, and snapping to it would
    // revert a correct optimistic value. A genuinely unapplied change is instead
    // corrected provider-side -- Pi/Codex/Claude restart when they can't apply a change
    // live (see PiAgent.UpdateSettings) -- and via the next status push.
    props.settingsLoading.stop(agentId, keys)
    if (OPTION_ID_MODEL in sets)
      reconcileSettledModelChange(agentId, sets[OPTION_ID_MODEL], resp.confirmedOptions ?? {}, expectedEffort)
  }

  /**
   * Permission-mode change shim for the approval-control "& Bypass Permissions"
   * button (which calls onPermissionModeChange directly). Routes through the
   * unified dispatcher.
   */
  const handlePermissionModeChange = (agentId: string, mode: PermissionMode) =>
    handleAgentSettingChange(agentId, { sets: { [OPTION_ID_PERMISSION_MODE]: mode } })

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
  const handleAgentClose = (agentId: string, worktreeAction: WorktreeAction = WorktreeAction.KEEP): Promise<CloseTabResult | undefined> => {
    const workerId = getAgentWorkerId(agentId)

    // Synchronous local cleanup: the tab disappears immediately.
    props.controlStore.clearAgent(agentId)
    clearAttachments(agentId)
    props.tabStore.removeTab(TabType.AGENT, agentId)

    // `tabStore.removeTab` above emitted the TombstoneTab op via the
    // CRDT bridge; the hub broadcasts it to peer clients via
    // /ws/orgevents.
    if (!workerId) {
      // No worker to send the close to. The local tab is gone, but a
      // REMOVE can't reach the worker — say so rather than letting the
      // caller assume the worktree was removed.
      warnWorktreeUnreachable(worktreeAction)
      return Promise.resolve(undefined)
    }

    // Background: kill the subprocess, DB-close the agent, optionally
    // remove the worktree. Partial failures come back as a non-empty
    // failure_message on the response; the resolved result lets the
    // delete-branch flow report the actual worktree outcome.
    return awaitCloseResult(workerRpc.closeAgent(workerId, { agentId, worktreeAction }), 'Failed to close agent')
  }

  return {
    availableProviders,
    loadAvailableProviders,
    openAgentInWorkspace,
    handleOpenAgent,
    handleControlResponse,
    handleInterrupt,
    handlePermissionModeChange,
    handleAgentSettingChange,
    handleRetryMessage,
    handleDeleteMessage,
    handleAgentClose,
  }
}

export type AgentOperations = ReturnType<typeof useAgentOperations>
