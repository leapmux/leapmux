import type { Component } from 'solid-js'
import type { StatusMessage } from '~/components/common/StatusLine'
import type { MyAPIToken } from '~/generated/leapmux/v1/user_pb'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import { createSignal, For, onMount, Show } from 'solid-js'
import { userClient } from '~/api/clients'
import { ConfirmDialog } from '~/components/common/ConfirmDialog'
import { Spinner } from '~/components/common/Spinner'
import { StatusLine } from '~/components/common/StatusLine'
import { formatErrorMessage } from '~/lib/errors'
import { warningText } from '~/styles/shared.css'
import * as styles from './credentialList.css'

/**
 * The account's command-line credentials, and self-service revocation.
 *
 * Neither listing nor revoking requires an elevated session. Listing carries metadata
 * only, and revoking can only REDUCE access -- demanding a fresh factor from
 * somebody who believes a credential is stolen is the wrong failure mode,
 * and the delay is the attacker's gain.
 */
/**
 * The hub's maximum page. Asked for explicitly, because an omitted limit
 * resolves to the hub's default of fifty: the loop below then took ten times
 * the round trips and covered a tenth of what MAX_PAGES claims.
 */
const PAGE_SIZE = 500

/**
 * How many pages the credential listing reads before it stops.
 *
 * At PAGE_SIZE this covers an account with a quarter of a million live
 * credentials -- which is to say it is a runaway guard, not a limit anybody
 * reaches.
 */
const MAX_PAGES = 500

export const AccountCLITokens: Component = () => {
  const [tokens, setTokens] = createSignal<MyAPIToken[]>([])
  const [loading, setLoading] = createSignal(true)
  // Distinct from `message`, which `revoke` also writes. Without it a hub
  // that answered 500 rendered "No command-line credentials" beside "Failed
  // to load" -- telling the user they own none, on the one screen the docs
  // send them to when they believe one is stolen.
  const [loadFailed, setLoadFailed] = createSignal(false)
  const [busy, setBusy] = createSignal(false)
  const [message, setMessage] = createSignal<StatusMessage | null>(null)
  const [target, setTarget] = createSignal<MyAPIToken | null>(null)

  /**
   * Load EVERY page, not the first.
   *
   * The listing is keyset-paginated, so a single call truncated it silently
   * -- and an account with more credentials than one page holds is exactly
   * the one most likely to hold a stolen one. Revoking is only reachable
   * from a rendered row, so a row the page never drew was a credential the
   * owner could not revoke here at all.
   *
   * The loop does NOT trust the cursor to end it. A client loop whose only
   * exit is a value the server chooses is a hang the server can cause, so it
   * also stops when the cursor fails to advance and when it has read
   * MAX_PAGES. Both limits are far above any real account.
   */
  const refresh = async () => {
    setLoading(true)
    setLoadFailed(false)
    try {
      // Keyed by id while assembling: a stalled cursor makes the last page
      // arrive twice, and a keyset boundary can repeat a row across two
      // pages. Neither should render the same credential twice with two
      // Revoke buttons.
      const byID = new Map<string, MyAPIToken>()
      let cursor = ''
      for (let page = 0; page < MAX_PAGES; page++) {
        const resp = await userClient.listMyAPITokens({ cursor, limit: BigInt(PAGE_SIZE) })
        for (const token of resp.tokens)
          byID.set(token.id, token)
        const next = resp.nextCursor ?? ''
        if (next === '' || next === cursor)
          break
        cursor = next
      }
      setTokens([...byID.values()])
    }
    catch (e) {
      setLoadFailed(true)
      setMessage({ type: 'error', text: formatErrorMessage(e, 'Failed to load CLI credentials') })
    }
    finally {
      setLoading(false)
    }
  }

  onMount(() => {
    void refresh()
  })

  const revoke = async (token: MyAPIToken) => {
    setBusy(true)
    setMessage(null)
    try {
      await userClient.revokeMyAPIToken({ id: token.id })
      setTokens(tokens().filter(t => t.id !== token.id))
      setMessage({ type: 'success', text: 'Credential revoked.' })
    }
    catch (e) {
      setMessage({ type: 'error', text: formatErrorMessage(e, 'Failed to revoke the credential') })
    }
    finally {
      setBusy(false)
      setTarget(null)
    }
  }

  const describe = (token: MyAPIToken) => {
    const parts: string[] = []
    if (token.lastUsedAt)
      parts.push(`Last used ${timestampDate(token.lastUsedAt).toLocaleString()}`)
    else if (token.createdAt)
      parts.push(`Added ${timestampDate(token.createdAt).toLocaleString()}`)
    if (token.refreshExpiresAt)
      parts.push(`Signs in again ${timestampDate(token.refreshExpiresAt).toLocaleDateString()}`)
    return parts.join(' · ')
  }

  return (
    <>
      <div class="vstack gap-4" data-testid="cli-tokens">
        <Show when={loading()}>
          <div class={styles.credentialListLoading}><Spinner /></div>
        </Show>
        <Show when={!loading() && !loadFailed() && tokens().length === 0}>
          <p class={styles.credentialListEmpty}>No command-line credentials. Run `leapmux control auth login` to add one.</p>
        </Show>
        <For each={tokens()}>
          {token => (
            <div class={styles.credentialRow} data-testid={`cli-token-${token.id}`}>
              <div class={styles.credentialInfo}>
                <span class={styles.credentialName}>
                  {token.clientName || 'Unnamed device'}
                  <Show when={token.adminScope}>
                    {' '}
                    <span class={warningText}>(hub administration)</span>
                  </Show>
                  {/*
                    MyAPIToken.current is deliberately NOT rendered here. The
                    hub derives it from the credential that made the call, and
                    a browser session is never one of the api_tokens rows this
                    list holds -- so it is false for every row on this page,
                    always. `leapmux control auth credentials` is the caller
                    that can see it, and it prints it. A branch that cannot
                    render is a promise to the next reader that it can.
                  */}
                </span>
                <span class={styles.credentialMeta}>{describe(token)}</span>
              </div>
              <div class={styles.credentialActions}>
                <button
                  type="button"
                  class={styles.credentialDanger}
                  onClick={() => setTarget(token)}
                  disabled={busy()}
                >
                  Revoke
                </button>
              </div>
            </div>
          )}
        </For>
        <StatusLine message={message()} />
      </div>

      <Show when={target()}>
        {token => (
          <ConfirmDialog
            title="Revoke this credential?"
            confirmLabel="Revoke"
            danger
            busy={busy()}
            onCancel={() => setTarget(null)}
            onConfirm={() => void revoke(token())}
          >
            <p>
              {token().clientName || 'This device'}
              {' '}
              will stop working immediately and must sign in again.
            </p>
            {/* See the list row above on why `current` is not rendered here. */}
          </ConfirmDialog>
        )}
      </Show>
    </>
  )
}
