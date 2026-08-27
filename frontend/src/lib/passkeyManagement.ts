import type { PasskeyInfo } from '~/generated/leapmux/v1/user_pb'
import { userClient } from '~/api/clients'

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
