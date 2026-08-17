import type { Component } from 'solid-js'
import { createSignal, Show } from 'solid-js'
import { errorText } from '~/styles/shared.css'
import * as styles from '../SettingRow.css'

export interface SecretControlProps {
  /** Whether the secret half already carries a stored value. */
  isSet: boolean
  ariaLabel: string
  /** Write the newly typed secret. */
  onSet: (value: string) => Promise<void>
}

/**
 * A write-only secret field (admin surface: SMTP password etc.). A stored
 * secret never leaves the hub, so the control renders only a fresh input:
 * the button's label ("Set" vs "Replace") is the sole evidence a value is
 * stored, and committing sends exactly what was typed.
 */
export const SecretControl: Component<SecretControlProps> = (props) => {
  const [draft, setDraft] = createSignal('')
  const [busy, setBusy] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)

  const commit = async () => {
    // The WIRE value is exactly what the operator typed. Only the emptiness
    // guard trims: a credential with a leading or trailing space is a
    // credential, the control can never redisplay the stored secret, and a
    // silently altered one has no symptom other than a later auth failure.
    const value = draft()
    if (value.trim() === '' || busy())
      return
    setBusy(true)
    setError(null)
    try {
      await props.onSet(value)
      setDraft('')
    }
    catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
    finally {
      setBusy(false)
    }
  }

  return (
    <span class={styles.secretRow}>
      <input
        type="password"
        aria-label={props.ariaLabel}
        autocomplete="new-password"
        placeholder={props.isSet ? '••••••••' : 'Not set'}
        value={draft()}
        onInput={(e) => {
          setDraft(e.currentTarget.value)
          setError(null)
        }}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault()
            void commit()
          }
        }}
        disabled={busy()}
      />
      <button
        type="button"
        class="small outline"
        disabled={busy() || draft().trim() === ''}
        onClick={() => void commit()}
      >
        {props.isSet ? 'Replace' : 'Set'}
      </button>
      <Show when={error()}>
        <span class={errorText}>{error()}</span>
      </Show>
    </span>
  )
}
