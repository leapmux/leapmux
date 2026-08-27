import type { Timestamp } from '@bufbuild/protobuf/wkt'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import { userClient } from '~/api/clients'
import { startAuthentication } from '~/lib/webauthn'

/**
 * Session elevation ("sudo mode") on the client side.
 *
 * One proven factor admits every sensitive action for a window, and each
 * successful action slides that window forward. So the client does NOT ask
 * the user to verify before every action: it attempts the action, and only
 * prompts when the hub says it must. That ordering is what makes "prompt
 * once, then work" true without the client having to model the window.
 */

/** Whether an elevation deadline is still in the future. */
export function isElevationCurrent(until: Timestamp | undefined, now: Date = new Date()): boolean {
  if (!until)
    return false
  return timestampDate(until).getTime() > now.getTime()
}

/** Prove a password. Resolves with the new deadline. */
export async function elevateWithPassword(currentPassword: string): Promise<Timestamp | undefined> {
  const resp = await userClient.elevateSession({ currentPassword })
  return resp.elevationExpiresAt
}

/**
 * Prove a passkey. The two legs are one operation from the caller's point of
 * view, so they live together: a caller that held only the Begin response
 * would have to know that a browser prompt sits between them.
 */
export async function elevateWithPasskey(): Promise<Timestamp | undefined> {
  const begin = await userClient.beginPasskeyElevation({})
  const credentialJson = await startAuthentication(begin.optionsJson)
  const resp = await userClient.finishPasskeyElevation({
    sessionId: begin.sessionId,
    credentialJson,
  })
  return resp.elevationExpiresAt
}

/** End the elevation immediately. */
export async function dropElevation(): Promise<void> {
  await userClient.dropElevation({})
}

/**
 * The provider start URL for an OAuth re-authentication, carrying the
 * address to return to.
 *
 * An OAuth-only account holds neither a password nor a passkey, so the
 * identity provider is the only thing that can confirm the person is still
 * there. This is a full-document navigation by nature: the provider redirects
 * the browser back to the hub, which redirects it here.
 */
export function oauthReauthUrl(providerId: string, redirect: string): string {
  return `/auth/oauth/${encodeURIComponent(providerId)}/reauth?redirect=${encodeURIComponent(redirect)}`
}
