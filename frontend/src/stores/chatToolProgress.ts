import type { MessageSpanIdentity } from '~/lib/messageSpan'
import { createStore } from 'solid-js/store'
import { messageSpanKey } from '~/lib/messageSpan'

// ---------------------------------------------------------------------------
// Tool-progress slice
//
// Live per-span progress for a tool that still runs (keyed
// agentId -> messageSpanKey -> entry), fed by the ephemeral `running_tool`
// agent_session_info key.
//
// The key is the SPAN IDENTITY -- the provider session plus the span id -- and
// not the span id alone, because a span id is unique inside one provider
// session and not across every session of one agent. The worker states the
// session on the payload for that reason. The three paths that reach an entry
// derive the key the same way: `apply` keys the update, `drop` keys the result
// row, and `get` keys the row that renders the badge. A path that keyed by span
// alone would read another session's entry.
//
// An entry holds ONLY what the badge renders. A field that no component reads
// does not belong here: the wire carries it, the store merges it, and nothing
// can observe it, which is the drift contracts/session-info.json's own admission
// rule exists to stop. The tool's name and a subagent's type both already reach
// the card from the tool_use ROW, so neither is on the entry.
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

/** The retry state of a Task subagent that retries an API call. */
export interface ToolProgressRetry {
  attempt: number
  maxRetries: number
  retryDelayMs: number
  /** null when the failure carried no HTTP status. */
  errorStatus: number | null
  errorCategory: string
}

/** What one still-running tool reports about itself. */
export interface ToolProgressEntry {
  /**
   * How long the tool ran, in seconds, as the AGENT last reported it. Absent
   * until the first heartbeat. Claude Code ticks every 30 seconds, so this steps
   * 30, 60, 90 -- it is not a live clock, and nothing here interpolates between
   * the ticks.
   *
   * Only Claude reports it, and only for its main agent. Replacing it with a
   * start timestamp the browser counts from would give every provider a badge:
   * https://github.com/leapmux/leapmux/issues/439
   */
  elapsedSeconds?: number
  retry?: ToolProgressRetry
}

/**
 * One `running_tool` broadcast, already translated from the wire. `spanId` and
 * `agentSessionId` together address the tool_use row; every other field is
 * optional because a family reports only what it knows.
 *
 * `retry` distinguishes three states on purpose: absent leaves the entry's
 * retry alone (a heartbeat), an object sets it, and `null` clears it (the CLI's
 * resolved-retry signal).
 */
export interface ToolProgressUpdate extends MessageSpanIdentity {
  elapsedSeconds?: number
  retry?: ToolProgressRetry | null
}

export function createToolProgressStore() {
  const [state, setState] = createStore<{
    byAgent: Record<string, Record<string, ToolProgressEntry>>
  }>({ byAgent: {} })

  // Vivify the per-agent record before a nested path-set: the per-span setter
  // keeps each span's reactivity isolated but cannot navigate into an undefined
  // agent entry on that agent's first update.
  //
  // The record is NOT removed again when its last span drops. A Solid store
  // subscribes a reader to every path segment it reads, so every mounted badge
  // holds a subscription on `byAgent[agentId]`, and a delete-then-recreate wakes
  // all of them. The main agent runs one tool at a time, so a collapse here
  // would fire that wake twice for every long tool of the turn, to reclaim one
  // empty object per agent. clearAgent already reclaims it at each turn
  // boundary.
  const ensureAgentRecord = (agentId: string) => {
    if (!state.byAgent[agentId])
      setState('byAgent', agentId, {})
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
      const key = messageSpanKey(update)
      ensureAgentRecord(agentId)
      // A function returning a plain object MERGES into the existing entry, so
      // returning only the fields this update carries is what keeps the other
      // fields. An omitted key is left alone by construction; there is nothing
      // to copy forward by hand.
      setState('byAgent', agentId, key, (): Partial<ToolProgressEntry> => {
        const next: Partial<ToolProgressEntry> = {}
        if (update.elapsedSeconds !== undefined)
          next.elapsedSeconds = update.elapsedSeconds
        if (update.retry != null)
          next.retry = update.retry
        return next
      })
      // The retry clear is a SEPARATE path set, not a `retry: undefined` in the
      // merge above: the merge would have to distinguish "omitted" from
      // "explicitly undefined", and a store proxy does not drop an omitted key.
      // This is the same explicit-undefined idiom clearThinkingTokens uses.
      if (update.retry === null)
        setState('byAgent', agentId, key, 'retry', undefined!)
    },

    /** One span's live progress, or undefined when the tool is not running. */
    get(agentId: string, identity: MessageSpanIdentity): ToolProgressEntry | undefined {
      if (!identity.spanId)
        return undefined
      return state.byAgent[agentId]?.[messageSpanKey(identity)]
    },

    /** Drop one span's entry -- its tool finished. */
    drop(agentId: string, identity: MessageSpanIdentity) {
      if (!identity.spanId)
        return
      const key = messageSpanKey(identity)
      if (!state.byAgent[agentId]?.[key])
        return
      setState('byAgent', agentId, key, undefined!)
    },

    /**
     * Drop every entry for an agent. The backstop for a tool whose result row
     * never arrives -- an interrupt, a crash, a context clear -- which is why the
     * turn boundaries and the agent boundaries call it rather than relying on
     * per-span drops alone.
     *
     * An ALREADY-EMPTY record returns early. Deleting it would wake every badge
     * subscribed through `byAgent[agentId]` for nothing, and the four boundaries
     * that call this run several times per turn. See ensureAgentRecord.
     */
    clearAgent(agentId: string) {
      const parent = state.byAgent[agentId]
      if (!parent || Object.keys(parent).length === 0)
        return
      setState('byAgent', agentId, undefined!)
    },
  }
}
