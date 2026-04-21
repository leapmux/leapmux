/**
 * Glue between the per-agent outbound queue (chat.store) and the
 * TileRenderer-scoped flush/fail handlers used by useWorkspaceConnection
 * to drain on STARTING → ACTIVE / mark failed on STARTING → STARTUP_FAILED.
 *
 * The queue itself lives in `chat.store.pendingOutboundMessages` so it
 * can be consumed by the same chatStore that owns the optimistic
 * bubbles and per-message labels. This module only owns the side-effect
 * callbacks needed to bridge the status-change observer to the actual
 * RPC dispatch (which depends on the tile's worker context).
 */

import type { PendingOutboundMessage } from './chat.store'

export type { PendingOutboundMessage as PendingMessage } from './chat.store'

type PendingHandler = (msgs: PendingOutboundMessage[]) => void | Promise<void>

const flushHandlers = new Map<string, PendingHandler>()
const failHandlers = new Map<string, PendingHandler>()

/**
 * Register flush + fail callbacks for an agent. TileRenderer wires these
 * at mount; useWorkspaceConnection invokes them on status transitions.
 * Handlers are replaced (not stacked) — there is only ever one active
 * renderer per agent.
 */
export function setPendingHandlers(
  agentId: string,
  flush: PendingHandler,
  fail: PendingHandler,
): void {
  flushHandlers.set(agentId, flush)
  failHandlers.set(agentId, fail)
}

/** Drop registrations when the agent tab unmounts. */
export function clearPendingHandlers(agentId: string): void {
  flushHandlers.delete(agentId)
  failHandlers.delete(agentId)
}

/** Lookup helpers used by useWorkspaceConnection's status-change handler. */
export function getFlushHandler(agentId: string): PendingHandler | undefined {
  return flushHandlers.get(agentId)
}

export function getFailHandler(agentId: string): PendingHandler | undefined {
  return failHandlers.get(agentId)
}

/** Test helper. */
export function resetPendingMessages(): void {
  flushHandlers.clear()
  failHandlers.clear()
}
