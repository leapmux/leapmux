import type { AgentEvent, TerminalEvent, WatchAgentEntry } from '~/generated/leapmux/v1/workspace_pb'
import type { createLoadingSignal } from '~/hooks/createLoadingSignal'
import type { createAgentStore } from '~/stores/agent.store'
import type { createAgentSessionStore } from '~/stores/agentSession.store'
import type { createChatStore } from '~/stores/chat.store'
import type { createControlStore } from '~/stores/control.store'
import type { createTabStore } from '~/stores/tab.store'
import type { createTerminalStore } from '~/stores/terminal.store'
import type { WorkspaceStoreRegistryType } from '~/stores/workspaceStoreRegistry'
import { createEffect, createSignal, onCleanup, untrack } from 'solid-js'
import { watchEventsViaChannel } from '~/api/workerRpc'
import { showWarnToast } from '~/components/common/Toast'
import { getTerminalInstance } from '~/components/terminal/TerminalView'
import { AgentStatus, MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { ChannelError } from '~/lib/channel'
import { createLogger } from '~/lib/logger'
import { extractAgentRenamed, extractAssistantUsage, extractPlanFilePath, extractRateLimitInfo, extractResultMetadata, extractSettingsChanges, getInnerMessageType, parseMessageContent } from '~/lib/messageParser'
import { emitSettingsChanged } from '~/lib/settingsChangedEvent'

const log = createLogger('workspace')

export interface WorkspaceConnectionParams {
  agentStore: ReturnType<typeof createAgentStore>
  chatStore: ReturnType<typeof createChatStore>
  terminalStore: ReturnType<typeof createTerminalStore>
  tabStore: ReturnType<typeof createTabStore>
  controlStore: ReturnType<typeof createControlStore>
  agentSessionStore: ReturnType<typeof createAgentSessionStore>
  registry: WorkspaceStoreRegistryType
  settingsLoading: ReturnType<typeof createLoadingSignal>
  getActiveWorkspaceId: () => string | null
  /** Returns the worker ID for the active workspace. */
  getWorkerId: () => string
  /** Called when an agent turn ends (turn completed or control request received). */
  onTurnEnd?: (agentId: string, numTurns?: number) => void
}

export function useWorkspaceConnection(params: WorkspaceConnectionParams) {
  const { agentStore, chatStore, terminalStore, tabStore, controlStore, agentSessionStore, settingsLoading } = params
  const [workerOnline, setWorkerOnline] = createSignal(true)

  // Single unified event stream abort controller.
  let eventStreamAbort: AbortController | null = null
  // Serialized key of the current subscription set to detect changes.
  let currentTargetsKey = ''

  // Set of agent/terminal IDs that belong to non-active workspaces (in registry
  // snapshots). Events for these receive lightweight handling (status/git updates
  // to the snapshot) rather than full chat processing.
  const nonActiveAgentIds = new Set<string>()
  const nonActiveTerminalIds = new Set<string>()

  // Handle an agent event from the unified stream.
  const handleAgentEvent = (
    agentEvent: AgentEvent,
    catchUpPhases: Map<string, 'catchingUp' | 'live'>,
    signal?: AbortSignal,
  ) => {
    const agentId = agentEvent.agentId
    const inner = agentEvent.event

    // Non-active workspace agent — only handle status/git changes,
    // skip full chat processing to avoid routing events to the wrong stores.
    if (nonActiveAgentIds.has(agentId)) {
      if (inner.case === 'statusChange') {
        const sc = inner.value
        // Update the agent's status and git info in the registry snapshot.
        for (const snap of params.registry.all()) {
          const agentIdx = snap.agents.findIndex(a => a.id === agentId)
          if (agentIdx >= 0) {
            if (sc.status !== AgentStatus.UNSPECIFIED) {
              snap.agents[agentIdx] = { ...snap.agents[agentIdx], status: sc.status }
            }
            if (sc.gitStatus) {
              // Update git branch/url on the tab in the snapshot.
              const tabIdx = snap.tabs.tabs.findIndex(t => t.type === TabType.AGENT && t.id === agentId)
              if (tabIdx >= 0) {
                snap.tabs.tabs[tabIdx] = {
                  ...snap.tabs.tabs[tabIdx],
                  gitBranch: sc.gitStatus.branch || undefined,
                  gitOriginUrl: sc.gitStatus.originUrl || undefined,
                }
              }
            }
            params.registry.set(snap.workspaceId, { ...snap })
            break
          }
        }
      }
      return
    }

    // Get or initialize catch-up phase for this agent.
    const catchUpPhase = catchUpPhases.get(agentId) ?? 'live'

    switch (inner.case) {
      case 'agentMessage': {
        const msg = inner.value

        // Intercept ephemeral agent_session_info messages (broadcast by
        // Worker without persisting). These update the agent session store
        // and should not appear in chat history.
        if (msg.role === MessageRole.LEAPMUX) {
          try {
            const parsed = parseMessageContent(msg)
            if (parsed.topLevel === null)
              break

            // Ephemeral (unwrapped) agent_session_info — not persisted, skip addMessage.
            if (!parsed.wrapper && parsed.topLevel.type === 'agent_session_info') {
              const info = parsed.topLevel.info as Record<string, unknown> | undefined
              const updates: Record<string, unknown> = {}
              if (info?.total_cost_usd !== undefined)
                updates.totalCostUsd = info.total_cost_usd
              if (info?.contextUsage !== undefined)
                updates.contextUsage = info.contextUsage
              if (info?.rateLimits !== undefined)
                updates.rateLimits = info.rateLimits as Record<string, unknown>
              if (info?.codexTurnId !== undefined)
                updates.codexTurnId = info.codexTurnId as string
              agentSessionStore.updateInfo(agentId, updates)
              break
            }

            // Persisted LEAPMUX messages — extract metadata, then fall through to addMessage.
            const innerType = getInnerMessageType(parsed)
            if (innerType === 'context_cleared') {
              agentSessionStore.clearContextUsage(agentId)
              chatStore.clearTodos(agentId)
            }
            const rls = extractRateLimitInfo(parsed)
            if (rls.length > 0) {
              const rateLimits: Record<string, Record<string, unknown>> = {}
              for (const rl of rls)
                rateLimits[rl.key] = rl.info
              agentSessionStore.updateInfo(agentId, { rateLimits } as Record<string, unknown>)
            }
            const sc = extractSettingsChanges(parsed)
            if (sc) {
              emitSettingsChanged(sc)
            }
            const planFile = extractPlanFilePath(parsed)
            if (planFile) {
              agentSessionStore.updateInfo(agentId, { planFilePath: planFile })
            }
            const renamedTitle = extractAgentRenamed(parsed)
            if (renamedTitle) {
              tabStore.updateTabTitle(TabType.AGENT, agentId, renamedTitle)
            }
          }
          catch { /* ignore parse errors */ }
        }

        chatStore.addMessage(agentId, msg)
        chatStore.clearStreamingText(agentId)

        // Extract context usage from assistant messages (rehydrates on reconnect).
        if (msg.role === MessageRole.ASSISTANT) {
          try {
            const usage = extractAssistantUsage(parseMessageContent(msg))
            if (usage) {
              agentSessionStore.updateInfo(agentId, usage as Record<string, unknown>)
            }
          }
          catch { /* ignore parse errors */ }
        }

        // Play turn-end sound when a real RESULT (with subtype) arrives.
        // Also extract contextWindow and total_cost_usd (rehydrates on reconnect).
        if (msg.role === MessageRole.RESULT) {
          try {
            const meta = extractResultMetadata(parseMessageContent(msg))
            if (meta) {
              if (meta.subtype && catchUpPhase === 'live')
                params.onTurnEnd?.(agentId, meta.numTurns)
              if (meta.contextWindow !== undefined) {
                const existingUsage = agentSessionStore.getInfo(agentId).contextUsage
                if (existingUsage) {
                  agentSessionStore.updateInfo(agentId, {
                    contextUsage: { ...existingUsage, contextWindow: meta.contextWindow },
                  })
                }
              }
              if (meta.totalCostUsd !== undefined) {
                agentSessionStore.updateInfo(agentId, { totalCostUsd: meta.totalCostUsd })
              }
            }
          }
          catch { /* ignore parse errors */ }
        }
        break
      }
      case 'streamChunk': {
        const text = new TextDecoder().decode(inner.value.delta)
        chatStore.setStreamingText(agentId, (chatStore.state.streamingText[agentId] ?? '') + text)
        break
      }
      case 'streamEnd':
        chatStore.clearStreamingText(agentId)
        if (tabStore.state.activeTabKey !== `agent:${agentId}`) {
          tabStore.setNotification(TabType.AGENT, agentId, true)
        }
        break
      case 'statusChange': {
        const sc = inner.value
        setWorkerOnline(sc.workerOnline)

        // When a settings change is in progress (optimistic update active),
        // don't overwrite the optimistically-set fields — the pending RPC
        // will resolve or revert them.
        const pendingSettings = settingsLoading.loading()
        // Only update status and agentSessionId when status is set
        // (non-UNSPECIFIED). Proto3 defaults unset enums to 0
        // (UNSPECIFIED), so a statusChange carrying only git data
        // would otherwise overwrite valid agent state with defaults,
        // causing the agent to become "unwatchable" and dropping the
        // event stream.
        const hasStatus = sc.status !== AgentStatus.UNSPECIFIED
        agentStore.updateAgent(sc.agentId, {
          ...(hasStatus ? { status: sc.status, agentSessionId: sc.agentSessionId } : {}),
          ...(pendingSettings
            ? {}
            : {
                ...(sc.permissionMode ? { permissionMode: sc.permissionMode } : {}),
                ...(sc.model ? { model: sc.model } : {}),
                ...(sc.effort ? { effort: sc.effort } : {}),
              }),
          gitStatus: sc.gitStatus,
        })
        if (sc.gitStatus) {
          const gs = sc.gitStatus
          tabStore.updateTab(TabType.AGENT, sc.agentId, {
            gitBranch: gs.branch || undefined,
            gitOriginUrl: gs.originUrl || undefined,
          })
        }
        if (!pendingSettings) {
          settingsLoading.stop()
        }
        if (sc.status === AgentStatus.INACTIVE) {
          // Agent is no longer running — clear any stale control requests
          // so the user can send a regular message (which auto-starts the
          // agent) instead of being stuck on an unanswerable prompt.
          controlStore.clearAgent(agentId)
          if (
            catchUpPhase === 'live'
            && sc.agentSessionId
            && agentStore.state.agents.some(a => a.id === agentId)
          ) {
            params.onTurnEnd?.(agentId)
          }
        }
        // The initial statusChange marks the end of the message
        // replay. Fetch remaining messages the server didn't replay
        // (limit of 50 per stream). The phase stays 'catchingUp'
        // until the server sends catch_up_complete.
        if (catchUpPhase === 'catchingUp') {
          const replayEndSeq = chatStore.getLastSeq(agentId)
          if (replayEndSeq > 0n) {
            const wid = agentStore.state.agents.find(a => a.id === agentId)?.workerId ?? ''
            void chatStore.loadNewerMessages(wid, agentId, replayEndSeq, signal)
          }
        }
        break
      }
      case 'controlRequest': {
        const cr = inner.value
        // During catch-up, the INACTIVE statusChange may have already been
        // processed before this replayed controlRequest arrives. Skip adding
        // the request so the user isn't stuck on an unanswerable prompt.
        const agentEntry = agentStore.state.agents.find(a => a.id === cr.agentId)
        if (agentEntry?.status === AgentStatus.INACTIVE)
          break
        const payload = JSON.parse(new TextDecoder().decode(cr.payload))
        controlStore.addRequest(cr.agentId, {
          requestId: cr.requestId,
          agentId: cr.agentId,
          payload,
        })
        if (catchUpPhase === 'live')
          params.onTurnEnd?.(agentId)
        break
      }
      case 'controlCancel': {
        const cc = inner.value
        controlStore.removeRequest(cc.agentId, cc.requestId)
        break
      }
      case 'messageError': {
        const me = inner.value
        if (me.error) {
          chatStore.setMessageError(me.messageId, me.error)
        }
        else {
          chatStore.clearMessageError(me.messageId)
        }
        break
      }
      case 'messageDeleted': {
        const md = inner.value
        chatStore.removeMessage(md.agentId, md.messageId)
        break
      }
      case 'catchUpComplete':
        catchUpPhases.set(agentId, 'live')
        break
    }
  }

  // Handle a terminal event from the unified stream.
  const handleTerminalEvent = (termEvent: TerminalEvent) => {
    const terminalId = termEvent.terminalId

    // Non-active workspace terminal — skip data events (no terminal instance
    // exists), but handle closed events to update the snapshot.
    if (nonActiveTerminalIds.has(terminalId)) {
      if (termEvent.event.case === 'closed') {
        for (const snap of params.registry.all()) {
          const termIdx = snap.terminals.findIndex(t => t.id === terminalId)
          if (termIdx >= 0) {
            snap.terminals[termIdx] = { ...snap.terminals[termIdx], exited: true }
            params.registry.set(snap.workspaceId, { ...snap })
            break
          }
        }
      }
      return
    }

    switch (termEvent.event.case) {
      case 'data': {
        const instance = getTerminalInstance(terminalId)
        if (instance) {
          if (termEvent.event.value.isSnapshot) {
            // Initial screen snapshot from WatchEvents. Only apply if the
            // screen hasn't already been restored (e.g. from the
            // listTerminals snapshot written during component mount).
            if (!instance.screenRestored) {
              instance.suppressInput = true
              instance.terminal.write(termEvent.event.value.data, () => {
                instance!.suppressInput = false
              })
              instance.screenRestored = true
            }
          }
          else {
            instance.terminal.write(termEvent.event.value.data)
          }
        }
        break
      }
      case 'closed':
        terminalStore.markExited(terminalId)
        {
          const instance = getTerminalInstance(terminalId)
          if (instance) {
            instance.terminal.write('\r\n\r\n[Connection to the terminal was lost.]\r\n')
          }
        }
        break
    }
  }

  // Handle from the previous stream iteration. Kept alive during the
  // gap between abort and new stream registration so that the old
  // listener can still receive terminal data (the server-side watcher
  // still routes via the old sender until the new WatchEvents updates it).
  let previousHandle: { close: () => void } | null = null

  // Unified event stream via E2EE channel with retry.
  const watchEvents = async (
    agentEntries: WatchAgentEntry[],
    terminalIds: string[],
    signal: AbortSignal,
  ) => {
    // Load initial messages for active workspace agents only. Non-active
    // workspace agents only receive lightweight status/git updates — they
    // don't need full chat history loaded.
    await Promise.all(
      agentEntries
        .filter(entry => !nonActiveAgentIds.has(entry.agentId))
        .map(async (entry) => {
          try {
            const wid = agentStore.state.agents.find(a => a.id === entry.agentId)?.workerId ?? ''
            await chatStore.loadInitialMessages(wid, entry.agentId)
          }
          catch (err) {
            showWarnToast('Failed to load chat history', err)
          }
        }),
    )

    if (signal.aborted)
      return

    // Per-agent catch-up phase tracking.
    const catchUpPhases = new Map<string, 'catchingUp' | 'live'>()
    for (const entry of agentEntries) {
      catchUpPhases.set(entry.agentId, 'catchingUp')
    }

    let backoff = 1000
    while (!signal.aborted) {
      try {
        // Build entries with current afterSeq values.
        const agents = agentEntries.map(entry => ({
          agentId: entry.agentId,
          afterSeq: untrack(() => chatStore.getLastSeq(entry.agentId)),
        }))

        // Reset catch-up phases on reconnect and clear stale control requests.
        for (const entry of agentEntries) {
          catchUpPhases.set(entry.agentId, 'catchingUp')
          controlStore.clearAgent(entry.agentId)
        }

        const workerId = untrack(() => params.getWorkerId())
        if (!workerId)
          return

        // Open the E2EE channel stream to the Worker.
        const handle = await watchEventsViaChannel(workerId, {
          agents,
          terminalIds,
        })

        // Now that the new stream listener is registered, clean up
        // the previous one. The server-side sender update (Bug 2 fix)
        // ensures no more events will arrive on the old request ID
        // once the server processes this new WatchEvents RPC.
        previousHandle?.close()
        previousHandle = handle

        // Wait for the stream to end or error using a promise.
        await new Promise<void>((resolve, reject) => {
          const onAbort = () => {
            resolve()
          }
          signal.addEventListener('abort', onAbort, { once: true })

          handle.onEvent((response) => {
            backoff = 1000
            switch (response.event.case) {
              case 'agentEvent':
                handleAgentEvent(response.event.value, catchUpPhases, signal)
                break
              case 'terminalEvent':
                handleTerminalEvent(response.event.value)
                break
            }
          })

          handle.onEnd(() => {
            signal.removeEventListener('abort', onAbort)
            resolve()
          })

          handle.onError((err) => {
            signal.removeEventListener('abort', onAbort)
            reject(err)
          })
        })
      }
      catch (err) {
        if (signal.aborted)
          return

        const isConnectionLost = err instanceof ChannelError && err.source === 'transport'

        if (isConnectionLost) {
          showWarnToast('Connection to worker lost, reconnecting\u2026', err)
          // Channel disconnected (worker went offline or restarted).
          // Mark worker as offline so terminals show disconnection and
          // thinking indicators are hidden.
          setWorkerOnline(false)
        }
        else {
          // Stream-level error (e.g. NOT_FOUND for entities not yet
          // visible). Retry quickly without alarming the user.
          log.warn('[watchEvents] stream error, retrying:', err)
          backoff = Math.min(backoff, 500)
        }
      }

      await new Promise(r => setTimeout(r, backoff))
      backoff = Math.min(backoff * 2, 30000)
    }
  }

  // Watch all agents and terminals on the current worker via a single
  // unified WatchEvents stream. When the entity set changes (new agent
  // or terminal created), the effect triggers a stream restart.
  // Also includes agents/terminals from non-active workspace snapshots
  // in the registry, so that status updates are received for all workspaces.
  createEffect(() => {
    const workerId = params.getWorkerId()
    const wsId = params.getActiveWorkspaceId()

    // Collect all agent IDs on this worker.
    const agentEntries: WatchAgentEntry[] = []
    const terminalIds: string[] = []
    nonActiveAgentIds.clear()
    nonActiveTerminalIds.clear()

    if (wsId && workerId) {
      // Active workspace agents/terminals from live stores.
      for (const agent of agentStore.state.agents) {
        if (agent.workerId === workerId) {
          agentEntries.push({ agentId: agent.id, afterSeq: BigInt(0) } as WatchAgentEntry)
        }
      }

      for (const terminal of terminalStore.state.terminals) {
        if (terminal.workerId === workerId) {
          terminalIds.push(terminal.id)
        }
      }

      // Non-active workspace agents/terminals from registry snapshots.
      const activeAgentIds = new Set(agentEntries.map(e => e.agentId))
      const activeTermIds = new Set(terminalIds)

      for (const snap of params.registry.all()) {
        if (snap.workspaceId === wsId)
          continue
        if (!snap.tabsLoaded)
          continue
        for (const agent of snap.agents) {
          if (agent.workerId === workerId && !activeAgentIds.has(agent.id)) {
            agentEntries.push({ agentId: agent.id, afterSeq: BigInt(0) } as WatchAgentEntry)
            nonActiveAgentIds.add(agent.id)
          }
        }
        for (const terminal of snap.terminals) {
          if (terminal.workerId === workerId && !activeTermIds.has(terminal.id)) {
            terminalIds.push(terminal.id)
            nonActiveTerminalIds.add(terminal.id)
          }
        }
      }
    }

    // Build a key representing the current subscription set.
    const agentIds = agentEntries.map(e => e.agentId).sort()
    const sortedTermIds = terminalIds.toSorted()
    const newKey = workerId ? `${workerId}|a:${agentIds.join(',')}|t:${sortedTermIds.join(',')}` : ''

    // Skip if the subscription set hasn't changed.
    if (newKey === currentTargetsKey)
      return

    // Tear down old stream.
    if (eventStreamAbort) {
      eventStreamAbort.abort()
      eventStreamAbort = null
    }
    currentTargetsKey = newKey

    // Start new stream if there's anything to watch.
    if (workerId && (agentEntries.length > 0 || terminalIds.length > 0)) {
      const abort = new AbortController()
      eventStreamAbort = abort
      watchEvents(agentEntries, terminalIds, abort.signal)
    }
  })

  // When the worker goes offline, mark all non-exited terminals as disconnected,
  // clear stale streaming text, and set active agents to inactive so the
  // thinking indicator hides. The real status will arrive when the WatchEvents
  // stream reconnects.
  createEffect(() => {
    if (workerOnline())
      return
    for (const t of terminalStore.state.terminals) {
      if (!terminalStore.isExited(t.id)) {
        terminalStore.markExited(t.id)
        const instance = getTerminalInstance(t.id)
        if (instance) {
          instance.terminal.write('\r\n\r\n[Connection to the terminal was lost.]\r\n')
        }
      }
    }
    for (const a of agentStore.state.agents) {
      chatStore.clearStreamingText(a.id)
      controlStore.clearAgent(a.id)
      if (a.status === AgentStatus.ACTIVE) {
        agentStore.updateAgent(a.id, { status: AgentStatus.INACTIVE })
      }
    }
  })

  // Lazy message loading for agent tabs not on the current worker
  createEffect(() => {
    const activeKey = tabStore.state.activeTabKey
    if (!activeKey)
      return
    const parts = activeKey.split(':')
    if (parts.length !== 2)
      return
    const tabType = Number(parts[0]) as TabType
    if (tabType !== TabType.AGENT)
      return
    const tabId = parts[1]
    if (chatStore.isInitialLoadComplete(tabId))
      return
    // Only load messages for agents in the active workspace's store.
    // Non-active workspace agents exist only in registry snapshots and
    // don't have a workerId in agentStore — attempting to load with an
    // empty workerId causes an "invalid_argument" error.
    const agent = agentStore.state.agents.find(a => a.id === tabId)
    if (!agent)
      return
    chatStore.loadInitialMessages(agent.workerId, tabId).catch((err) => {
      showWarnToast('Failed to load chat history', err)
    })
  })

  // Abort the stream on page unload. SolidJS's onCleanup does
  // not fire on hard browser refresh, so without this the connection stays
  // open as a zombie until the server times it out.
  const abortStream = () => {
    if (eventStreamAbort) {
      eventStreamAbort.abort()
      eventStreamAbort = null
    }
    previousHandle?.close()
    previousHandle = null
  }

  window.addEventListener('beforeunload', abortStream)

  onCleanup(() => {
    window.removeEventListener('beforeunload', abortStream)
    abortStream()
    currentTargetsKey = ''
  })

  return {
    workerOnline,
  }
}
