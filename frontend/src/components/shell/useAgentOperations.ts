import type { TabContext } from './tabContext'
import type { ProviderSettingChange } from '~/components/chat/providerSettings'
import type { CloseTabResult } from '~/generated/proto/leapmux/v1/common_pb'
import type { Workspace } from '~/generated/proto/leapmux/v1/workspace_pb'
import type { ToggleDialogState } from '~/hooks/createDialogState'
import type { createAgentSessionStore } from '~/stores/agentSession.store'
import type { createChatStore } from '~/stores/chat.store'
import type { createControlStore } from '~/stores/control.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createRepoGitStore } from '~/stores/repoGit.store'
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import type { TabView } from '~/stores/tabView'

import { createEffect, createSignal, on, onCleanup } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { clearAttachments } from '~/components/chat/attachments'
import { openAgentRequestOptions } from '~/components/chat/providers/registry'
import { optionGroupLabel } from '~/components/chat/settingsGroups'
import { showWarnToast, showWarnToastUnlessDisconnected } from '~/components/common/Toast'
import { awaitCloseResult, warnWorktreeUnreachable } from '~/components/shell/closeResultToast'
import { AgentOptionSettlementState, AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { WorktreeAction } from '~/generated/proto/leapmux/v1/common_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { base64ToUint8Array } from '~/lib/base64'
import { getInnerMessage, parseMessageContent } from '~/lib/messageParser'
import { getMruProviders, touchMruProvider } from '~/lib/mruAgentProviders'
import { openedAgentTabFields, planOptimisticRepoGit, setOptionValue } from '~/stores/tab.helpers'
import { emitRemoveTab, emitRemoveTabs, hasLiveTabRecord } from '~/stores/tabOps'
import { openTabInFocusedTile } from './openTabInFocusedTile'
import { warnUnlessPlaceableTab } from './placeableTabGuard'
import '~/components/chat/providers'

export interface UseAgentOperationsProps {
  agentSessionStore: ReturnType<typeof createAgentSessionStore>
  chatStore: ReturnType<typeof createChatStore>
  controlStore: ReturnType<typeof createControlStore>
  view: TabView
  metadata: TabMetadataStore
  selection: TabSelectionStore
  getActiveWorkspaceId: () => string | null
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
  repoGitStore: ReturnType<typeof createRepoGitStore>
}

export function useAgentOperations(props: UseAgentOperationsProps) {
  const [availableProviders, setAvailableProviders] = createSignal<AgentProvider[] | undefined>(undefined)
  /** The newest provider scan, so a superseded one can be aborted. */
  let inflightProviderScan: AbortController | undefined

  /**
   * Load the worker's provider list, and stop caring about the answer when
   * the tab moves off that worker.
   *
   * The RETRY is not here. An incomplete provider scan answers Unavailable,
   * which the worker declares as "ask again", and `callWorker` retries
   * every such reply with a capped backoff — so this hook sees only the
   * settled outcome. The cancellation guard IS here, because it is about
   * this hook's own state: a reply for the previous worker must not
   * overwrite the list for the one the user moved to.
   *
   * The guard sits on the WRITE, not on the call. Handing a cancel closure
   * back to the caller only worked for the one caller that registered it;
   * the refresh button in the agent dialogs reaches this same function
   * through five hops that each discard the return value, so a manual
   * refresh for the previous worker could still settle late — after 1.4s
   * of `Unavailable` backoff — and overwrite the current worker's list.
   * Both the abort signal and the issuing worker id are re-checked here,
   * so no caller can hold the guard wrong.
   */
  const loadAvailableProviders = (): void => {
    // One list, so one scan: a newer request supersedes the older one, and
    // aborting it stops a pending backoff from keeping a dead request
    // alive. This runs BEFORE the no-worker guard, so a tab that moves off
    // every worker drops the previous worker's scan rather than leaving it
    // to retry for an answer nobody can use.
    inflightProviderScan?.abort()
    inflightProviderScan = undefined
    const ctx = props.getCurrentTabContext()
    if (!ctx.workerId)
      return
    const abort = new AbortController()
    inflightProviderScan = abort
    const issuedFor = ctx.workerId
    const superseded = () =>
      abort.signal.aborted || props.getCurrentTabContext().workerId !== issuedFor
    workerRpc.listAvailableProviders(issuedFor, { signal: abort.signal })
      .then((resp) => {
        if (superseded())
          return
        setAvailableProviders([...resp.providers])
      })
      .catch((err) => {
        if (superseded())
          return
        // Keep the previous list — a transient refresh failure shouldn't
        // erase a correct list the user was relying on, and conflating
        // failure with "backend said none" would masquerade as an empty
        // provider list once the backend stops forcing a CLAUDE_CODE
        // fallback.
        //
        // The scan runs from an effect, not from a gesture, so a dropped
        // connection needs no toast of its own here: useWatchEventsStreams
        // announces the outage once, and this effect re-runs on the next
        // worker change.
        showWarnToastUnlessDisconnected('Failed to load available agent providers', err)
      })
  }

  // The signal this scan writes lives on the hook, so the abort does too:
  // a reply that arrives after the hook is disposed must write nothing.
  onCleanup(() => inflightProviderScan?.abort())

  createEffect(on(
    () => props.getCurrentTabContext().workerId,
    () => loadAvailableProviders(),
  ))

  /** Look up the workerId for a given agent from the projection join. */
  const getAgentWorkerId = (agentId: string): string => {
    return props.view.getAgentTab(agentId)?.workerId ?? ''
  }

  const resolvePreferredProvider = (): AgentProvider | null => {
    const available = availableProviders() ?? []
    if (available.length === 0)
      return null

    const activeTab = props.selection.activeTabForWorkspace(props.getActiveWorkspaceId() ?? '')
    if (activeTab?.type === TabType.AGENT && activeTab.agentProvider && available.includes(activeTab.agentProvider))
      return activeTab.agentProvider

    const mru = getMruProviders().find(p => available.includes(p))
    if (mru)
      return mru

    return available[0] ?? null
  }

  // Open a new agent in the given workspace. Answers whether an agent was
  // actually opened, so callers can record the "use" only on success — a
  // refused open (see the guard below) is not a use.
  const openAgentInWorkspace = async (workspaceId: string, workerId: string, workingDir: string, sessionId?: string, agentProvider: AgentProvider = AgentProvider.CLAUDE_CODE): Promise<boolean> => {
    // BEFORE the worker RPC: it is the step that can't be taken back. A
    // placement refusal after it (no projected tree to place on — e.g. the
    // workspace's tree hasn't arrived) leaves an orphaned agent behind with
    // no tab to reach it by.
    if (!warnUnlessPlaceableTab(props.layoutStore, 'an agent'))
      return false
    try {
      // Title left empty: the worker picks "Agent <Name>" server-side
      // so CLI and UI paths share one pool (see worker/service/
      // tab_names.go). The response carries the resolved title back.
      const resp = await workerRpc.openAgent(workerId, {
        agentProvider,
        workerId,
        workingDir,
        ...openAgentRequestOptions(agentProvider),
        ...(sessionId ? { agentSessionId: sessionId } : {}),
      })
      if (resp.agent) {
        // Seed git branch / origin from the active tab when both resolve to
        // the same directory. Agent tabs have no shellStartDir --
        // effectiveGitDir collapses to workingDir for them.
        //
        // The worker resolves the real values later. The OpenAgent response
        // carries no git status: the worker computes it in startup phase 1 and
        // sends it on the STARTING broadcast, because the `git status`
        // shell-out would otherwise block the RPC. So this seed is what the
        // sidebar shows until that broadcast lands.
        //
        // ONE read of the active tab feeds both the dir-match guard and the
        // branch copy. A second read would let a later edit give the two
        // different tabs.
        const activeTab = props.selection.activeTabForWorkspace(workspaceId)
        const seed = planOptimisticRepoGit(
          props.repoGitStore,
          activeTab,
          { workerId: resp.agent.workerId, workingDir: resp.agent.workingDir },
        )
        // Everything the OpenAgent response carries that the CRDT does not:
        // title, provider, status, session, option catalogs. This also primes
        // the settings-label cache with the agent's catalogs.
        //
        // `hydrated`: the OpenAgent response IS the worker's answer for this
        // tab, so `useTabHydrators` must not immediately re-ask. Its reply
        // would land without the pending-axis suppression the live settings
        // path applies, overwriting an optimistic edit made during the
        // round-trip.
        const agentFields = openedAgentTabFields(props.repoGitStore, resp.agent)
        const placedTileId = openTabInFocusedTile(
          props,
          { type: TabType.AGENT, id: resp.agent.id, workerId: resp.agent.workerId },
          // `seed.fields` FIRST, so the worker's own answer wins on the row for
          // the same reason it wins in the store. The two halves of a tab's
          // repo identity must not resolve a conflict in opposite directions.
          //
          // Today the response carries no toplevel, so the seed is the only
          // source and the order changes nothing. It matters if the response
          // ever carries one and it differs from the active tab's guess.
          { ...seed.fields, ...agentFields },
        )
        // Only now, because placement can be refused. Writing the store first
        // leaves an entry that no tab reads and nothing reclaims.
        if (placedTileId)
          seed.commit()
        // Focus the editor after the reactive updates propagate to the DOM.
        requestAnimationFrame(() => props.focusEditor?.())
        return true
      }
      return false
    }
    catch (err) {
      showWarnToast('Failed to open agent', err)
      return false
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
      // Only a successful open counts as a use: a refused or failed open
      // records nothing, so the MRU list keeps reflecting real usage.
      if (await openAgentInWorkspace(ws.id, ctx.workerId, ctx.workingDir, undefined, provider))
        touchMruProvider(provider)
    }
    finally {
      props.setNewAgentLoadingProvider(null)
    }
  }

  // Handle control responses (permission grant/deny) for agent prompts. answeredClaimToken is the
  // per-instance token captured from the ACTIVE control request at the answer site (AgentEditorPanel /
  // controlResponseHandling), threaded through so it is always the answered instance's token -- even
  // once the request has left the store (a double-submit / answer-after-cancel race).
  const handleControlResponse = async (agentId: string, requestId: string, content: Uint8Array, answeredClaimToken?: string) => {
    props.forceScrollToBottom?.()
    try {
      const workerId = getAgentWorkerId(agentId)
      // Echo the per-instance claimToken the worker minted for THIS request so its idempotency claim
      // dedups per instance -- a reused request_id gets a fresh token (see AgentControlRequest.claim_token).
      // Prefer the token threaded from the answer site (authoritative even after the request left the
      // store); fall back to a store lookup for any caller that doesn't thread one, then to '' (which
      // degrades to request_id-only dedup on the worker rather than dropping the answer).
      const claimToken = answeredClaimToken
        ?? (requestId ? props.controlStore.getRequest(agentId, requestId)?.claimToken : undefined)
        ?? ''

      await workerRpc.sendControlResponse(workerId, {
        agentId,
        content,
        claimToken,
      })

      if (requestId)
        props.controlStore.removeRequest(agentId, requestId)
    }
    catch (err) {
      showWarnToast('Failed to send response', err)
      throw err
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

  const settingVersions = new Map<string, Map<string, number>>()
  const pendingSettingRequests = new Map<string, number>()

  const beginSettingRequest = (agentId: string, keys: readonly string[]) => {
    const versions = settingVersions.get(agentId) ?? new Map<string, number>()
    settingVersions.set(agentId, versions)
    for (const key of keys)
      versions.set(key, (versions.get(key) ?? 0) + 1)
    pendingSettingRequests.set(agentId, (pendingSettingRequests.get(agentId) ?? 0) + 1)
    return new Map(versions)
  }

  const finishSettingRequest = (agentId: string) => {
    const pending = (pendingSettingRequests.get(agentId) ?? 1) - 1
    if (pending > 0) {
      pendingSettingRequests.set(agentId, pending)
      return
    }
    pendingSettingRequests.delete(agentId)
    settingVersions.delete(agentId)
  }

  const requestStillOwnsAxis = (agentId: string, versions: Map<string, number>, axis: string) =>
    (settingVersions.get(agentId)?.get(axis) ?? 0) === (versions.get(axis) ?? 0)

  const reconcileSettingSettlements = (
    agentId: string,
    requestVersions: Map<string, number>,
    settlements: Awaited<ReturnType<typeof workerRpc.updateAgentSettings>>['optionSettlements'],
  ) => {
    const current = props.view.getAgentTab(agentId)?.optionValues || {}
    const reconciled = { ...current }
    let changed = false
    for (const [axis, settlement] of Object.entries(settlements)) {
      if (settlement.state !== AgentOptionSettlementState.CONFIRMED || !requestStillOwnsAxis(agentId, requestVersions, axis))
        continue
      if (settlement.value === undefined) {
        if (axis in reconciled) {
          delete reconciled[axis]
          changed = true
        }
      }
      else if (reconciled[axis] !== settlement.value) {
        reconciled[axis] = settlement.value
        changed = true
      }
    }
    if (changed)
      props.metadata.patch(agentId, { optionValues: reconciled })
  }

  /**
   * Single entry point for any settings panel change. The settings model is now
   * uniform: every change is a map of option-group id -> value (one axis for a plain
   * option pick, several for an action button like Codex "Bypass permissions"). We
   * optimistically write every axis into the tab's one generic `optionValues` map
   * (model/effort/permissionMode and every provider extra alike, keyed by group id) and
   * send one updateAgentSettings RPC carrying `{ options: sets }`. The worker owns
   * live application, restart fallback, and settlement for the complete change.
   */
  const handleAgentSettingChange = async (agentId: string, change: ProviderSettingChange) => {
    const { sets } = change
    const keys = Object.keys(sets)
    if (keys.length === 0)
      return
    const agent = props.view.getAgentTab(agentId)
    if (!agent || !agent.workerId)
      return
    // Refuse a change for an agent that has reported no option catalog yet: there is no group to
    // back the optimistic write, so the UI would show a selection nothing can reconcile, and the
    // RPC would target an axis the running session may not validate. The pre-unification model/effort
    // handler refused the same way on an empty availableModels list; programmatic callers (the
    // control-request bypass switch, the plan-mode toggle) can otherwise reach here
    // with an empty catalog because a static provider constant controls their visibility, not the
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

    // Optimistic update -- apply EVERY axis in one patch so a multi-axis change shows its
    // combined state atomically. setOptionValue preserves the other axes' optimistic values and
    // enforces the "never store empty" invariant (an empty value deletes the key rather than
    // blanking the group with a spurious '' override).
    let optimistic = agent.optionValues
    for (const key of keys)
      optimistic = setOptionValue(optimistic, key, sets[key])
    props.metadata.patch(agentId, { optionValues: optimistic })

    const requestVersions = beginSettingRequest(agentId, keys)

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
      // Roll back an axis only when this request still owns its version. A newer
      // request can select the same value, so value equality cannot detect this race.
      const current = props.view.getAgentTab(agentId)
      const rolledBack = { ...(current?.optionValues || {}) }
      let didRollback = false
      for (const { key, hadPrevious, previous } of priors) {
        if (!requestStillOwnsAxis(agentId, requestVersions, key))
          continue
        didRollback = true
        if (hadPrevious)
          rolledBack[key] = previous as string
        else
          delete rolledBack[key]
      }
      if (didRollback)
        props.metadata.patch(agentId, { optionValues: rolledBack })
      props.settingsLoading.stop(agentId, keys)
      finishSettingRequest(agentId)
      showWarnToast(`Failed to change ${keys.map(key => optionGroupLabel(agent.optionGroups, key)).join(', ')}`, err)
      return
    }
    // The RPC succeeded. Clear the in-flight marker and reconcile OUTSIDE the rollback
    // guard above: a fault while reconciling a confirmed change must not be mistaken for
    // an RPC failure, which would revert the just-applied value and pop a false error
    // toast. Stop FIRST so a (theoretical) reconcile fault can't strand the spinner until
    // the safety-net timeout; reconcile runs synchronously right after, so no status push
    // can interleave and overwrite the optimistic value before it snaps.
    props.settingsLoading.stop(agentId, keys)
    try {
      reconcileSettingSettlements(agentId, requestVersions, resp.optionSettlements ?? {})
    }
    finally {
      finishSettingRequest(agentId)
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
      // The resend SUCCEEDED (the new message arrives via WatchEvents), so removing the
      // old failed bubble is best-effort cleanup. A delete failure here must NOT fall
      // through to the outer catch and re-stamp "Failed to deliver": that would mislead
      // the user into thinking the retry failed when it actually landed. The worker now
      // rejects deleteAgentMessage unless the row is still a FAILED user message, so a
      // concurrent state change (or a transient network error) can make this throw after
      // a good resend.
      if (messageId.startsWith('local-')) {
        props.chatStore.removeMessage(agentId, messageId)
      }
      else {
        try {
          await workerRpc.deleteAgentMessage(workerId, { agentId, messageId })
          props.chatStore.removeMessage(agentId, messageId)
        }
        catch (cleanupErr) {
          showWarnToast('Could not remove the old failed message', cleanupErr)
        }
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

  /**
   * Reclaim every per-agent store entry a closed agent tab leaves behind.
   *
   * Separate from the tombstone, because the two have different owners: the
   * tombstone is one CRDT op that a caller may want to batch with others, and
   * this is the local state that NOTHING else reclaims. `forgetAgent` has no
   * other production caller, and the tombstone-driven metadata sweep drops
   * `tabMetadata` rows only -- so an agent tab that leaves without this call
   * strands its loaded window, live tail, command streams, span index, to-dos,
   * streaming text and pending outbound for the life of the page.
   *
   * Every path that retires an agent tab must call it: the ordinary close
   * below, and the descendant sweep that follows the worker's answer.
   */
  const retireAgentTabLocally = (agentId: string) => {
    props.controlStore.clearAgent(agentId)
    clearAttachments(agentId)
    props.chatStore.forgetAgent(agentId)
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
    retireAgentTabLocally(agentId)
    emitRemoveTab(TabType.AGENT, agentId)

    // The TombstoneTab op above is the removal: the tab leaves the projection
    // and therefore every view, and the hub broadcasts it to peer clients via
    // /ws/userevents.
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
    const rpc = workerRpc.closeAgent(workerId, { agentId, worktreeAction })

    // Retire the subagent tabs the worker reports underneath this one.
    //
    // `handleTabClose` already swept them optimistically from this client's own
    // tabs, which is what makes the strip update on the click rather than a
    // round-trip later. This is the AUTHORITATIVE pass behind it: the parent
    // chain lives on the worker, and a subagent tab opened moments ago carries
    // no `parentAgentId` until `listAgents` hydrates one -- so the optimistic
    // sweep cannot see it, and the tab would outlive the agent that fed it.
    // A tombstone is idempotent, so the two passes overlapping costs nothing.
    //
    // Errors are the RPC's own, and `awaitCloseResult` below already toasts
    // them; a second handler would report the same failure twice.
    //
    // The rejection handler is the SECOND argument to `then`, not a trailing
    // `catch`: a trailing one also swallows a throw from the sweep itself, so a
    // failure half way through would leave the remaining descendant tabs on
    // screen with no toast and no console trace. This form scopes the swallow
    // to the RPC's own rejection and lets a sweep error surface.
    rpc.then((resp) => {
      // Only the ids this client holds a LIVE tab record for. The worker answers
      // from the `agents` table, where `EnsureChildAgent` creates a row on first
      // sight of a spawn -- whether or not anyone ever opened that subagent's
      // transcript. The hub rejects a tombstone for an id it has no record for,
      // so an unfiltered list makes the hub reject the batch instead of retiring
      // the tabs. Filtering on LIVE also drops the ids the optimistic sweep just
      // tombstoned, which would otherwise be rejected the same way.
      const ids = (resp.descendantAgentIds ?? []).filter(hasLiveTabRecord)
      if (ids.length === 0)
        return
      for (const id of ids)
        retireAgentTabLocally(id)
      // One batch, not one per id: the hub dedups, validates, journals and swaps
      // state per BATCH, so a per-id loop pays all of that N times to say what
      // one batch says.
      emitRemoveTabs(TabType.AGENT, ids)
    }, () => {})

    return awaitCloseResult(rpc, 'Failed to close agent')
  }

  return {
    availableProviders,
    loadAvailableProviders,
    openAgentInWorkspace,
    handleOpenAgent,
    handleControlResponse,
    handleInterrupt,
    handleAgentSettingChange,
    handleRetryMessage,
    handleDeleteMessage,
    handleAgentClose,
    retireAgentTabLocally,
  }
}

export type AgentOperations = ReturnType<typeof useAgentOperations>
