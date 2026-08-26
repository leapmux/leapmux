import type { Component } from 'solid-js'

import { useNavigate } from '@solidjs/router'
import { useAuth } from '~/context/AuthContext'
import { loadSystemInfo } from '~/lib/systemInfo'
import { pageCard } from '~/styles/shared.css'
import * as styles from './LoginPage.css'
import { SignupForm } from './SignupForm'

/**
 * First-run setup: create the hub's first administrator.
 *
 * The page renders unconditionally. Whether this address is the right one for
 * this hub is `SetupGate`'s decision, made above the router outlet in both
 * directions — it sends a visitor here while no account exists, and away to
 * `/login` once one does. The check used to sit in an `onMount` here, where it
 * read `isSetupRequired()` before the system info had arrived: on a cold load
 * that answered the fabricated `false`, so a direct visit to `/setup` bounced
 * to `/login` and back again.
 *
 * The administrator chooses a password or a passkey, the same two methods
 * `/signup` offers. A passkey-only first administrator has no password to
 * lose, and can add one later from Preferences → Account; if they lose the
 * passkey instead, the offline `leapmux recover-user` command is the way back
 * in.
 */
export const SetupPage: Component = () => {
  const navigate = useNavigate()
  const auth = useAuth()

  return (
    <div class={styles.container}>
      <div class={pageCard}>
        <h1>Welcome to LeapMux</h1>
        <SignupForm
          submitLabel="Create account"
          submittingLabel="Creating account..."
          errorPrefix="Setup failed"
          allowAdminUsername
          header={<p>Create the first administrator account to get started.</p>}
          onSuccess={(resp) => {
            // Fire-and-forget refresh of `setupRequired`: the account now
            // exists, so a stale `true` here only means a later /setup visit
            // redirects once it re-reads. Nothing on this path may block or
            // fail on it, hence the swallowed rejection.
            //
            // Nothing races it either. `setAuth` below runs first and
            // synchronously, and SetupGate lets an authenticated visitor
            // through whatever the snapshot still says — a session proves an
            // account exists.
            void loadSystemInfo(true).catch(() => {})
            auth.setAuth(resp.user)
            navigate('/', { replace: true })
          }}
        />
      </div>
    </div>
  )
}
