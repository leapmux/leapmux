import type { Component } from 'solid-js'
import { Show } from 'solid-js'
import { validatePassword } from '~/lib/validate'
import * as styles from './PasswordFields.css'

interface PasswordFieldsBase {
  password: () => string
  setPassword: (v: string) => void
  confirmPassword: () => string
  setConfirmPassword: (v: string) => void
  labelClass?: string
}

type PasswordFieldsProps = PasswordFieldsBase & (
  | { showCurrentPassword?: false }
  | { showCurrentPassword: boolean, currentPassword: () => string, setCurrentPassword: (v: string) => void }
)

/**
 * Shared password + confirm password fields with live inline validation.
 * Use `error()` and `canSubmit()` from the returned helpers to wire up
 * the parent form's submit button.
 */
export const PasswordFields: Component<PasswordFieldsProps> = (props) => {
  // When showCurrentPassword is true, the discriminated union guarantees
  // currentPassword and setCurrentPassword are present.
  const curPw = () => props.showCurrentPassword ? props.currentPassword() : ''
  const setCurPw = (v: string) => {
    if (props.showCurrentPassword)
      props.setCurrentPassword(v)
  }

  return (
    <>
      <Show when={props.showCurrentPassword}>
        <label class={props.labelClass}>
          Current Password
          <input
            type="password"
            value={curPw()}
            onInput={e => setCurPw(e.currentTarget.value)}
            autocomplete="current-password"
          />
        </label>
      </Show>
      <label class={props.labelClass}>
        New Password
        <input
          type="password"
          value={props.password()}
          onInput={e => props.setPassword(e.currentTarget.value)}
          autocomplete="new-password"
        />
        <Show when={props.password()}>
          {(() => {
            const err = () => passwordError(props)
            const s = () => passwordStrength(props.password())
            const color = () => err() ? '--danger' : s().color
            return (
              <div class={styles.strengthRow}>
                <Show when={!err()}>
                  <progress class={styles.strengthProgress} value={s().percent} max="100" style={{ '--primary': `var(${s().color})` }} />
                </Show>
                <span class={styles.strengthLabel} style={{ color: `var(${color()})` }}>{err() || s().label}</span>
              </div>
            )
          })()}
        </Show>
      </label>
      <label class={props.labelClass}>
        Confirm Password
        <input
          type="password"
          value={props.confirmPassword()}
          onInput={e => props.setConfirmPassword(e.currentTarget.value)}
          autocomplete="new-password"
        />
      </label>
    </>
  )
}

interface StrengthResult {
  label: string
  percent: number
  color: string // CSS variable name
}

const RE_LOWER = /[a-z]/
const RE_UPPER = /[A-Z]/
const RE_DIGIT = /\d/
const RE_SPECIAL = /[^a-z\d]/i
const RE_UNIFORM = /^[a-z]+$|^\d+$/i

function passwordStrength(pw: string): StrengthResult {
  if (!pw)
    return { label: '', percent: 0, color: '--muted' }

  let score = 0

  // Length contribution (up to 3 points).
  if (pw.length >= 8)
    score += 1
  if (pw.length >= 12)
    score += 1
  if (pw.length >= 16)
    score += 1

  // Character variety (up to 4 points).
  if (RE_LOWER.test(pw))
    score += 1
  if (RE_UPPER.test(pw))
    score += 1
  if (RE_DIGIT.test(pw))
    score += 1
  if (RE_SPECIAL.test(pw))
    score += 1

  // Penalty for uniform character class.
  if (RE_UNIFORM.test(pw))
    score = Math.max(score - 2, 0)

  if (score <= 2)
    return { label: 'Weak', percent: 25, color: '--danger' }
  if (score <= 4)
    return { label: 'Fair', percent: 50, color: '--warning' }
  if (score <= 5)
    return { label: 'Good', percent: 75, color: '--success' }
  return { label: 'Strong', percent: 100, color: '--success' }
}

/* eslint-disable solid/reactivity -- These helpers read reactive accessors and are designed to be called within tracked scopes (JSX, createMemo, createEffect). */

/** Reactive validation error for password fields. */
export function passwordError(props: Pick<PasswordFieldsBase, 'password' | 'confirmPassword'>): string | null {
  const pw = props.password()
  if (!pw)
    return null
  const validationErr = validatePassword(pw)
  if (validationErr)
    return validationErr
  if (props.confirmPassword() && pw !== props.confirmPassword())
    return 'Passwords do not match.'
  return null
}

/** Whether the password fields are valid and ready to submit. */
export function passwordCanSubmit(props: Pick<PasswordFieldsBase, 'password' | 'confirmPassword'> & { showCurrentPassword?: boolean, currentPassword?: () => string }): boolean {
  if (props.showCurrentPassword && props.currentPassword && props.currentPassword() === '')
    return false
  return props.password() !== '' && props.confirmPassword() !== '' && !passwordError(props)
}

/* eslint-enable solid/reactivity */
