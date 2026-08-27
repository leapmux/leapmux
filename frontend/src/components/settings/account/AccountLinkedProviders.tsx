import type { Component } from 'solid-js'
import { For, Show } from 'solid-js'
import { userClient } from '~/api/clients'
import { StatusLine } from '~/components/common/StatusLine'
import { useAuth } from '~/context/AuthContext'
import { elevationPrompting } from '~/lib/elevationPrompt'
import * as styles from './accountFields.css'
import { createAccountAction } from './createAccountAction'
import * as listStyles from './credentialList.css'

/**
 * The identity providers this account signs in through, and the control that
 * detaches one.
 *
 * The row RENDERS EVEN WHEN EMPTY, unlike the section it replaced, which
 * appeared only for an account that already had a link. A settings row that
 * appears and disappears leaves the reader unable to tell "I have no links"
 * from "this hub has no providers"; the empty state says which.
 */
export const AccountLinkedProviders: Component = () => {
  const auth = useAuth()
  // KEYED by the provider id: one row's request must disable that row's own
  // control, and not every row's.
  const action = createAccountAction<string>()

  const providers = () => auth.user()?.oauthProviders ?? []

  const detach = async (providerId: string) => {
    await action.run({
      token: providerId,
      fallback: 'Failed to unlink provider',
      work: async () => {
        await userClient.unlinkOAuthProvider({ providerId })
        // AWAITED, and inside the work: `busy` clears when this resolves, so
        // an unawaited refresh would re-enable the control while the stale
        // user still lists the provider that the hub just detached. A second click
        // sends the same request, the hub answers NotFound, and this panel
        // reports a failure for an operation that SUCCEEDED.
        await auth.refreshUser()
        // The row disappears, which is the whole report.
        return null
      },
    })
  }

  return (
    <div class="vstack gap-2">
      <Show when={providers().length === 0}>
        <p class={listStyles.credentialListEmpty}>This account signs in with no identity provider.</p>
      </Show>
      <For each={providers()}>
        {provider => (
          <div class={styles.linkedAccount}>
            <span class={styles.linkedAccountName}>{provider.name}</span>
            {/*
              This panel lists a DISABLED provider, and must say so. The hub
              reports the link either way so the owner can detach it — filtering
              it out left them holding a login method they could neither use nor
              remove — but nothing behind it works: every OAuth leg answers 403
              "provider disabled". Without this the row is indistinguishable
              from a working one, and the user reasonably tries to sign in with
              it.
            */}
            <Show when={!provider.enabled}>
              <span class={styles.linkedAccountDisabled} data-testid={`linked-disabled-${provider.id}`}>
                Turned off by an administrator — you cannot sign in with it
              </span>
            </Show>
            <button
              type="button"
              class={styles.linkedAccountUnlink}
              onClick={() => void detach(provider.id)}
              disabled={action.busy() === provider.id || elevationPrompting()}
            >
              {action.busy() === provider.id ? 'Unlinking...' : 'Unlink'}
            </button>
          </div>
        )}
      </For>
      <StatusLine message={action.message()} />
    </div>
  )
}
