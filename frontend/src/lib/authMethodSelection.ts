import type { Accessor } from 'solid-js'
import type { PillOptionSpec } from '~/components/common/PillGroup'

import { createSignal } from 'solid-js'
import { passkeyBlocker } from '~/lib/systemInfo'
import { passkeyBlockerMessage } from '~/lib/webauthn'

/** The two credential kinds the login and sign-up forms offer. */
export type AuthMethod = 'password' | 'passkey'

/** Which form the selection belongs to. Only the captcha action differs. */
export type AuthMethodKind = 'login' | 'signup'

/**
 * The captcha action bound into an external provider's token. The hub
 * refuses a token minted under a different action, so this must follow the
 * EFFECTIVE method, never the raw selection.
 */
export type AuthCaptchaAction = 'login' | 'signup' | 'passkey_login' | 'passkey_signup'

export interface AuthMethodSelection {
  /**
   * The method the form must actually act on. `passkeysUsableHere()` can
   * flip false at runtime — a captcha refusal re-fetches system info, an
   * admin settings write re-fetches it, and an admin can clear the hub's
   * public URL — so a passkey selection made before that falls back to the
   * password arm.
   */
  effectiveMethod: Accessor<AuthMethod>
  /** Handler for the method pills. */
  select: (method: AuthMethod) => void
  /** The captcha action for the effective method. */
  captchaAction: Accessor<AuthCaptchaAction>
}

/**
 * Owns the credential-kind choice for an auth form.
 *
 * The raw signal stays PRIVATE on purpose. LoginPage and SignupForm each
 * kept their own copy of this two-line state machine, and each then read
 * the raw signal at one or two sites while every other site read the
 * effective one — so a hub that lost passkey support mid-form hid the
 * password field, hid the "Forgot password?" link and skipped the password
 * validation, while the submit path had already fallen back to the password
 * arm. A caller that cannot reach the raw signal cannot reintroduce that
 * split.
 */
export function createAuthMethodSelection(kind: AuthMethodKind): AuthMethodSelection {
  const [method, setMethod] = createSignal<AuthMethod>('password')

  const effectiveMethod = (): AuthMethod =>
    method() === 'passkey' && passkeyBlocker() === null ? 'passkey' : 'password'

  const captchaAction = (): AuthCaptchaAction => {
    if (effectiveMethod() === 'passkey')
      return kind === 'login' ? 'passkey_login' : 'passkey_signup'
    return kind
  }

  return { effectiveMethod, select: setMethod, captchaAction }
}

/**
 * The method pills a sign-in or sign-up form offers.
 *
 * ONE statement of the rule, because both forms ask the same question and an
 * answer that differed between them would be a bug nobody could see from
 * either file.
 *
 * Two shapes of refusal, and they are not the same to a reader:
 *
 *   - The BROWSER refuses (the page is not secure, or it has no WebAuthn at
 *     all). The pill STAYS, disabled, carrying the reason. It is a property of
 *     where the reader is standing, and they can move: hiding it leaves
 *     somebody whose only credential is a passkey at a dead end with nothing
 *     to read.
 *   - The HUB does not run ceremonies at this origin. The pill GOES. It is a
 *     property of the deployment, identical for every visitor, and a
 *     permanently dead pill on the sign-in page of a hub without passkeys is
 *     noise rather than help.
 *
 * The account panel still explains the hub's refusal in full, to the one
 * audience that can do something about it -- somebody already signed in.
 */
export function authMethodOptions(): PillOptionSpec<AuthMethod>[] {
  const options: PillOptionSpec<AuthMethod>[] = [{ value: 'password', label: 'Password' }]
  const blocker = passkeyBlocker()
  if (blocker === 'origin-not-allowed')
    return options
  options.push({
    value: 'passkey',
    label: 'Passkey',
    disabledReason: blocker ? passkeyBlockerMessage(blocker) : undefined,
  })
  return options
}
