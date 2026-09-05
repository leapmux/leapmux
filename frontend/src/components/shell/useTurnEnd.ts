import type { createActiveClientStore } from '~/lib/presence/activeClient'
import { monotonicNow } from '~/lib/monotonicNow'

/**
 * Builds the debounced handler that drives:
 *   - the active-client-gated ding sound,
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
 * `isAgentClosing` is late-bound (the caller initializes it after
 * useTabOperations is constructed); the returned handler reads it on
 * every invocation, so a getter-style binding is fine.
 *
 * `turnEndAudio` is lazily mounted on the first construction in the
 * module — every workspace switch reuses the same Audio element.
 */
export interface UseTurnEndOpts {
  preferences: {
    turnEndSound: () => string
    turnEndSoundVolume: () => number
  }
  activeClient: ReturnType<typeof createActiveClientStore>
  effectiveClientId: () => string
  getActiveWorkspaceId: () => string | null | undefined
  ownClientId: () => string
  isAgentClosing: (agentId: string) => boolean
}

const TURN_END_SOUND_COOLDOWN_MS = 60_000

let turnEndAudio: HTMLAudioElement | undefined

export function useTurnEnd(opts: UseTurnEndOpts): (agentId: string, numToolUses?: number) => void {
  if (!turnEndAudio)
    turnEndAudio = new Audio('/sounds/benkirb-electronic-doorbell-262895.mp3')

  // undefined = never played. A numeric zero would suppress the first ding
  // for ~60s after page load because performance.now() starts near 0.
  let lastSoundPlayedAt: number | undefined

  return (agentId: string, numToolUses?: number) => {
    if (opts.isAgentClosing(agentId))
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
