import { createStore, reconcile } from 'solid-js/store'

// ---------------------------------------------------------------------------
// Turn-retry offer slice
//
// The one live retry offer for each agent (keyed agentId -> offer). The
// ephemeral `turn_retry_offer` agent_session_info key supplies it. The offer
// identifies the turn and the faster model that the provider proposes.
//
// Keep this state outside the agent-session store, which persists to browser
// storage. Browser storage can retain an offer after the worker withdraws it.
// On reconnect, the worker replays its current offer or its withdrawal.
// The browser uses that replay to restore the live state.
// ---------------------------------------------------------------------------

/** The retry that the worker offers for one running turn. */
export interface TurnRetryOffer {
  /** The turn that the retry stops and sends again. */
  turnId: string
  /** The faster model that the retry uses. */
  model: string
  /** The help page of the provider for the notice that made the offer, when it has one. */
  learnMoreUrl?: string
}

export function createTurnRetryOfferStore() {
  const [state, setState] = createStore<{ byAgent: Record<string, TurnRetryOffer | undefined> }>({ byAgent: {} })

  return {
    /**
     * Record the live offer of the agent. It replaces the previous offer
     * completely.
     *
     * A plain path set MERGES an object into the existing entry, so a field
     * that the new offer omits keeps the value of the old offer. `reconcile`
     * deletes that field and notifies its readers.
     *
     * The store keeps the object that it receives as its own state, and the
     * next `reconcile` edits that object in place. The copy keeps the object
     * of the caller out of the store, so the store never edits it.
     */
    set(agentId: string, offer: TurnRetryOffer) {
      setState('byAgent', agentId, reconcile({ ...offer }))
    },

    /** The live offer of the agent, or undefined when none exists. */
    get(agentId: string): TurnRetryOffer | undefined {
      return state.byAgent[agentId]
    },

    /**
     * Withdraw the offer of the agent. The worker withdraws it at every turn
     * boundary, so an already-empty entry returns early: the four boundaries
     * that clear this run several times per turn.
     */
    clear(agentId: string) {
      if (!state.byAgent[agentId])
        return
      setState('byAgent', agentId, undefined!)
    },
  }
}
