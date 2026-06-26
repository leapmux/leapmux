import { createStore, produce } from 'solid-js/store'

// ---------------------------------------------------------------------------
// Per-agent store spine
//
// The shared backbone of the simple chat sub-stores: a single
// `{ byAgent: Record<string, T> }` reactive store with get / set / clear over a
// configured empty value. Extracted so chatStreamingText, chatTodoStore, and
// chatPendingOutbound stop each re-spelling the same createStore +
// `byAgent[agentId] ?? empty` accessors; each slice layers its own domain methods
// (todos.replace, pendingOutbound.take, ...) on top. The saved-viewport-scroll
// slice has no domain logic, so the window store uses this spine directly for it.
// chatLiveTail layers its bump/settle/onDelete reconcilers on a `bigint` value.
// Mirrors the chatReconcile / chatMessageOrder leaf extractions: a small,
// independently tested unit the slices compose. NOT for chatCommandStreams, whose
// two-level agentId -> spanId nesting is a fundamentally different shape.
// ---------------------------------------------------------------------------

export interface PerAgentStore<T> {
  /**
   * The value for an agent, or the configured empty value when unset. The empty
   * value is a SHARED reference handed to every unset/cleared agent, so callers
   * MUST treat a `get` result as read-only -- mutating it in place (e.g. `.push`
   * on an array empty) would corrupt the default for every other agent. Every
   * write path replaces the whole leaf (spread into a fresh value), never mutates
   * one. (Object.freeze on the empty would catch a violation but breaks Solid's
   * store proxy, which caches a $PROXY property on the wrapped value.)
   */
  get: (agentId: string) => T
  /** Replace an agent's value. */
  set: (agentId: string, value: T) => void
  /** Reset an agent's value to the configured empty value. */
  clear: (agentId: string) => void
  /**
   * Drop an agent's entry entirely (the key is deleted, not reset to the empty
   * value as `clear` does), so a closed agent leaves no residue in `byAgent`.
   * Unlike `clear`, this also makes a `byAgent[agentId] !== undefined` presence
   * check report the agent as gone. Called from the agent-close cleanup.
   */
  remove: (agentId: string) => void
  /**
   * The raw reactive byAgent record. For slices that need a presence check
   * (`byAgent[agentId] !== undefined`, which `get` hides behind the empty
   * fallback) or a whole-map read; prefer `get` for the common single-value read.
   */
  readonly byAgent: Record<string, T>
}

export function createPerAgentStore<T>(empty: T): PerAgentStore<T> {
  const [state, setState] = createStore<{ byAgent: Record<string, T> }>({ byAgent: {} })
  // Replace the leaf with the value form of the path setter (NOT the updater
  // form -- `(prev) => value` reconciles/merges an object or array leaf into the
  // old one, so a shorter array would keep stale trailing entries). The `as never`
  // selects the value overload for a generic T; the slices' leaf values are never
  // functions, so this is the same direct replace each slice spelled inline.
  const write = (agentId: string, value: T) =>
    setState('byAgent', agentId, value as never)
  return {
    get: agentId => state.byAgent[agentId] ?? empty,
    set: write,
    clear: agentId => write(agentId, empty),
    remove: agentId => setState('byAgent', produce((map) => {
      delete map[agentId]
    })),
    get byAgent() {
      return state.byAgent
    },
  }
}
