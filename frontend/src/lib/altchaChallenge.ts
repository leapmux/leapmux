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
 * runtime, or solo mode): the hub answers with an empty interchange blob,
 * and the caller must stand down instead of treating it as a load failure
 * — the form would otherwise dead-lock on a challenge that never comes.
 * Another provider selected is NOT a stand-down: the hub rejects the call
 * (FailedPrecondition), which surfaces as the field's load error until
 * the denial-driven system-info reload mounts the right provider's field.
 */
export async function fetchAltchaChallenge(): Promise<AltchaChallenge | null> {
  // Pre-warm the solver for the algorithm the hub advertises, so the
  // worker chunk download overlaps the challenge fetch instead of
  // delaying the first click. Best-effort: a failed worker-chunk fetch
  // rejects here, and the re-await below re-awaits the same call inside
  // the field's error path -- without this catch the pre-warm's
  // rejection would surface as an unhandledrejection on every fetch.
  void ensureAltchaSolver(getAltchaAlgorithm()).catch(() => {})
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
