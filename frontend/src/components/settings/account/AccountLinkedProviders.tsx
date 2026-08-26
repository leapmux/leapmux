import type { Component } from 'solid-js'
import type { StatusMessage } from '~/components/common/StatusLine'
import { createSignal, For, Show } from 'solid-js'
import { userClient } from '~/api/clients'
import { StatusLine } from '~/components/common/StatusLine'
import { useAuth } from '~/context/AuthContext'
import { elevationPrompting } from '~/lib/elevationPrompt'
import { formatErrorMessage } from '~/lib/errors'
import * as styles from './accountFields.css'
import * as listStyles from './credentialList.css'

/**
 * The identity providers this account signs in through, and the control that
 * detaches one.
 *
 * The row RENDERS EVEN WHEN EMPTY, unlike the section it replaced, which
 * appeared only for an account that already had a link. A settings row that
 * comes and goes leaves the reader unable to tell "I have no links" from "this
 * hub has no providers"; the empty state says which.
 */
export const AccountLinkedProviders: Component = () => {
  const auth = useAuth()
  const [unlinking, setUnlinking] = createSignal<string | null>(null)
  const [message, setMessage] = createSignal<StatusMessage | null>(null)

  const providers = () => auth.user()?.oauthProviders ?? []

  const unlink = async (providerId: string) => {
    setUnlinking(providerId)
    setMessage(null)
    try {
      await userClient.unlinkOAuthProvider({ providerId })
      auth.refreshUser()
    }
    catch (e) {
      setMessage({ type: 'error', text: formatErrorMessage(e, 'Failed to unlink provider') })
    }
    finally {
      setUnlinking(null)
    }
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
              A DISABLED provider is listed, and must say so. The hub reports
              the link either way so the owner can detach it — filtering it out
              left them holding a login method they could neither use nor
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
              onClick={() => void unlink(provider.id)}
              disabled={unlinking() === provider.id || elevationPrompting()}
            >
              {unlinking() === provider.id ? 'Unlinking...' : 'Unlink'}
            </button>
          </div>
        )}
      </For>
      <StatusLine message={message()} />
    </div>
  )
}
