import type { Accessor } from 'solid-js'
import type { CaptchaAction } from '~/generated/contracts/captcha'

import { createSignal } from 'solid-js'
import { passkeyBlocker } from '~/lib/systemInfo'

/** The two credential kinds the login and sign-up forms offer. */
export type AuthMethod = 'password' | 'passkey'

/** Which form the selection belongs to. Only the captcha action differs. */
export type AuthMethodKind = 'login' | 'signup' | 'recovery'

/**
 * The captcha action bound into an external provider's token. The hub
 * refuses a token minted under a different action, so this must follow the
 * EFFECTIVE method, never the raw selection. The members come from the
 * generated captcha contract (contracts/captcha.json), so a renamed
 * action breaks this union at compile time instead of at the hub's
 * action check.
 */
export type AuthCaptchaAction = Extract<CaptchaAction, 'login' | 'signup' | 'passkey_login' | 'passkey_signup' | 'account_recovery_password' | 'account_recovery_passkey'>

export interface AuthMethodSelection {
  /**
   * The method the form must actually act on. `passkeysUsableHere()` can
   * flip false at runtime — a captcha refusal re-fetches system info, an
   * admin settings write re-fetches it, and an admin can clear the hub's
   * public URL — so a passkey selection made before that falls back to the
   * password option.
   */
  effectiveMethod: Accessor<AuthMethod>
  /** Handler for the method pills. */
  select: (method: AuthMethod) => void
  /** The captcha action for the effective method. */
  captchaAction: Accessor<AuthCaptchaAction>
}

/** Kind × method → captcha action. A new kind is a compile error here, not a fallthrough. */
const CAPTCHA_ACTIONS = {
  login: { password: 'login', passkey: 'passkey_login' },
  signup: { password: 'signup', passkey: 'passkey_signup' },
  recovery: { password: 'account_recovery_password', passkey: 'account_recovery_passkey' },
} as const satisfies Record<AuthMethodKind, Record<AuthMethod, AuthCaptchaAction>>

/**
 * Owns the credential-kind choice for an auth form.
 *
 * The raw signal stays PRIVATE on purpose. LoginPage and SignupForm each
 * kept their own copy of this two-line state machine, and each then read
 * the raw signal at one or two sites while every other site read the
 * effective one — so a hub that lost passkey support mid-form hid the
 * password field, hid the recovery link and skipped the password
 * validation, while the submit path already fell back to the password
 * option. A caller that cannot reach the raw signal cannot reintroduce that
 * split.
 */
export function createAuthMethodSelection(kind: AuthMethodKind): AuthMethodSelection {
  const [method, setMethod] = createSignal<AuthMethod>('password')

  const effectiveMethod = (): AuthMethod =>
    method() === 'passkey' && passkeyBlocker() === null ? 'passkey' : 'password'

  const captchaAction = (): AuthCaptchaAction => CAPTCHA_ACTIONS[kind][effectiveMethod()]

  return { effectiveMethod, select: setMethod, captchaAction }
}
