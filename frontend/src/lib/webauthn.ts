import type { PasskeyBlocker } from '~/lib/systemInfo'
import {
  startAuthentication as browserStartAuthentication,
  startRegistration as browserStartRegistration,
} from '@simplewebauthn/browser'
import { arrayBufferToBase64 } from '~/lib/base64'
import { formatErrorMessage } from '~/lib/errors'
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

/**
 * A ceremony the user dismissed, or that the browser ended without an
 * error the user caused. Callers treat it as a no-op instead of an error
 * banner: dismissing the OS passkey sheet is not a failure to report.
 *
 * `isPasskeyCeremonyCancelled` is the test; the raw SimpleWebAuthn error
 * stays as the `cause` for the log.
 */
export class PasskeyCeremonyCancelledError extends Error {
  constructor(cause: unknown) {
    super('Passkey prompt dismissed', { cause })
    this.name = 'PasskeyCeremonyCancelledError'
  }
}

/** True when a rejected ceremony was a dismissal, not a real failure. */
export function isPasskeyCeremonyCancelled(err: unknown): boolean {
  return err instanceof PasskeyCeremonyCancelledError
}

/**
 * The banner text for a rejected passkey action, or null when the user
 * dismissed the prompt and there is nothing to report. Every surface that
 * runs a ceremony routes its catch through here, so "cancel is not an
 * error" is one rule rather than seven.
 */
export function passkeyErrorMessage(err: unknown, fallback: string): string | null {
  if (isPasskeyCeremonyCancelled(err))
    return null
  return formatErrorMessage(err, fallback)
}

/**
 * Why a passkey surface is unavailable, in one sentence the reader can act
 * on. `passkeyBlocker` decides the state; this states it.
 *
 * ONE text for each blocker, beside `passkeyErrorMessage`, because two
 * surfaces already say it and they must say the same thing: the account
 * panel disables Add passkey, and the step-up form explains a passkey-only
 * account it cannot verify. Each sentence names the party that has to act,
 * because the three remedies go to three different people -- the reader
 * moves to a secure address, the reader changes browser, or an
 * administrator publishes the address.
 */
export function passkeyBlockerMessage(blocker: PasskeyBlocker): string {
  switch (blocker) {
    case 'insecure-context':
      return 'A browser runs a passkey only on a secure page. '
        + 'Open the hub over HTTPS, or at a localhost address.'
    case 'no-webauthn':
      return 'This browser does not support passkeys. Use a browser with WebAuthn support.'
    case 'origin-not-allowed':
      return 'This hub does not run passkey ceremonies at this address. '
        + 'Open the hub through its configured URL, or ask an administrator to publish '
        + 'the address you reach it by.'
  }
}

/**
 * Classify a rejected ceremony at the ONE boundary that knows the
 * SimpleWebAuthn error shape.
 *
 * Every call site used to fall through to `formatErrorMessage`, which
 * returns `err.message` for any `Error` -- and SimpleWebAuthn passes the
 * browser's raw DOMException text through verbatim. A user who simply
 * dismissed the OS sheet read "The operation either timed out or was not
 * allowed. See: https://www.w3.org/TR/webauthn-2/..." in a red banner.
 *
 * The library's own `code` carries the distinction, so the mapping lives
 * here once instead of as seven ad-hoc fallback strings.
 */
function classifyCeremonyError(err: unknown): never {
  const code = (err as { code?: string } | null)?.code
  const causeName = (err as { cause?: { name?: string } } | null)?.cause?.name
  if (code === 'ERROR_CEREMONY_ABORTED'
    || causeName === 'AbortError'
    || causeName === 'NotAllowedError') {
    throw new PasskeyCeremonyCancelledError(err)
  }
  throw err
}

/** Begin a WebAuthn registration ceremony from server JSON options. */
export async function startRegistration(optionsJson: string): Promise<string> {
  const optionsJSON = parseWebAuthnOptionsJSON<RegistrationOptionsJSON>(optionsJson)
  const credential = await browserStartRegistration({ optionsJSON }).catch(classifyCeremonyError)
  return JSON.stringify(credential)
}

/** Begin a WebAuthn authentication ceremony from server JSON options. */
export async function startAuthentication(optionsJson: string): Promise<string> {
  const optionsJSON = parseWebAuthnOptionsJSON<AuthenticationOptionsJSON>(optionsJson)
  const credential = await browserStartAuthentication({ optionsJSON }).catch(classifyCeremonyError)
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
  return arrayBufferToBase64(userId).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
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
