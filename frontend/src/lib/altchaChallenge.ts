import { authClient } from '~/api/clients'
import { ensureAltchaSolver } from '~/lib/altchaSolvers'
import { getAltchaAlgorithm } from '~/lib/systemInfo'

// The challenge interchange blob's wire shape (altcha's Challenge). The
// package's type entry does not export it, so the fields are declared here
// to match what the hub marshals and the widget's configure() expects.
export interface AltchaChallenge {
  parameters: {
    algorithm: string
    nonce: string
    salt: string
    cost: number
    keyLength: number
    keyPrefix: string
    expiresAt?: number
    memoryCost?: number
    parallelism?: number
    [key: string]: unknown
  }
  signature?: string
  [key: string]: unknown
}

/**
 * Fetch the next ALTCHA challenge and pre-warm its solver worker.
 *
 * Returns null when the hub reports no challenge (captcha disabled at
 * runtime, solo mode, or an external provider selected — those mint
 * tokens client-side): the hub answers with an empty interchange blob,
 * and the caller must stand down instead of treating it as a load failure
 * — the form would otherwise dead-lock on a challenge that never comes.
 */
export async function fetchAltchaChallenge(): Promise<AltchaChallenge | null> {
  // Pre-warm the solver for the algorithm the hub advertises, so the
  // worker chunk download overlaps the challenge fetch instead of
  // delaying the first click.
  void ensureAltchaSolver(getAltchaAlgorithm())
  const resp = await authClient.getAltchaChallenge({})
  // The empty blob is the hub's "no challenge" answer; JSON.parse("")
  // throws, which would land in the connection-error path.
  if (!resp.challengeJson)
    return null
  const challenge = JSON.parse(resp.challengeJson) as AltchaChallenge
  if (!challenge)
    return null
  // The challenge's own algorithm is authoritative (an admin may have
  // switched since systemInfo loaded); make sure its solver exists before
  // the widget needs it.
  await ensureAltchaSolver(challenge.parameters?.algorithm)
  return challenge
}
