import type { AgentEvent, TerminalEvent, WatchAgentEntry, WatchTerminalEntry } from '~/generated/leapmux/v1/workspace_pb'
import type { createLoadingSignal } from '~/hooks/createLoadingSignal'
import type { createAgentStore } from '~/stores/agent.store'
import type { createAgentContextStore } from '~/stores/agentContext.store'
import type { createChatStore } from '~/stores/chat.store'
import type { createControlStore } from '~/stores/control.store'
import type { createTabStore } from '~/stores/tab.store'
import type { createTerminalStore } from '~/stores/terminal.store'
import { create } from '@bufbuild/protobuf'
import { createEffect, createMemo, createSignal, onCleanup, untrack } from 'solid-js'
import { getToken } from '~/api/transport'
import { watchEventsViaWebSocket } from '~/api/wsWatchEvents'
import { getTerminalInstance } from '~/components/terminal/TerminalView'
import { AgentStatus, MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { TabType, WatchEventsRequestSchema } from '~/generated/leapmux/v1/workspace_pb'
import { decompressContentToString } from '~/lib/decompress'

export interface WorkspaceConnectionParams {
  agentStore: ReturnType<typeof createAgentStore>
  chatStore: ReturnType<typeof createChatStore>
  terminalStore: ReturnType<typeof createTerminalStore>
  tabStore: ReturnType<typeof createTabStore>
  controlStore: ReturnType<typeof createControlStore>
  agentContextStore: ReturnType<typeof createAgentContextStore>
  settingsLoading: ReturnType<typeof createLoadingSignal>
  getOrgId: () => string
  getActiveWorkspaceId: () => string | null
  /** Returns the set of per-tile active tab keys that need connections. */
  getTileActiveTabKeys?: () => string[]
  /** Called when a turn-end sound should play (turn completed or control request received). */
  onTurnEndSound?: () => void
}

export function useWorkspaceConnection(params: WorkspaceConnectionParams) {
  const { agentStore, chatStore, terminalStore, tabStore, controlStore, agentContextStore, settingsLoading } = params
  const [workerOnline, setWorkerOnline] = createSignal(true)

  // Single unified event stream abort controller.
  let eventStreamAbort: AbortController | null = null
  // Serialized key of the current subscription set to detect changes.
  let currentTargetsKey = ''

  // Handle an agent event from the unified stream.
  const handleAgentEvent = (
    agentEvent: AgentEvent,
    catchUpPhases: Map<string, 'messages' | 'controlRequests' | 'live'>,
  ) => {
    const agentId = agentEvent.agentId
    const inner = agentEvent.event

    // Get or initialize catch-up phase for this agent.
    let catchUpPhase = catchUpPhases.get(agentId) ?? 'live'

    // Transition from 'controlRequests' → 'live' once a
    // non-controlRequest event arrives after the initial statusChange.
    if (catchUpPhase === 'controlRequests' && inner.case !== 'controlRequest') {
      catchUpPhase = 'live'
      catchUpPhases.set(agentId, catchUpPhase)
    }

    switch (inner.case) {
      case 'agentMessage': {
        const msg = inner.value

        // Intercept ephemeral agent_context_info messages (broadcast by
        // Hub without persisting). These update the agent context store
        // and should not appear in chat history.
        if (msg.role === MessageRole.LEAPMUX) {
          try {
            const text = decompressContentToString(msg.content, msg.contentCompression)
            if (text !== null) {
              const parsed = JSON.parse(text)
              // Ephemeral messages are not wrapped — check type directly.
              if (parsed.type === 'agent_context_info') {
                const info = parsed.info as Record<string, unknown> | undefined
                const updates: Record<string, unknown> = {}
                if (info?.total_cost_usd !== undefined)
                  updates.totalCostUsd = info.total_cost_usd
                if (info?.contextUsage !== undefined)
                  updates.contextUsage = info.contextUsage
                agentContextStore.updateInfo(agentId, updates)
                break
              }

              // Clear context usage indicator when context is cleared.
              // Persisted LEAPMUX messages are wrapped in a threadWrapper
              // envelope: {"old_seqs":[],"messages":[{...}]}. Unwrap to
              // get the inner message before checking the type.
              // Falls through — the message still appears in chat history.
              const innerMsg = parsed?.messages?.[0] ?? parsed
              if (innerMsg.type === 'context_cleared') {
                agentContextStore.clearContextUsage(agentId)
                chatStore.clearTodos(agentId)
              }
            }
          }
          catch { /* ignore parse errors */ }
        }

        chatStore.addMessage(agentId, msg)
        chatStore.clearStreamingText(agentId)

        // Play turn-end sound when a real RESULT (with subtype) arrives.
        if (msg.role === MessageRole.RESULT) {
          try {
            const text = decompressContentToString(msg.content, msg.contentCompression)
            if (text !== null) {
              const parsed = JSON.parse(text)
              // Content is wrapped: {"old_seqs":[],"messages":[{...}]}
              const innerMsg = parsed?.messages?.[0] ?? parsed
              if (innerMsg?.subtype) {
                if (catchUpPhase === 'live')
                  params.onTurnEndSound?.()
              }
            }
          }
          catch {
            // Ignore parse errors.
          }
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
        agentStore.updateAgent(sc.agentId, {
          status: sc.status,
          agentSessionId: sc.agentSessionId,
          permissionMode: sc.permissionMode,
          model: sc.model,
          effort: sc.effort,
          gitStatus: sc.gitStatus,
        })
        settingsLoading.stop()
        if (sc.status === AgentStatus.INACTIVE) {
          if (catchUpPhase === 'live' && sc.agentSessionId) {
            params.onTurnEndSound?.()
          }
          if (!sc.agentSessionId) {
            const hasMessages = chatStore.getMessages(sc.agentId).length > 0
            if (!hasMessages) {
              agentStore.removeAgent(sc.agentId)
              tabStore.removeTab(TabType.AGENT, sc.agentId)
            }
          }
        }
        // The initial statusChange marks the end of the message
        // replay; subsequent events are pending controlRequests
        // (still suppressed) or live events.
        catchUpPhases.set(agentId, 'controlRequests')
        break
      }
      case 'controlRequest': {
        const cr = inner.value
        const payload = JSON.parse(new TextDecoder().decode(cr.payload))
        controlStore.addRequest(cr.agentId, {
          requestId: cr.requestId,
          agentId: cr.agentId,
          payload,
        })
        if (catchUpPhase === 'live')
          params.onTurnEndSound?.()
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
    }
  }

  // Handle a terminal event from the unified stream.
  const handleTerminalEvent = (termEvent: TerminalEvent) => {
    const terminalId = termEvent.terminalId
    switch (termEvent.event.case) {
      case 'data': {
        const instance = getTerminalInstance(terminalId)
        if (instance) {
          instance.terminal.write(termEvent.event.value.data)
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

  // Unified event stream with retry.
  const watchEvents = async (
    wsId: string,
    agentEntries: WatchAgentEntry[],
    terminalEntries: WatchTerminalEntry[],
    signal: AbortSignal,
  ) => {
    // Load initial messages for all agents in parallel before starting the stream.
    await Promise.all(
      agentEntries.map(async (entry) => {
        try {
          await chatStore.loadInitialMessages(entry.agentId)
        }
        catch {
          // Ignore — the stream will still deliver history.
        }
      }),
    )

    if (signal.aborted)
      return

    // Per-agent catch-up phase tracking.
    const catchUpPhases = new Map<string, 'messages' | 'controlRequests' | 'live'>()
    for (const entry of agentEntries) {
      catchUpPhases.set(entry.agentId, 'messages')
    }

    let backoff = 1000
    while (!signal.aborted) {
      try {
        // Build entries with current afterSeq values.
        const agents = agentEntries.map(entry => ({
          agentId: entry.agentId,
          afterSeq: untrack(() => chatStore.getLastSeq(entry.agentId)),
        }))
        const terminals = terminalEntries.map(entry => ({
          terminalId: entry.terminalId,
        }))

        // Reset catch-up phases on reconnect and clear stale control requests.
        for (const entry of agentEntries) {
          catchUpPhases.set(entry.agentId, 'messages')
          controlStore.clearAgent(entry.agentId)
        }

        const token = getToken()
        if (!token)
          return

        const request = create(WatchEventsRequestSchema, {
          orgId: untrack(() => params.getOrgId()),
          workspaceId: wsId,
          agents,
          terminals,
        })

        for await (const response of watchEventsViaWebSocket(token, request, { signal })) {
          backoff = 1000
          switch (response.event.case) {
            case 'agentEvent':
              handleAgentEvent(response.event.value, catchUpPhases)
              break
            case 'terminalEvent':
              handleTerminalEvent(response.event.value)
              break
          }
        }
      }
      catch {
        if (signal.aborted)
          return
      }

      await new Promise(r => setTimeout(r, backoff))
      backoff = Math.min(backoff * 2, 30000)
    }
  }

  // Helper: check if a tab key is watchable
  const isWatchable = (key: string): boolean => {
    const parts = key.split(':')
    if (parts.length !== 2)
      return false
    const tabType = Number(parts[0]) as TabType
    const tabId = parts[1]
    if (tabType === TabType.AGENT) {
      const agent = agentStore.state.agents.find(a => a.id === tabId)
      return !!(agent && (agent.status === AgentStatus.ACTIVE || agent.agentSessionId))
    }
    if (tabType === TabType.TERMINAL) {
      return !terminalStore.isExited(tabId)
    }
    return false
  }

  // Determines what the active tab should watch (used for single-tile / mobile)
  const activeWatchTarget = createMemo((): string | null => {
    const activeKey = tabStore.state.activeTabKey
    if (!activeKey)
      return null
    return isWatchable(activeKey) ? activeKey : null
  })

  // Collect all unique watchable targets across tiles
  const allWatchTargets = createMemo((): Set<string> => {
    const targets = new Set<string>()
    // Include the global active tab
    const globalTarget = activeWatchTarget()
    if (globalTarget)
      targets.add(globalTarget)
    // Include per-tile active tabs
    const tileKeys = params.getTileActiveTabKeys?.() ?? []
    for (const key of tileKeys) {
      if (key && isWatchable(key))
        targets.add(key)
    }
    return targets
  })

  // Watch all active targets via a single unified WatchEvents stream.
  // When the target set changes, tear down the old stream and start a new one.
  createEffect(() => {
    const targets = allWatchTargets()
    const wsId = params.getActiveWorkspaceId()

    // Build agent and terminal entries from targets.
    const agentEntries: WatchAgentEntry[] = []
    const terminalEntries: WatchTerminalEntry[] = []

    if (wsId) {
      for (const target of targets) {
        const parts = target.split(':')
        const tabType = Number(parts[0]) as TabType
        const tabId = parts[1]
        if (tabType === TabType.AGENT) {
          agentEntries.push({ agentId: tabId, afterSeq: BigInt(0) } as WatchAgentEntry)
        }
        else if (tabType === TabType.TERMINAL) {
          terminalEntries.push({ terminalId: tabId } as WatchTerminalEntry)
        }
      }

      // For terminal-only tiles, ensure at least one active agent is watched in background
      // (so we get status updates, control requests, etc.)
      if (agentEntries.length === 0) {
        const bgAgent = agentStore.state.agents.find(a => a.status === AgentStatus.ACTIVE || a.agentSessionId)
        if (bgAgent) {
          agentEntries.push({ agentId: bgAgent.id, afterSeq: BigInt(0) } as WatchAgentEntry)
        }
      }
    }

    // Build a key representing the current subscription set.
    const agentIds = agentEntries.map(e => e.agentId).sort()
    const terminalIds = terminalEntries.map(e => e.terminalId).sort()
    const newKey = wsId ? `${wsId}|a:${agentIds.join(',')}|t:${terminalIds.join(',')}` : ''

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
    if (wsId && (agentEntries.length > 0 || terminalEntries.length > 0)) {
      const abort = new AbortController()
      eventStreamAbort = abort
      watchEvents(wsId, agentEntries, terminalEntries, abort.signal)
    }
  })

  // When the worker goes offline, mark all non-exited terminals as disconnected
  // and clear stale streaming text from agents.
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
    }
  })

  // Lazy message loading for non-watchable agent tabs
  createEffect(() => {
    const activeKey = tabStore.state.activeTabKey
    if (!activeKey)
      return
    // Skip if already being watched (via global active or tile watchers)
    if (allWatchTargets().has(activeKey))
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
    chatStore.loadInitialMessages(tabId).catch(() => {})
  })

  // Abort the WebSocket connection on page unload. SolidJS's onCleanup does
  // not fire on hard browser refresh, so without this the WebSocket stays
  // open as a zombie until the server times it out.
  const abortStream = () => {
    if (eventStreamAbort) {
      eventStreamAbort.abort()
      eventStreamAbort = null
    }
  }

  window.addEventListener('beforeunload', abortStream)

  onCleanup(() => {
    window.removeEventListener('beforeunload', abortStream)
    abortStream()
    currentTargetsKey = ''
  })

  return {
    workerOnline,
    activeWatchTarget,
  }
}
