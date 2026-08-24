import type { PasskeyInfo } from '~/generated/leapmux/v1/user_pb'
import { Code, ConnectError } from '@connectrpc/connect'
import { userClient } from '~/api/clients'
import { startAuthentication } from '~/lib/webauthn'

export async function obtainPasskeyReauthProof(): Promise<string> {
  const begin = await userClient.beginPasskeyReauth({})
  const credentialJson = await startAuthentication(begin.optionsJson)
  const finish = await userClient.finishPasskeyReauth({
    sessionId: begin.sessionId,
    credentialJson,
  })
  return finish.reauthProof
}

/**
 * A step-up rejection answers Unauthenticated. Reauth proofs are
 * single-use: the failed mutation consumed it (or it expired), so the
 * caller must drop the cached proof or every retry resends the same dead
 * one and fails identically. Classifying on the connect code survives any
 * message rewording; a wrong-password Unauthenticated on a password
 * account carries no proof to drop, so the same classification is safe
 * there.
 */
export function isReauthProofRejected(err: unknown): boolean {
  return err instanceof ConnectError && err.code === Code.Unauthenticated
}

export function credentialIdFromRegistrationJson(credentialJson: string): string | undefined {
  try {
    const parsed = JSON.parse(credentialJson) as { id?: string }
    return typeof parsed.id === 'string' ? parsed.id : undefined
  }
  catch {
    return undefined
  }
}

/**
 * The passkey list plus the hub's relying party ID. The RP ID comes from the
 * server (it derives it from the hub's configured origins), so the Signal
 * API calls never mirror that derivation in TypeScript.
 */
export interface PasskeyList {
  passkeys: PasskeyInfo[]
  rpId: string
}

export async function loadPasskeys(): Promise<PasskeyList> {
  const resp = await userClient.listPasskeys({})
  return { passkeys: resp.passkeys, rpId: resp.rpId }
}
