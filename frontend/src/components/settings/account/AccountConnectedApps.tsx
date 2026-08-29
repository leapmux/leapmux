import type { Component } from 'solid-js'
import type { StatusMessage } from '~/components/common/StatusLine'
import type { MyAPIToken } from '~/generated/leapmux/v1/user_pb'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import { createMemo, createSignal, For, onMount, Show } from 'solid-js'
import { userClient } from '~/api/clients'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { ConfirmDialog } from '~/components/common/ConfirmDialog'
import { Spinner } from '~/components/common/Spinner'
import { StatusLine } from '~/components/common/StatusLine'
import { formatErrorMessage } from '~/lib/errors'
import * as styles from './credentialList.css'
import { fetchAllPages, MAX_PAGES, PAGE_SIZE } from './fetchAllPages'

/**
 * One app and every credential this account holds for it.
 *
 * The panel groups by `clientId` rather than by name, because the name is the
 * registrant's choice and two apps may share one. `clientName` is carried for
 * display only.
 */
interface ConnectedApp {
  clientId: string
  clientName: string
  verified: boolean
  installations: MyAPIToken[]
}

/**
 * The apps CONNECTED to this account, and self-service disconnection.
 *
 * GROUPED BY APP, with two endings, because those are two different decisions:
 *
 * - **Disconnect** ends the account's whole authorization of one app. It is
 *   what somebody reaches for on deciding an app should not reach their
 *   account, and it must take every machine that app runs on. A flat list of
 *   credentials made that a repeated action whose completeness the reader had
 *   to verify by eye, and one missed row leaves the app working.
 * - **Revoke** ends ONE installation, which is how a single laptop is signed
 *   out while the rest keep working.
 *
 * Under each app is one row per installation: which machine, and exactly what
 * the account granted it. The permission list is the point -- it is what a
 * person reads to decide, and a count would answer a question nobody asked.
 *
 * Neither listing nor either ending requires an elevated session. Listing
 * carries metadata only, and both endings can only REDUCE access -- a demand
 * for a fresh factor from somebody who believes an app is malicious is the
 * wrong failure mode, and the delay is the attacker's gain. The rule gets
 * STRONGER with third-party apps, not weaker.
 */
