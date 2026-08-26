import type { Component } from 'solid-js'
import type { StatusMessage } from '~/components/common/StatusLine'
import { createSignal } from 'solid-js'
import { userClient } from '~/api/clients'
import { passwordCanSubmit, PasswordFields } from '~/components/common/PasswordFields'
import { StatusLine } from '~/components/common/StatusLine'
import { useAuth } from '~/context/AuthContext'
import { elevationPrompting } from '~/lib/elevationPrompt'
import { formatErrorMessage } from '~/lib/errors'
import * as styles from './accountFields.css'

/**
 * Set or change the account password.
 *
 * There is no "current password" field: the step-up is the SESSION's
 * elevation, proved once for every sensitive action, not a secret re-typed
 * per action. See the elevationInterceptor in `~/api/transport`.
 */
export const AccountPassword: Component = () => {
  const auth = useAuth()
  const [newPassword, setNewPassword] = createSignal('')
  const [confirmPassword, setConfirmPassword] = createSignal('')
  const [saving, setSaving] = createSignal(false)
  const [message, setMessage] = createSignal<StatusMessage | null>(null)

  const pwProps = { password: newPassword, confirmPassword }

  const change = async () => {
    if (!passwordCanSubmit(pwProps))
      return
    setSaving(true)
    setMessage(null)
    try {
      const wasSet = auth.user()?.passwordSet === true
      await userClient.changePassword({ newPassword: newPassword() })
      setMessage({ type: 'success', text: wasSet ? 'Password changed.' : 'Password set.' })
      setNewPassword('')
      setConfirmPassword('')
      await auth.refreshUser()
    }
    catch (e) {
      setMessage({ type: 'error', text: formatErrorMessage(e, 'Failed to change password') })
    }
    finally {
      setSaving(false)
    }
  }

  return (
    <div class="vstack gap-4">
      <PasswordFields
        password={newPassword}
        setPassword={setNewPassword}
        confirmPassword={confirmPassword}
        setConfirmPassword={setConfirmPassword}
        labelClass={styles.fieldLabel}
      />
      <StatusLine message={message()} />
      <div>
        <button
          type="button"
          onClick={() => void change()}
          disabled={saving() || elevationPrompting() || !passwordCanSubmit(pwProps)}
        >
          {saving()
            ? (auth.user()?.passwordSet ? 'Changing...' : 'Setting...')
            : (auth.user()?.passwordSet ? 'Change Password' : 'Set Password')}
        </button>
      </div>
    </div>
  )
}
