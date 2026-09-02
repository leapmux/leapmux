import type { Component } from 'solid-js'
import { createSignal, Show } from 'solid-js'
import { userClient } from '~/api/clients'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { Alert } from '~/components/common/Alert'
import { passwordCanSubmit, PasswordFields } from '~/components/common/PasswordFields'
import { StatusLine } from '~/components/common/StatusLine'
import { useAuth } from '~/context/AuthContext'
import { elevationPrompting } from '~/lib/elevationPrompt'
import { isSoloMode, loadSystemInfo } from '~/lib/systemInfo'
import * as styles from './accountFields.css'
import { createAccountAction } from './createAccountAction'

/**
 * Set or change the account password.
 *
 * There is no "current password" field: the step-up is the SESSION's
 * elevation, proved once for every sensitive action, not a secret re-typed
 * per action. See the elevationInterceptor in `~/api/transport`.
 *
 * It is the one Account row a SOLO hub keeps, and it is where that hub's first
 * password is set. ChangePassword is the one account verb solo does not
 * refuse, so the WRITE needs no branch for it. Two things around the write do:
 * the warning below states what a first password arms on such a hub, and the
 * re-read afterwards moves the snapshot the Network access panel decides on.
 */
export const AccountPassword: Component = () => {
  const auth = useAuth()
  const [newPassword, setNewPassword] = createSignal('')
  const [confirmPassword, setConfirmPassword] = createSignal('')
  const action = createAccountAction()

  const pwProps = { password: newPassword, confirmPassword }

  /**
   * Whether a password already signs this account in.
   *
   * The ACCOUNT answers it, on every hub. The hub reports the stored hash here
   * rather than the users.password_set column, so a solo account that has
   * never held one reads false -- see User.password_set in auth.proto.
   */
  const passwordSet = () => auth.user()?.passwordSet === true

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
        const wasSet = passwordSet()
        await userClient.changePassword({ newPassword: newPassword() })
        setNewPassword('')
        setConfirmPassword('')
        // BOTH re-reads, and neither may fail the action: the hub stored the
        // password already, so reporting "Failed to change password" here
        // would tell the user to retry a write that landed. `refreshUser`
        // swallows its own failure; the snapshot load rejects by design, and
        // allSettled is what holds that promise to the same rule.
        //
        // The SNAPSHOT is the solo hub's copy of this same fact, and
        // Administration → Network access reads it to decide whether it must
        // ask for a first password beside the addresses it guards. Without
        // this read that panel keeps offering the field, and an Apply there
        // replaces the password this row just stored. A multi-user hub reports
        // `soloPasswordSet` false whatever the account holds, so the read
        // would move nothing.
        await Promise.allSettled([
          auth.refreshUser(),
          ...(isSoloMode() ? [loadSystemInfo(true)] : []),
        ])
        return wasSet ? 'Password changed.' : 'Password set.'
      },
    })
  }

  return (
    <div class="vstack gap-4">
      {/*
        The FIRST password on a solo hub arms a rule that reaches every
        address, so this row states it exactly as the two other surfaces that
        offer that password do -- the setup gate and the Network access panel.
        Without it this is the one place a solo owner can arm it silently.

        It goes once the account holds a password: replacing one changes
        nothing about who is asked for it.
      */}
      <Show when={isSoloMode() && !passwordSet()}>
        <Alert variant="warning">
          This hub authenticates everyone who can reach it while the “solo”
          account has no password. Setting one asks every network address for a
          sign-in as “solo”, 127.0.0.1 included. The desktop app’s local socket
          is the only exception.
        </Alert>
      </Show>
      <PasswordFields
        password={newPassword}
        setPassword={setNewPassword}
        confirmPassword={confirmPassword}
        setConfirmPassword={setConfirmPassword}
        labelClass={styles.fieldLabel}
      />
      <StatusLine message={action.message()} />
      <div class={actionsFooter}>
        <button
          type="button"
          onClick={() => void change()}
          disabled={action.running() || elevationPrompting() || !passwordCanSubmit(pwProps)}
        >
          {action.running()
            ? (passwordSet() ? 'Changing...' : 'Setting...')
            : (passwordSet() ? 'Change Password' : 'Set Password')}
        </button>
      </div>
    </div>
  )
}
