import type { Component } from 'solid-js'
import { createSignal } from 'solid-js'
import { userClient } from '~/api/clients'
import { passwordCanSubmit, PasswordFields } from '~/components/common/PasswordFields'
import { StatusLine } from '~/components/common/StatusLine'
import { useAuth } from '~/context/AuthContext'
import { elevationPrompting } from '~/lib/elevationPrompt'
import * as styles from './accountFields.css'
import { createAccountAction } from './createAccountAction'

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
  const action = createAccountAction()

  const pwProps = { password: newPassword, confirmPassword }

  const change = async () => {
    // Silently, and not through `reject`: the button is already disabled for
    // this state, and PasswordFields states the rule under the field itself.
    if (!passwordCanSubmit(pwProps))
      return
    await action.run({
      fallback: 'Failed to change password',
      work: async () => {
        // Read BEFORE the request: the refresh below moves this flag, and the
        // message states what the user just did.
        const wasSet = auth.user()?.passwordSet === true
        await userClient.changePassword({ newPassword: newPassword() })
        setNewPassword('')
        setConfirmPassword('')
        await auth.refreshUser()
        return wasSet ? 'Password changed.' : 'Password set.'
      },
    })
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
      <StatusLine message={action.message()} />
      <div>
        <button
          type="button"
          onClick={() => void change()}
          disabled={action.running() || elevationPrompting() || !passwordCanSubmit(pwProps)}
        >
          {action.running()
            ? (auth.user()?.passwordSet ? 'Changing...' : 'Setting...')
            : (auth.user()?.passwordSet ? 'Change Password' : 'Set Password')}
        </button>
      </div>
    </div>
  )
}
