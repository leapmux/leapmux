/**
 * What this client asks a worker to watch, and how it stops asking.
 *
 * Split out of `useWorkspaceConnection` because none of it touches the
 * connection's state: the subscription request is a pure function of the tabs
 * on screen, and retiring a subscription is a pure function of the channel.
 * Keeping them here makes the subscription key testable without standing up the
 * hook -- which is what the key is FOR, since a wrong key silently either
 * re-dials on every tick or never re-dials at all.
 */
import type { WatchAgentEntry } from '~/generated/leapmux/v1/workspace_pb'
import { channelManager, watchEventsViaChannel } from '~/api/workerRpc'
import { WatchReplayMode } from '~/generated/leapmux/v1/agent_pb'
import { createLogger } from '~/lib/logger'

const log = createLogger('watchTargets')

/**
 * Build a WatchEvents agent entry from a resume cursor. A resume seq of 0n means
 * nothing has been observed yet, so subscribe fresh (LATEST: replay the most
 * recent page); a positive seq resumes AFTER_CURSOR from it. Mirrors the worker
 * and CLI `AgentWatchEntry` mapping so the wire request is explicit -- no 0/sign
 * overload disambiguating fresh from resume.
 */
export function agentWatchEntry(agentId: string, resumeSeq: bigint): WatchAgentEntry {
  return (resumeSeq > 0n
    ? { agentId, replay: WatchReplayMode.AFTER_CURSOR, cursorSeq: resumeSeq }
    : { agentId, replay: WatchReplayMode.LATEST, cursorSeq: BigInt(0) }) as WatchAgentEntry
}

/**
 * Tell the worker this channel is watching nothing.
 *
 * Closing a WatchEvents handle is purely client-local: it removes a stream
 * listener and produces no frame the worker can see. So when the last tab on a
 * worker goes away there is nothing to retire its subscriptions, and the worker
 * keeps marshalling, encrypting and shipping every event for tabs nobody is
 * listening to for the life of the pooled channel.
 *
 * The worker reads a WatchEvents request as the channel's whole current
 * interest, so an empty one IS the unsubscribe.
 *
 * `stillWanted` is re-checked immediately before the send, and that check is
 * the point of the seam. Opening the channel can block behind a handshake, so
 * between deciding to unsubscribe and reaching the wire the user may well have
 * opened a new tab and subscribed. Landing afterwards, this request would then
 * be read as "watching nothing" and wipe the subscription that tab just made --
 * silently, because an empty request is a legitimate one that answers with no
 * error, so nothing would ever re-subscribe.
 */
export async function unsubscribeAllWatchEvents(
  workerId: string,
  stillWanted: () => boolean = () => true,
): Promise<void> {
  try {
    if (!stillWanted())
      return
    // No channel, nothing to retire. Checked before opening one, because
    // a channel that does not exist holds no subscriptions -- opening one
    // (a full Noise_NK + ML-KEM handshake plus a hub round trip) purely
    // to say nothing is wanted is cost with no effect. The watch effect
    // reaches here on ordinary paths that never had a channel: a resolved
    // workerId before tabs hydrate, or a workspace whose only tab on that
    // worker is a FILE tab.
    if (!channelManager.hasOpenChannelForWorker(workerId))
      return
    // Open (in practice, resolve the existing) channel first, then
    // re-check. The open is the part that can block; everything
    // watchEventsViaChannel does after it is synchronous, so re-checking
    // here shrinks the race window from a hub round trip to a microtask.
    await channelManager.getOrOpenChannel(workerId)
    if (!stillWanted())
      return
    const handle = await watchEventsViaChannel(workerId, { agents: [], terminals: [] })
    handle.close()
  }
  catch (err) {
    // A closed channel retires the subscriptions on its own, so that case is
    // genuinely nothing to do. Logged rather than swallowed silently because
    // any OTHER failure leaves the worker shipping events to nobody for the
    // pooled channel's remaining life, and that is invisible from the UI.
    log.debug('unsubscribe-all WatchEvents did not reach the worker', { workerId, error: String(err) })
  }
}

export function buildWatchTargetsKey(
  workerId: string,
  agentEntries: readonly WatchAgentEntry[],
  terminalIds: readonly string[],
  nonActiveAgentIds: ReadonlySet<string>,
  nonActiveTerminalIds: ReadonlySet<string>,
): string {
  if (!workerId)
    return ''
  const activeAgentIds = agentEntries
    .map(e => e.agentId)
    .filter(id => !nonActiveAgentIds.has(id))
    .toSorted()
  const passiveAgentIds = agentEntries
    .map(e => e.agentId)
    .filter(id => nonActiveAgentIds.has(id))
    .toSorted()
  const activeTerminalIds = terminalIds
    .filter(id => !nonActiveTerminalIds.has(id))
    .toSorted()
  const passiveTerminalIds = terminalIds
    .filter(id => nonActiveTerminalIds.has(id))
    .toSorted()
  return `${workerId}|aa:${activeAgentIds.join(',')}|pa:${passiveAgentIds.join(',')}|at:${activeTerminalIds.join(',')}|pt:${passiveTerminalIds.join(',')}`
}
