// ──────────────────────────────────────────────
// ALTCHA solving for API-level test prerequisites
// ──────────────────────────────────────────────

import type { Challenge, DeriveKeyFunction } from 'altcha/lib'
import { Buffer } from 'node:buffer'
import { argon2id, pbkdf2, scrypt, sha, solveChallenge } from 'altcha/lib'

// The solver uses the altcha package's own derive funcs (the same code the
// widget workers run), so it stays correct by construction when the hub
// advertises any family — including the memory-hard ones the previous
// hand-rolled version silently mis-derived.
const deriveKeys: Record<string, DeriveKeyFunction> = {
  'SHA-256': sha.deriveKey,
  'SHA-384': sha.deriveKey,
  'SHA-512': sha.deriveKey,
  'PBKDF2/SHA-256': pbkdf2.deriveKey,
  'PBKDF2/SHA-384': pbkdf2.deriveKey,
  'PBKDF2/SHA-512': pbkdf2.deriveKey,
  'SCRYPT': scrypt.deriveKey,
  'ARGON2ID': argon2id.deriveKey,
}

/**
 * Fetch the next challenge over the raw Connect-API (Node, no browser).
 * Returns null when the hub reports no challenge (captcha disabled).
 */
export async function fetchAltchaChallenge(hubUrl: string): Promise<Challenge | null> {
  const res = await fetch(`${hubUrl}/leapmux.v1.AuthService/GetAltchaChallenge`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({}),
  })
  if (!res.ok) {
    throw new Error(`getAltchaChallenge failed: ${res.status}`)
  }
  const data = await res.json() as { challengeJson: string }
  if (!data.challengeJson) {
    return null
  }
  return JSON.parse(data.challengeJson) as Challenge
}

/**
 * Mirrors the hub-side captcha enforcement for raw Connect-API calls made
 * outside a browser (fixture seeding, loginViaAPI): fetch a challenge and
 * solve it in Node. Returns the request-body fields to attach, or an empty
 * payload when the hub has captcha disabled.
 */
export async function solveCaptchaViaAPI(hubUrl: string): Promise<{ captchaPayload: string, honeypot: string }> {
  const challenge = await fetchAltchaChallenge(hubUrl)
  if (!challenge) {
    return { captchaPayload: '', honeypot: '' }
  }
  const deriveKey = deriveKeys[challenge.parameters.algorithm]
  if (!deriveKey) {
    throw new Error(`no solver registered for algorithm ${challenge.parameters.algorithm}`)
  }
  const solution = await solveChallenge({ challenge, deriveKey })
  if (!solution) {
    throw new Error(`altcha solver gave up on algorithm ${challenge.parameters.algorithm}`)
  }
  const payload = JSON.stringify({
    challenge: {
      parameters: challenge.parameters,
      signature: challenge.signature,
    },
    solution,
  })
  return { captchaPayload: Buffer.from(payload, 'utf8').toString('base64'), honeypot: '' }
}
