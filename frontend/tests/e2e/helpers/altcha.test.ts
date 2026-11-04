import type { Challenge, Solution } from 'altcha/lib'
import { Buffer } from 'node:buffer'
import { fromJson } from '@bufbuild/protobuf'
import { createChallenge, sha, verifySolution } from 'altcha/lib'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { CaptchaProofSchema } from '../../../src/generated/proto/leapmux/v1/auth_pb'
import { solveCaptchaViaAPI } from './altcha'

const hubUrl = 'http://altcha-hub.test'
const secret = 'altcha-unit-secret'

/** Answer GetAltchaChallenge with `challengeJson`, as the hub does. An empty value means that captcha is disabled. */
function hubChallenge(challengeJson: string) {
  const fetch = vi.fn(async () => Response.json({ challengeJson }))
  vi.stubGlobal('fetch', fetch)
  return fetch
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('solveCaptchaViaAPI', () => {
  it('returns an empty captcha proof when the hub has captcha disabled', async () => {
    const fetch = hubChallenge('')
    const proof = await solveCaptchaViaAPI(hubUrl)
    expect(proof).toEqual({ payload: '', honeypot: '' })
    // The decode refuses an unknown field, so the proof matches the `captcha` field of each request.
    expect(() => fromJson(CaptchaProofSchema, proof)).not.toThrow()
    expect(fetch).toHaveBeenCalledExactlyOnceWith(`${hubUrl}/leapmux.v1.AuthService/GetAltchaChallenge`, expect.objectContaining({ method: 'POST' }))
  })

  it('returns a captcha proof whose payload solves the challenge of the hub', async () => {
    const challenge = await createChallenge({
      algorithm: 'SHA-256',
      cost: 1,
      counter: 5,
      deriveKey: sha.deriveKey,
      hmacSignatureSecret: secret,
    })
    hubChallenge(JSON.stringify(challenge))
    const proof = await solveCaptchaViaAPI(hubUrl)
    expect(() => fromJson(CaptchaProofSchema, proof)).not.toThrow()
    expect(proof.honeypot).toBe('')
    const submitted = JSON.parse(Buffer.from(proof.payload, 'base64').toString('utf8')) as { challenge: Challenge, solution: Solution }
    expect(submitted.challenge).toEqual({ parameters: challenge.parameters, signature: challenge.signature })
    await expect(verifySolution({
      challenge: submitted.challenge,
      deriveKey: sha.deriveKey,
      hmacSignatureSecret: secret,
      solution: submitted.solution,
    })).resolves.toMatchObject({ verified: true })
  })
})
