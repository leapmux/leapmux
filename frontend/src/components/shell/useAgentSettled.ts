import type { createActiveClientStore } from '~/lib/presence/activeClient'
import { monotonicNow } from '~/lib/monotonicNow'

/**
 * Builds the debounced handler that drives:
 *   - the ding sound, which only the active client plays,
 *   - the `leapmux:turn-end-played` test hook event.
 *
 * Called when an agent SETTLES -- the Worker's busy -> idle edge -- not when a
 * turn ends. A turn that spawns a subagent ends while the subagent keeps
 * working, and ringing there told the user their agent was done while it was
 * still running. The git-status and directory-tree refresh stays on the turn
 * boundary, because the working tree changed whether or not a subagent is still
 * going; AppShell wires that separately.
 *
 * The active-client gate distinguishes three cases for the broadcast
 * `active_client_id` vs. our hub-reported effective identity:
 *
 *   - active === effective → I am the active client → play.
 *   - active !== '' && active !== effective → someone else is active
 *     → suppress.
 *   - active === '' or effective === '' → degraded (no presence yet or
 *     no clear leader) → play; better a brief double-ding under a rare
 *     multi-client tie than silently swallowing a turn-end for a
 *     focused user.
 *
 * A SUBAGENT tab rings nothing. Its settle is one step of the parent's work,
 * and the parent settles on its own once the whole piece of work is done --
 * which is the settle the user waits for. The tab still shows its dot, which
 * says "this transcript moved" without interrupting anybody, and
 * handleAgentSettled owns that one layer up.
 *
 * `isSubagent` answers `undefined` while this client does not know the lineage
 * yet -- a tab restored from the CRDT carries no parent link until the
 * listAgents reply lands. That case RINGS, because a swallowed completion is
 * worse than a spurious one, but it does not SPEND the cooldown. Spending it on
 * a settle that turns out to be a child's would silence the root's own settle
 * for the next minute, which is exactly the failure the subagent rule exists to
 * prevent.
 *
 * `isAgentClosing` is late-bound (the caller initializes it after
 * useTabOperations is constructed); the returned handler reads it on
 * every invocation, so a getter-style binding is fine.
 *
 * `turnEndAudio` is lazily mounted on the first construction in the
 * module — every workspace switch reuses the same Audio element.
 */
export interface UseAgentSettledOpts {
  preferences: {
    turnEndSound: () => string
    turnEndSoundVolume: () => number
  }
  activeClient: ReturnType<typeof createActiveClientStore>
  effectiveClientId: () => string
  getActiveWorkspaceId: () => string | null | undefined
  ownClientId: () => string
  isAgentClosing: (agentId: string) => boolean
  /**
   * Whether this agent is a subagent transcript, or `undefined` while the
   * lineage is not known yet. See isSubagentTab.
   */
  isSubagent: (agentId: string) => boolean | undefined
}

const TURN_END_SOUND_COOLDOWN_MS = 60_000

let turnEndAudio: HTMLAudioElement | undefined

export function useAgentSettled(opts: UseAgentSettledOpts): (agentId: string, numToolUses?: number) => void {
  if (!turnEndAudio)
    turnEndAudio = new Audio('/sounds/benkirb-electronic-doorbell-262895.mp3')

  // undefined = never played. A numeric zero would suppress the first ding
  // for ~60s after page load because performance.now() starts near 0.
  let lastSoundPlayedAt: number | undefined

  return (agentId: string, numToolUses?: number) => {
    if (opts.isAgentClosing(agentId))
      return
    // A subagent settles for its own step of the parent's work. Before the
    // cooldown, deliberately: a child that spent it would silence the ROOT's
    // settle that follows within the same minute, which is the one the user
    // waits for.
    const subagent = opts.isSubagent(agentId)
    if (subagent === true)
      return
    // Skip the audible notification for a trivial single-exchange turn.
    // UNDEFINED is not zero here: a settle that no turn end caused (a permission
    // prompt, a process exit) carries no count and must still ring, as must a
    // provider that cannot report one.
    if (numToolUses !== undefined && numToolUses === 0)
      return
    const wsId = opts.getActiveWorkspaceId() ?? ''
    if (wsId) {
      const active = opts.activeClient.activeFor(wsId)
      const effective = opts.effectiveClientId()
      if (active !== '' && effective !== '' && active !== effective)
        return
    }
    const now = monotonicNow()
    if (lastSoundPlayedAt !== undefined && now - lastSoundPlayedAt < TURN_END_SOUND_COOLDOWN_MS)
      return
    const sound = opts.preferences.turnEndSound()
    if (sound === 'ding-dong') {
      // Only a KNOWN root spends the cooldown. `undefined` here is a tab whose
      // lineage has not arrived; it can still turn out to be a child, and a
      // child must never take the minute the root's own settle needs.
      if (subagent === false)
        lastSoundPlayedAt = now
      turnEndAudio!.currentTime = 0
      turnEndAudio!.volume = opts.preferences.turnEndSoundVolume() / 100
      turnEndAudio!.play().catch(() => {})
      // Test hook: dispatched whenever the active-client gate actually
      // fires the ding. The E2E 151 spec listens for this event to
      // assert that ONLY the focused client plays the sound under
      // multi-context tests where the audio stack is either suppressed
      // or unobservable (jsdom-like envs).
      window.dispatchEvent(new CustomEvent('leapmux:turn-end-played', {
        detail: { agentId, ownClientId: opts.ownClientId(), effectiveClientId: opts.effectiveClientId() },
      }))
    }
  }
}
