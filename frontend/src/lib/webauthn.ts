import {
  startAuthentication as browserStartAuthentication,
  startRegistration as browserStartRegistration,
} from '@simplewebauthn/browser'
import { createLogger } from '~/lib/logger'

const log = createLogger('webauthn')

type RegistrationOptionsJSON = Parameters<typeof browserStartRegistration>[0]['optionsJSON']
type AuthenticationOptionsJSON = Parameters<typeof browserStartAuthentication>[0]['optionsJSON']

/**
 * go-webauthn serializes `CredentialCreation` / `CredentialAssertion` as
 * `{ publicKey: … }`. @simplewebauthn/browser expects the inner options
 * object directly.
 */
export function parseWebAuthnOptionsJSON<T>(optionsJson: string): T {
  const parsed = JSON.parse(optionsJson) as T | { publicKey?: T }
  if (parsed && typeof parsed === 'object' && 'publicKey' in parsed && parsed.publicKey)
    return parsed.publicKey
  return parsed as T
}

/** Begin a WebAuthn registration ceremony from server JSON options. */
export async function startRegistration(optionsJson: string): Promise<string> {
  const optionsJSON = parseWebAuthnOptionsJSON<RegistrationOptionsJSON>(optionsJson)
  const credential = await browserStartRegistration({ optionsJSON })
  return JSON.stringify(credential)
}

/** Begin a WebAuthn authentication ceremony from server JSON options. */
export async function startAuthentication(optionsJson: string): Promise<string> {
  const optionsJSON = parseWebAuthnOptionsJSON<AuthenticationOptionsJSON>(optionsJson)
  const credential = await browserStartAuthentication({ optionsJSON })
  return JSON.stringify(credential)
}

interface SignalUnknownCredential {
  rpId: string
  credentialId: string
}

interface SignalAllAcceptedCredentials {
  rpId: string
  userId: string
  allAcceptedCredentialIds: string[]
}

type PublicKeyCredentialSignal = typeof PublicKeyCredential & {
  signalUnknownCredential?: (options: SignalUnknownCredential) => Promise<void>
  signalAllAcceptedCredentials?: (options: SignalAllAcceptedCredentials) => Promise<void>
}

function publicKeyCredentialSignal(): PublicKeyCredentialSignal | undefined {
  return globalThis.PublicKeyCredential as PublicKeyCredentialSignal | undefined
}

/**
 * Encode a LeapMux user UUID as the WebAuthn Signal API userId.
 * Server user handles are `[]byte(uuid)` — base64url of those UTF-8 bytes.
 */
export function encodeWebAuthnUserId(userId: string): string {
  const bytes = new TextEncoder().encode(userId)
  let binary = ''
  for (let i = 0; i < bytes.length; i++)
    binary += String.fromCharCode(bytes[i]!)
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

/** Tell the browser a passkey was removed (best-effort; no-op when unsupported). */
export function signalPasskeyRemoved(rpId: string, credentialId: string): void {
  if (!credentialId)
    return
  // The Signal API can reject (unknown RP, malformed credential id); it is
  // a best-effort hint, so a rejection is logged and swallowed -- an
  // unhandled rejection would fire right after the success toast.
  publicKeyCredentialSignal()?.signalUnknownCredential?.({ rpId, credentialId }).catch((err) => {
    log.warn('signalUnknownCredential rejected', { error: String(err) })
  })
}

/** Tell the browser which passkeys remain valid for a user (best-effort). */
export function signalAcceptedPasskeys(rpId: string, userId: string, allAcceptedCredentialIds: string[]): void {
  if (allAcceptedCredentialIds.length === 0)
    return
  publicKeyCredentialSignal()?.signalAllAcceptedCredentials?.({
    rpId,
    userId: encodeWebAuthnUserId(userId),
    allAcceptedCredentialIds,
  }).catch((err) => {
    log.warn('signalAllAcceptedCredentials rejected', { error: String(err) })
  })
}
