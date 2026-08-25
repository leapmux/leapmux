import type { Accessor } from 'solid-js'

import { createSignal } from 'solid-js'
import { isPasskeyEnabled } from '~/lib/systemInfo'

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
   * The method the form must actually act on. `isPasskeyEnabled()` can flip
   * false at runtime — a captcha refusal re-fetches system info, and an
   * admin can clear the hub's public URL — so a passkey selection made
   * before that falls back to the password arm.
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
    method() === 'passkey' && isPasskeyEnabled() ? 'passkey' : 'password'

  const captchaAction = (): AuthCaptchaAction => {
    if (effectiveMethod() === 'passkey')
      return kind === 'login' ? 'passkey_login' : 'passkey_signup'
    return kind
  }

  return { effectiveMethod, select: setMethod, captchaAction }
}