export const AccountConnectedApps: Component = () => {
  const [tokens, setTokens] = createSignal<MyAPIToken[]>([])
  const [loading, setLoading] = createSignal(true)
  // Distinct from `message`, which `revoke` also writes. Without it a hub
  // that answered 500 rendered "No command-line credentials" beside "Failed
  // to load" -- telling the user they own none, on the one screen the docs
  // send them to when they believe one is stolen.
  const [loadFailed, setLoadFailed] = createSignal(false)
  const [busy, setBusy] = createSignal(false)
  const [message, setMessage] = createSignal<StatusMessage | null>(null)
  // TWO confirmations, because the two endings take different things. One
  // target would have to carry which verb it meant, and the dialog would then
  // describe the wrong ending whenever that flag and the target disagreed.
  const [disconnectTarget, setDisconnectTarget] = createSignal<ConnectedApp | null>(null)
  const [revokeTarget, setRevokeTarget] = createSignal<MyAPIToken | null>(null)

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
   * also stops when the cursor fails to advance and when it read MAX_PAGES.
   * Both limits are far above any real account.
   */
  const refresh = async () => {
    setLoading(true)
    setLoadFailed(false)
    try {
      // The shared every-page loop owns the runaway cap, the stalled-cursor
      // guard and the keyed dedupe this panel taught the codebase.
      const tokens = await fetchAllPages(
        async cursor => await userClient.listMyAPITokens({ cursor, limit: BigInt(PAGE_SIZE) })
          .then(resp => ({ items: resp.tokens, nextCursor: resp.nextCursor ?? '' })),
        { maxPages: MAX_PAGES, keyOf: token => token.id },
      )
      setTokens(tokens)
    }
    catch (e) {
      setLoadFailed(true)
      setMessage({ type: 'error', text: formatErrorMessage(e, 'Failed to load connected apps') })
    }
    finally {
      setLoading(false)
    }
  }

  onMount(() => {
    void refresh()
  })

  /**
   * End the account's whole authorization of one app.
   *
   * ONE call, not one per installation. A client loop would report success
   * after a partial failure and leave the app working on a machine the reader
   * believes is disconnected; the hub retires every row in one statement.
   */
  const disconnect = async (app: ConnectedApp) => {
    setBusy(true)
    setMessage(null)
    try {
      await userClient.disconnectApp({ clientId: app.clientId })
      setTokens(tokens().filter(t => t.clientId !== app.clientId))
      setMessage({ type: 'success', text: 'App disconnected.' })
    }
    catch (e) {
      setMessage({ type: 'error', text: formatErrorMessage(e, 'Failed to disconnect the app') })
    }
    finally {
      setBusy(false)
      setDisconnectTarget(null)
    }
  }

  /** End ONE installation, leaving the app connected on every other machine. */
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
      setRevokeTarget(null)
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
    // The fixed-lifetime kind carries no refresh deadline, so its whole life
    // is reported instead. The hub sets exactly one of the two, so a row with
    // neither is a credential that never expires rather than one whose
    // deadline this panel could not read.
    else if (token.expiresAt)
      parts.push(`Expires ${timestampDate(token.expiresAt).toLocaleDateString()}`)
    return parts.join(' · ')
  }

  /**
   * The permissions this credential holds, as an ARRAY that renders one chip
   * each. An empty grant renders nothing rather than "none": the hub refuses
   * to store one, so a row without permissions is a credential this panel
   * could not read, and a confident "none" would be a lie.
   */
  const permissionsOf = (token: MyAPIToken) => token.grantedScopes ?? []

  /**
   * Whether a credential reaches hub administration. It is a property of the
   * GRANT, read from the scope tokens themselves rather than from a separate
   * flag -- there is no second field to disagree with the list a reader sees.
   */
  const administersHub = (token: MyAPIToken) =>
    permissionsOf(token).some(scope => scope.startsWith('admin:'))

  /**
   * The listing, grouped by APP.
   *
   * Derived from `tokens()` rather than stored beside it, so the two cannot
   * drift: an ending removes rows from the one signal and every group
   * recomputes, including the app disappearing when its last row goes.
   *
   * Grouped on `clientId`, never on the name. The name is the registrant's
   * choice and two apps may share one, so grouping by it would merge two
   * apps into a block whose single Disconnect took only one of them.
   *
   * `clientName` and `clientVerified` are read from the FIRST row of each
   * group. Every row of one app carries the same registration, so any row
   * answers; taking the first keeps the read at one place.
   */
  // createMemo hands its previous value to the function, and the grouping
  // reads it to preserve wrapper identity (see the comment inside).
  const connectedApps = createMemo((prev: ConnectedApp[] | undefined): ConnectedApp[] => {
    // Build the groups fresh, then keep the PREVIOUS wrapper for a group
    // whose installation ids are unchanged. <For> keys by reference, so a
    // memo that rebuilt every wrapper disposed and re-created every group on
    // each write -- O(total installations) of DOM churn for a one-row revoke.
    // The token objects themselves keep their identity through a filter, so
    // matching ids means matching rows.
    const prevByClient = new Map((prev ?? []).map(g => [g.clientId, g]))
    const byClient = new Map<string, ConnectedApp>()
    for (const token of tokens()) {
      const existing = byClient.get(token.clientId)
      if (existing) {
        existing.installations.push(token)
        continue
      }
      byClient.set(token.clientId, {
        clientId: token.clientId,
        clientName: token.clientName,
        verified: token.clientVerified,
        installations: [token],
      })
    }
    return [...byClient.values()].map((group) => {
      const prev = prevByClient.get(group.clientId)
      if (!prev)
        return group
      const prevIds = prev.installations.map(t => t.id).join('\u0000')
      const nextIds = group.installations.map(t => t.id).join('\u0000')
      return prevIds === nextIds ? prev : group
    })
  })

  return (
    <>
      <div class="vstack gap-4" data-testid="connected-apps">
        <Show when={loading()}>
          <div class={styles.credentialListLoading}><Spinner /></div>
        </Show>
        <Show when={!loading() && !loadFailed() && tokens().length === 0}>
          <p class={styles.credentialListEmpty}>
            No connected apps. Run
            {' '}
            <code>leapmux control auth login</code>
            {' '}
            to connect the command-line tool, or authorize an app from its own sign-in screen.
          </p>
        </Show>
        {/*
          Keyed by object identity, which the grouping below PRESERVES for a
          group whose installations did not change: one revoke removes one row
          from one group instead of disposing and re-creating every group on
          the page.
        */}
        <For each={connectedApps()}>
          {app => (
            <div class={styles.credentialGroup} data-testid={`connected-app-${app.clientId}`}>
              {/*
                The app's own line: its name and the vouch state, on the full
                width. The Disconnect sits in a footer row under the
                installations, so the app-level ending reads as the block's
                closing action rather than as one more thing competing with
                the name for the same line.
              */}
              <div class={styles.credentialInfo}>
                <span class={styles.credentialName}>
                  {app.clientName || 'Unnamed app'}
                  <Show when={!app.verified}>
                    {' '}
                    <span class="badge" data-variant="warning">unverified</span>
                  </Show>
                </span>
              </div>
              <div class={styles.credentialGroupBody}>
                <For each={app.installations}>
                  {token => (
                    <div class={styles.credentialRow} data-testid={`app-credential-${token.id}`}>
                      <div class={styles.credentialInfo}>
                        <span class={styles.credentialName}>
                          {/*
                            The INSTALLATION names this row. One app holds one
                            credential per machine, so the app name above
                            cannot tell two rows apart -- which is the whole
                            reason a per-installation ending exists beside the
                            app-level one.
                          */}
                          {token.installationName || 'Unnamed installation'}
                          <Show when={administersHub(token)}>
                            {' '}
                            <span class="badge" data-variant="warning">hub administration</span>
                          </Show>
                          {/*
                            This list deliberately does NOT render
                            MyAPIToken.current. The hub derives it from the
                            credential that made the call, and a browser
                            session is never one of the api_tokens rows this
                            list holds -- so it is false for every row on this
                            page, always. `leapmux control auth credentials`
                            is the caller that can see it, and it prints it. A
                            branch that cannot render is a promise to the next
                            reader that it can.
                          */}
                        </span>
                        <span class={styles.credentialMeta}>{describe(token)}</span>
                        <Show when={permissionsOf(token).length > 0}>
                          <span class={styles.credentialScopeLine} data-testid={`app-scopes-${token.id}`}>
                            <For each={permissionsOf(token)}>
                              {scope => <span class={styles.credentialScope}>{scope}</span>}
                            </For>
                          </span>
                        </Show>
                      </div>
                      <div class={actionsFooter}>
                        {/*
                          An Oat danger OUTLINE: this control only OPENS the
                          confirmation, whose primary carries the danger
                          weight. Filling it here would paint the louder
                          button on the step that still cancels.
                        */}
                        <button
                          type="button"
                          class="outline"
                          data-variant="danger"
                          onClick={() => setRevokeTarget(token)}
                          disabled={busy()}
                        >
                          Revoke
                        </button>
                      </div>
                    </div>
                  )}
                </For>
              </div>
              <div class={actionsFooter}>
                <button
                  type="button"
                  class="outline"
                  data-variant="danger"
                  onClick={() => setDisconnectTarget(app)}
                  disabled={busy()}
                >
                  Disconnect
                </button>
              </div>
            </div>
          )}
        </For>
        <StatusLine message={message()} />
      </div>

      <Show when={disconnectTarget()}>
        {app => (
          <ConfirmDialog
            title="Disconnect this app?"
            confirmLabel="Disconnect"
            danger
            busy={busy()}
            onCancel={() => setDisconnectTarget(null)}
            onConfirm={() => void disconnect(app())}
          >
            <p>
              {app().clientName || 'This app'}
              {' '}
              will lose access from
              {' '}
              {/*
                The COUNT, stated. This ending takes every machine, and a
                reader who has one installation open in front of them would
                otherwise have no way to tell whether it takes the others.
              */}
              {app().installations.length === 1
                ? 'the one machine it runs on'
                : `all ${app().installations.length} machines it runs on`}
              , and must be authorized again.
            </p>
          </ConfirmDialog>
        )}
      </Show>

      <Show when={revokeTarget()}>
        {token => (
          <ConfirmDialog
            title="Revoke this credential?"
            confirmLabel="Revoke"
            danger
            busy={busy()}
            onCancel={() => setRevokeTarget(null)}
            onConfirm={() => void revoke(token())}
          >
            <p>
              {token().clientName || 'This app'}
              {token().installationName ? ` on ${token().installationName}` : ''}
              {' '}
              will stop working immediately. The app stays connected on every other machine; use
              {' '}
              <b>Disconnect</b>
              {' '}
              to end all of them.
            </p>
            {/* See the list row above on why this does not render `current`. */}
          </ConfirmDialog>
        )}
      </Show>
    </>
  )
}
