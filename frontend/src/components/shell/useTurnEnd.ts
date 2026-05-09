import type { createActiveClientStore } from '~/lib/presence/activeClient'

/**
 * Builds the debounced turn-end handler that drives:
 *   - turnEndTrigger bump (downstream: git status + directory tree refresh),
 *   - the active-client-gated ding sound,
 *   - the `leapmux:turn-end-played` test hook event.
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
  ownClientId: string
  setTurnEndTrigger: (updater: (v: number) => number) => void
  isAgentClosing: (agentId: string) => boolean
}

const TURN_END_SOUND_COOLDOWN_MS = 60_000

let turnEndAudio: HTMLAudioElement | undefined

export function useTurnEnd(opts: UseTurnEndOpts): (agentId: string, numToolUses?: number) => void {
  if (!turnEndAudio)
    turnEndAudio = new Audio('/sounds/benkirb-electronic-doorbell-262895.mp3')

  let lastSoundPlayedAt = 0

  return (agentId: string, numToolUses?: number) => {
    if (opts.isAgentClosing(agentId))
      return
    // Always bump the trigger (drives git status and directory tree
    // refresh), but skip the audible notification for trivial
    // single-exchange turns.
    opts.setTurnEndTrigger(v => v + 1)
    if (numToolUses !== undefined && numToolUses === 0)
      return
    const wsId = opts.getActiveWorkspaceId() ?? ''
    if (wsId) {
      const active = opts.activeClient.activeFor(wsId)
      const effective = opts.effectiveClientId()
      if (active !== '' && effective !== '' && active !== effective)
        return
    }
    const now = Date.now()
    if (now - lastSoundPlayedAt < TURN_END_SOUND_COOLDOWN_MS)
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
        detail: { agentId, ownClientId: opts.ownClientId, effectiveClientId: opts.effectiveClientId() },
      }))
    }
  }
}
