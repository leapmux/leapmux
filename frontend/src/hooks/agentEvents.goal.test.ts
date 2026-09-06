/// <reference types="vitest/globals" />
import { describe, expect, it } from 'vitest'
import { wireGoalProgressToUpdate } from '~/hooks/agentEvents'

/**
 * The volatile half of the session goal, read off the ephemeral session-info
 * channel.
 *
 * It rides that channel rather than the goal event because Codex advances these
 * counters after every completed tool call -- so the goal event fires only on a
 * real transition, and a 200-tool turn costs no database writes.
 */
describe('wireGoalProgressToUpdate', () => {
  it('reads every counter a provider sent', () => {
    expect(wireGoalProgressToUpdate({
      tokens_used: 1200,
      token_budget: 50000,
      time_used_seconds: 90,
      iterations: 4,
    })).toEqual({ tokensUsed: 1200, tokenBudget: 50000, timeUsedSeconds: 90, iterations: 4 })
  })

  /**
   * Absent and zero are different answers, and no two providers report the same
   * set: Codex sends tokens and seconds but no iteration count, ZCode sends
   * seconds and an iteration but no tokens, Claude Code only an iteration
   * count. A field the provider never mentioned must stay OFF the update, or
   * the card renders "0 tokens" -- a number nobody gave.
   */
  it('omits a counter the provider did not send', () => {
    expect(wireGoalProgressToUpdate({ tokens_used: 10, time_used_seconds: 2 }))
      .toEqual({ tokensUsed: 10, timeUsedSeconds: 2 })
  })

  // A zero the provider DID send is real: a goal that just started has used no
  // tokens, and that is worth showing.
  it('keeps a zero the provider actually reported', () => {
    expect(wireGoalProgressToUpdate({ tokens_used: 0 })).toEqual({ tokensUsed: 0 })
  })

  // A NaN reaches the duration formatter and renders as "NaN"; a negative count
  // is not a count. Same guard the running-tool elapsed time carries.
  it('drops a value that is not a finite, non-negative number', () => {
    expect(wireGoalProgressToUpdate({ tokens_used: Number.NaN, iterations: -3 })).toBeUndefined()
    expect(wireGoalProgressToUpdate({ tokens_used: 'lots' })).toBeUndefined()
  })

  it('reports nothing for a payload with no counters at all', () => {
    expect(wireGoalProgressToUpdate({})).toBeUndefined()
    expect(wireGoalProgressToUpdate(undefined)).toBeUndefined()
    expect(wireGoalProgressToUpdate('not an object')).toBeUndefined()
  })
})
