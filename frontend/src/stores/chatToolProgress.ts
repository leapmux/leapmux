import { createStore } from 'solid-js/store'

// ---------------------------------------------------------------------------
// Tool-progress slice
//
// Live per-span progress for a tool that is still executing (keyed
// agentId -> spanId -> entry), fed by the ephemeral `running_tool`
// agent_session_info key. A self-contained sub-store, shaped like the
// command-stream slice beside it.
//
// The worker sends only UPDATES, never an end message: it cannot observe the
// boundaries at which a card should stop showing progress (a result row landing,
// a turn ending, an interrupt), and the frontend can. So every removal is
// driven from here -- see the drop/clear callers in hooks/agentEvents.ts.
//
// Each update is MERGED into the span's entry rather than replacing it, because
// the two families the worker forwards report disjoint facts about the same
// tool. A heartbeat carries the elapsed time and says nothing about a retry; a
// subagent-retry frame carries the retry state and reports its elapsed time as
// a hardcoded 0, which must not rewind the clock the heartbeats maintain. A
// replace would let each family erase the other's field twice a minute.
// ---------------------------------------------------------------------------

/** The retry state of a Task subagent that is retrying an API call. */
export interface ToolProgressRetry {
  attempt: number
  maxRetries: number
  retryDelayMs: number
  /** null when the failure carried no HTTP status (a connection error). */
  errorStatus: number | null
  errorCategory: string
}

/** What one still-running tool reports about itself. */
export interface ToolProgressEntry {
  toolName: string
  /**
   * Seconds the tool has run, as the AGENT last reported it. Absent until the
   * first heartbeat. Claude Code ticks every 30 seconds, so this steps 30, 60,
   * 90 -- it is not a live clock, and nothing here interpolates between ticks.
   */
  elapsedSeconds?: number
  subagentType?: string
  retry?: ToolProgressRetry
}

/**
 * One `running_tool` broadcast, already translated from the wire. `spanId`
 * addresses the tool_use row; every other field is optional because a family
 * reports only what it knows.
 *
 * `retry` distinguishes three states on purpose: absent leaves the entry's
 * retry alone (a heartbeat), an object sets it, and `null` clears it (the CLI's
 * resolved-retry signal).
 */
export interface ToolProgressUpdate {
  spanId: string
  toolName?: string
  elapsedSeconds?: number
  subagentType?: string
  retry?: ToolProgressRetry | null
}

export function createToolProgressStore() {
  const [state, setState] = createStore<{
    byAgent: Record<string, Record<string, ToolProgressEntry>>
  }>({ byAgent: {} })

  // Vivify the per-agent record before a nested path-set: the per-span setter
  // keeps each span's reactivity isolated but cannot navigate into an undefined
  // agent entry on that agent's first update. Mirror of dropAgentIfEmpty.
  const ensureAgentRecord = (agentId: string) => {
    if (!state.byAgent[agentId])
      setState('byAgent', agentId, {})
  }
  // Remove a now-empty parent record so a long session does not accumulate one
  // empty `{}` per agent that ever ran a tool. Readers coalesce a missing parent
  // to undefined, so this is invisible to them.
  const dropAgentIfEmpty = (agentId: string) => {
    const parent = state.byAgent[agentId]
    if (parent && Object.keys(parent).length === 0)
      setState('byAgent', agentId, undefined!)
  }

  return {
    /**
     * Merge one update into its span's entry. See the merge rationale at the top
     * of this file: a field the update omits keeps the value the previous update
     * left, so the two families do not erase each other.
     */
    apply(agentId: string, update: ToolProgressUpdate) {
      if (!update.spanId)
        return
      ensureAgentRecord(agentId)
      // A function returning a plain object MERGES into the existing entry, so
      // returning only the fields this update carries is what keeps the other
      // fields. An omitted key is left alone by construction; there is nothing
      // to copy forward by hand.
      setState('byAgent', agentId, update.spanId, (prev): Partial<ToolProgressEntry> => {
        const next: Partial<ToolProgressEntry> = {}
        // Every entry needs a name, so the first update seeds one even when the
        // agent omitted it -- the badge must never render "undefined".
        if (update.toolName !== undefined || prev?.toolName === undefined)
          next.toolName = update.toolName ?? ''
        if (update.elapsedSeconds !== undefined)
          next.elapsedSeconds = update.elapsedSeconds
        if (update.subagentType !== undefined)
          next.subagentType = update.subagentType
        if (update.retry != null)
          next.retry = update.retry
        return next
      })
      // The retry clear is a SEPARATE path set, not a `retry: undefined` in the
      // merge above: the merge would have to distinguish "omitted" from
      // "explicitly undefined", and a store proxy does not drop an omitted key.
      // This is the same explicit-undefined idiom clearThinkingTokens uses.
      if (update.retry === null)
        setState('byAgent', agentId, update.spanId, 'retry', undefined!)
    },

    /** One span's live progress, or undefined when the tool is not running. */
    get(agentId: string, spanId: string): ToolProgressEntry | undefined {
      if (!spanId)
        return undefined
      return state.byAgent[agentId]?.[spanId]
    },

    /** Drop one span's entry -- its tool finished. */
    drop(agentId: string, spanId: string) {
      if (!spanId || !state.byAgent[agentId]?.[spanId])
        return
      setState('byAgent', agentId, spanId, undefined!)
      dropAgentIfEmpty(agentId)
    },

    /**
     * Drop every entry for an agent. The backstop for a tool whose result row
     * never arrives -- an interrupt, a crash, a context clear -- which is why the
     * turn/agent boundaries call it rather than relying on per-span drops alone.
     */
    clearAgent(agentId: string) {
      if (!state.byAgent[agentId])
        return
      setState('byAgent', agentId, undefined!)
    },
  }
}
