import type { Component, JSX } from 'solid-js'
import type { StatusMessage } from '~/components/common/StatusLine'
import type { App } from '~/generated/leapmux/v1/app_pb'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import { createSignal, For, onMount, Show, untrack } from 'solid-js'
import { appClient } from '~/api/clients'
import { ConfirmDialog } from '~/components/common/ConfirmDialog'
import { PillGroup } from '~/components/common/PillGroup'
import { Spinner } from '~/components/common/Spinner'
import { StatusLine } from '~/components/common/StatusLine'
import { Tooltip } from '~/components/common/Tooltip'
import { useAuth } from '~/context/AuthContext'
import { AppClientType, AppVisibility } from '~/generated/leapmux/v1/app_pb'
import { Scope } from '~/generated/leapmux/v1/scope_pb'
import { useCopyButton } from '~/hooks/useCopyButton'
import { formatErrorMessage } from '~/lib/errors'
import * as styles from './credentialList.css'
import { fetchAllPages, MAX_PAGES, PAGE_SIZE } from './fetchAllPages'
import { scopeToken } from './scopeToken'

/**
 * The grantable vocabulary, in the order scope.proto declares it.
 *
 * It is derived from the generated enum rather than typed again: a scope added
 * to the proto appears here without an edit, and one removed cannot linger as
 * a checkbox that registers nothing.
 *
 * The three NON-grantable values are excluded by name, because each is a
 * construction rather than a permission: UNSPECIFIED is "nobody classified
 * this", NEVER is a recorded refusal, and ALL is the absence of a limit, which
 * no registration may claim.
 */
const NON_GRANTABLE: readonly Scope[] = [Scope.UNSPECIFIED, Scope.NEVER, Scope.ALL]

const GRANTABLE_SCOPES: readonly Scope[] = Object.values(Scope)
  .filter((v): v is Scope => typeof v === 'number')
  .filter(scope => !NON_GRANTABLE.includes(scope))
  .sort((a, b) => a - b)

/**
 * The editable field set the register and edit forms share: name, home page,
 * redirect addresses, and the permission-ceiling checkboxes.
 *
 * ONE copy of the markup and of the toggleScope closure, so a new editable
 * registration field reaches both forms or neither -- the edit form is the
 * register form pre-filled, and the two hand-kept copies had already grown
 * the toggle in parallel. What each caller keeps is its own submit verb and
 * the register form's extra app-type selector, which slots in after the
 * redirect addresses through afterRedirects.
 */
function createAppFormFields(initial?: { name: string, uri: string, redirects: string, scopes: Scope[] }) {
  const [name, setName] = createSignal(initial?.name ?? '')
  const [uri, setUri] = createSignal(initial?.uri ?? '')
  const [redirects, setRedirects] = createSignal(initial?.redirects ?? '')
  const [scopes, setScopes] = createSignal<Scope[]>(initial ? [...initial.scopes] : [])

  const toggleScope = (scope: Scope) => {
    setScopes(current => current.includes(scope)
      ? current.filter(s => s !== scope)
      : [...current, scope])
  }

  const redirectList = () =>
    redirects().split('\n').map(line => line.trim()).filter(line => line !== '')

  const Fields: Component<{ afterRedirects?: JSX.Element }> = props => (
    <>
      <label>
        Name
        <input
          type="text"
          value={name()}
          onInput={e => setName(e.currentTarget.value)}
          placeholder="My integration"
        />
      </label>
      <label>
        Home page
        <input
          type="url"
          value={uri()}
          onInput={e => setUri(e.currentTarget.value)}
          placeholder="https://example.com"
        />
      </label>
      <label>
        Redirect addresses, one per line
        <textarea
          value={redirects()}
          onInput={e => setRedirects(e.currentTarget.value)}
          placeholder="https://example.com/callback"
          rows={3}
        />
      </label>
      {props.afterRedirects}
      <fieldset>
        <legend>Permissions this app may ask for</legend>
        <div class={styles.credentialScopeLine}>
          <For each={GRANTABLE_SCOPES}>
            {scope => (
              <label class={styles.credentialScope}>
                <input
                  type="checkbox"
                  checked={scopes().includes(scope)}
                  onChange={() => toggleScope(scope)}
                />
                {' '}
                {scopeToken(scope)}
              </label>
            )}
          </For>
        </div>
      </fieldset>
    </>
  )

  return { Fields, name, uri, scopes, redirectList, setName }
}

/**
 * The registration form.
 *
 * It is a separate component so the list above does not carry seven signals it
 * only needs while the form is open, and so closing the form discards them
 * rather than leaving a half-filled draft behind a collapsed section.
 */
const RegisterAppForm: Component<{
  busy: boolean
  onCancel: () => void
  onRegistered: (message: string, app?: App) => void
  onError: (message: string) => void
  setBusy: (value: boolean) => void
}> = (props) => {
  const fields = createAppFormFields()
  const [clientType, setClientType] = createSignal(AppClientType.PUBLIC)
  // The secret, once. The hub stores only its hash, so this signal is the
  // ONLY place it ever exists on this machine -- which is why the form stays
  // open showing it rather than closing on success like every other write.
  const [secret, setSecret] = createSignal('')
  // The created row, kept so the Done button (which closes the form after the
  // operator copied the secret) can hand it to the listing exactly like the
  // no-secret path does.
  const [created, setCreated] = createSignal<App | null>(null)
  const { copied, copy } = useCopyButton(() => secret())

  const submit = async () => {
    props.setBusy(true)
    try {
      const resp = await appClient.registerApp({
        clientName: fields.name().trim(),
        clientUri: fields.uri().trim(),
        redirectUris: fields.redirectList(),
        scopes: fields.scopes(),
        // PRIVATE always, from this panel. A hub-wide registration needs an
        // administrator, and offering a control that most accounts are
        // refused would be a worse answer than not offering it: the CLI's
        // `--visibility hub-wide` is where an administrator does it.
        visibility: AppVisibility.PRIVATE,
        clientType: clientType(),
      })
      if (resp.clientSecret) {
        setSecret(resp.clientSecret)
        setCreated(resp.app ?? null)
        return
      }
      props.onRegistered('App registered.', resp.app)
    }
    catch (e) {
      props.onError(formatErrorMessage(e, 'Failed to register the app'))
    }
    finally {
      props.setBusy(false)
    }
  }

  return (
    <div class="vstack gap-3" data-testid="register-app-form">
      <Show
        when={secret()}
        fallback={(
          <>
            {/*
              The register form's one extra control slots in after the
              redirect addresses: two options on one row, so a PillGroup
              rather than a menu -- and never a native <select>, which opens
              the OS picker and cannot show the second line each choice needs.
            */}
            <fields.Fields
              afterRedirects={(
                <PillGroup
                  label="App type"
                  options={[
                    { value: AppClientType.PUBLIC, label: 'Public (a binary a user holds)' },
                    { value: AppClientType.CONFIDENTIAL, label: 'Confidential (a server you run)' },
                  ]}
                  selected={value => clientType() === value}
                  onSelect={setClientType}
                />
              )}
            />
            <div class="hstack gap-2">
              <button type="button" onClick={() => void submit()} disabled={props.busy || fields.name().trim() === '' || fields.scopes().length === 0}>
                Register
              </button>
              <button type="button" onClick={props.onCancel} disabled={props.busy}>Cancel</button>
            </div>
          </>
        )}
      >
        <p>
          Copy this app secret now. The hub stores only its hash and cannot show it again.
        </p>
        <div class="hstack gap-2">
          <code data-testid="new-client-secret">{secret()}</code>
          <button type="button" onClick={() => void copy()}>{copied() ? 'Copied' : 'Copy'}</button>
        </div>
        <div>
          <button type="button" onClick={() => created() && props.onRegistered('App registered.', created()!)}>Done</button>
        </div>
      </Show>
    </div>
  )
}

/**
 * The edit form for one registration.
 *
 * It is the register form pre-filled, minus everything a registration cannot
 * change after the fact: the app type is fixed by whether the hub holds a
 * secret, and the visibility is the owner identity itself. What remains --
 * name, home page, redirect addresses, permission ceiling -- is the set the
 * hub replaces wholesale, so the form always sends the replace flags and
 * never leaves a half-updated registration behind.
 */
const EditAppForm: Component<{
  app: App
  busy: boolean
  onCancel: () => void
  onSaved: (message: string, app?: App) => void
  onError: (message: string) => void
  setBusy: (value: boolean) => void
}> = (props) => {
  // Each field opens PRE-FILLED from a snapshot, read once and untracked: an
  // edit must not live-update under the person typing into it.
  const fields = createAppFormFields({
    name: untrack(() => props.app.clientName),
    uri: untrack(() => props.app.clientUri),
    redirects: untrack(() => props.app.redirectUris.join('\n')),
    scopes: untrack(() => [...props.app.scopes]),
  })

  const submit = async () => {
    props.setBusy(true)
    try {
      const resp = await appClient.updateApp({
        clientId: props.app.clientId,
        clientName: fields.name().trim(),
        clientUri: fields.uri().trim(),
        replaceRedirectUris: true,
        redirectUris: fields.redirectList(),
        replaceScopes: true,
        scopes: fields.scopes(),
      })
      props.onSaved('App updated.', resp.app)
    }
    catch (e) {
      props.onError(formatErrorMessage(e, 'Failed to update the app'))
    }
    finally {
      props.setBusy(false)
    }
  }

  return (
    <div class="vstack gap-3" data-testid={`edit-app-form-${props.app.clientId}`}>
      <fields.Fields />
      <div class="hstack gap-2">
        <button type="button" onClick={() => void submit()} disabled={props.busy || fields.name().trim() === '' || fields.scopes().length === 0}>
          Save
        </button>
        <button type="button" onClick={() => props.onCancel()} disabled={props.busy}>Cancel</button>
      </div>
    </div>
  )
}

/**
 * The apps REGISTERED on this hub, and self-service registration.
 *
 * It is the other half of Connected apps, one step further out: that panel
 * holds what this account AUTHORIZED, this one holds what it registered for
 * others to authorize.
 *
 * An ordinary account may register an app, and its registration is visible and
 * authorizable to that account alone. An administrator's is hub-wide. One
 * column on the hub carries that whole rule, so there is no second flag here
 * that could disagree with what the list shows.
 */
export const AccountAppRegistrations: Component = () => {
  const auth = useAuth()
  const [apps, setApps] = createSignal<App[]>([])
  const [loading, setLoading] = createSignal(true)
  // Distinct from `message`, which the write paths also use. Without it a hub
  // that answered 500 rendered "No app registrations" beside "Failed to
  // load" -- telling somebody they have none while the truth is unknown.
  const [loadFailed, setLoadFailed] = createSignal(false)
  const [busy, setBusy] = createSignal(false)
  const [message, setMessage] = createSignal<StatusMessage | null>(null)
  const [target, setTarget] = createSignal<App | null>(null)
  const [creating, setCreating] = createSignal(false)
  const [editing, setEditing] = createSignal<string | null>(null)
  // The app whose step-up allowance is about to be WIDENED, awaiting the
  // confirmation. Refusing needs no dialog: it only reduces access.
  const [elevating, setElevating] = createSignal<App | null>(null)
  // Read from the listing so the switch an administrator flips sits beside
  // what it affects, the way app.proto documents the field.
  const [openRegistration, setOpenRegistration] = createSignal(false)

  // Only an administrator may vouch. The listing already hides everybody
  // else's private apps, so for a non-admin every row here is their own --
  // and self-vouching is exactly the thing the vouch is not.
  const vouchAvailable = () => auth.user()?.isAdmin === true

  // The generation a refresh started under. Two refreshes can overlap (the
  // onMount load and one a write path fires), and the one that started EARLIER
  // can finish last -- adopting its pages would drop a row the newer refresh
  // already shows. Only the newest generation writes its result.
  let refreshGeneration = 0

  /**
   * Rows a write path added, kept until the next refresh to complete absorbs
   * them into the server's own ordering. A refresh that started BEFORE the
   * write cannot name the row, so landing its list as-is deleted a
   * registration the user just watched appear.
   */
  const pendingRows = new Map<string, App>()
  const refresh = async () => {
    const generation = ++refreshGeneration
    setLoading(true)
    setLoadFailed(false)
    try {
      // EVERY page through the shared fetchAllPages, at the sibling panel's
      // constants: an administrator with open registration on can pass a
      // smaller page size without lifting a finger, and a registration the
      // panel never drew was one whose Edit and Retire controls did not exist.
      // The hub's own switch rides the listing; the last page's value is the
      // current one, so the fetchPage callback records it as a side effect.
      let openRegistrationEnabled = false
      const collected = await fetchAllPages(
        async (cursor) => {
          // includeRevoked: a retired app stays readable because a live
          // credential on one must still be explainable (app.proto states the
          // contract), and the retired badge and the gates below key on it --
          // without the flag the row vanishes on the refresh that retires it.
          const resp = await appClient.listApps({ cursor, limit: PAGE_SIZE, includeRevoked: true })
          openRegistrationEnabled = resp.openRegistrationEnabled
          return { items: resp.apps, nextCursor: resp.nextCursor }
        },
        { maxPages: MAX_PAGES, keyOf: app => app.clientId },
      )
      if (generation === refreshGeneration) {
        setOpenRegistration(openRegistrationEnabled)
        // PRESERVE the previous object for a row whose id came back: <For>
        // keys by reference, so reusing the old object reconciles the row in
        // place instead of disposing and remounting it -- which is what kept
        // an open EditAppForm's typed edits through any other row's write.
        //
        // Rows a write path added WHILE this refresh was paginating ride on
        // top: the refresh started before they existed, so its collected list
        // cannot name them, and landing it as-is deleted a registration the
        // user just watched appear.
        setApps((prev) => {
          const prevById = new Map(prev.map(a => [a.clientId, a]))
          const merged = collected.map(a => prevById.get(a.clientId) ?? a)
          const mergedIDs = new Set(merged.map(a => a.clientId))
          for (const row of pendingRows.values()) {
            if (!mergedIDs.has(row.clientId))
              merged.unshift(row)
          }
          return merged
        })
        pendingRows.clear()
      }
    }
    catch (e) {
      if (generation === refreshGeneration) {
        setLoadFailed(true)
        setMessage({ type: 'error', text: formatErrorMessage(e, 'Failed to load app registrations') })
      }
    }
    finally {
      if (generation === refreshGeneration)
        setLoading(false)
    }
  }

  onMount(() => {
    void refresh()
  })

  /**
   * Replace ONE row with the row a mutation response carries.
   *
   * Every mutation except Retire answers with the updated `App`, so a write
   * patches its own row instead of re-paging the listing: the full re-page is
   * a mount-time cost, and paying it per click both showed the loading state
   * over rows nobody touched and ran the whole listing's server work again.
   */
  const patchRow = (app: App | undefined) => {
    if (!app) {
      void refresh()
      return
    }
    setApps(prev => prev.map(a => (a.clientId === app.clientId ? app : a)))
  }

  /** Prepend a row the panel just created. The listing orders newest first. */
  const prependRow = (app: App | undefined) => {
    if (!app) {
      void refresh()
      return
    }
    pendingRows.set(app.clientId, app)
    setApps(prev => [app, ...prev])
  }

  const revoke = async (app: App) => {
    setBusy(true)
    setMessage(null)
    try {
      const resp = await appClient.revokeApp({ clientId: app.clientId })
      await refresh()
      const n = Number(resp.revokedCredentialCount)
      setMessage({
        type: 'success',
        text: n > 0
          ? `App retired. ${n} credential${n === 1 ? '' : 's'} revoked.`
          : 'App retired.',
      })
    }
    catch (e) {
      setMessage({ type: 'error', text: formatErrorMessage(e, 'Failed to retire the app') })
    }
    finally {
      setBusy(false)
      setTarget(null)
    }
  }

  /**
   * The registration's permission ceiling, as tokens.
   *
   * It is what any consent screen MAY grant, never what one account granted:
   * that lives on the credential and is what Connected apps shows.
   */
  const ceiling = (app: App) => app.scopes.map(scopeToken)

  // SetAppElevationAllowed, from the list. It is the one field even a
  // built-in registration may change, so -- unlike Edit -- it is offered on
  // every row.
  const setElevation = async (app: App, allowed: boolean) => {
    setBusy(true)
    setMessage(null)
    try {
      // The response carries the updated row, so the panel patches THAT row
      // rather than re-paging the whole listing: one write is one row, and the
      // full re-page cost (MAX_PAGES round trips plus the server work behind
      // each) buys nothing a single-row patch does not already state.
      const resp = await appClient.setAppElevationAllowed({ clientId: app.clientId, allowed })
      patchRow(resp.app)
      setMessage({
        type: 'success',
        text: allowed
          ? 'The app may now run the step-up stage.'
          : 'The app can no longer run the step-up stage. Any live elevation window closes on its next request.',
      })
    }
    catch (e) {
      setMessage({ type: 'error', text: formatErrorMessage(e, 'Failed to change the step-up setting') })
    }
    finally {
      setBusy(false)
      setElevating(null)
    }
  }

  // VerifyApp, from the list. Vouching states an administrator's judgement on
  // a consent screen strangers read, so it sits beside the badge it moves.
  const setVerified = async (app: App, verified: boolean) => {
    setBusy(true)
    setMessage(null)
    try {
      const resp = await appClient.verifyApp({ clientId: app.clientId, verified })
      patchRow(resp.app)
      setMessage({ type: 'success', text: verified ? 'Vouch recorded.' : 'Vouch withdrawn.' })
    }
    catch (e) {
      setMessage({ type: 'error', text: formatErrorMessage(e, 'Failed to change the vouch') })
    }
    finally {
      setBusy(false)
    }
  }

  const describe = (app: App) => {
    const parts: string[] = []
    if (app.createdAt)
      parts.push(`Registered ${timestampDate(app.createdAt).toLocaleDateString()}`)
    const live = Number(app.liveCredentialCount)
    if (live > 0)
      parts.push(`${live} live credential${live === 1 ? '' : 's'}`)
    return parts.join(' · ')
  }

  return (
    <>
      <div class="vstack gap-4" data-testid="app-registrations">
        <Show when={loading()}>
          <div class={styles.credentialListLoading}><Spinner /></div>
        </Show>
        <Show when={!loading() && !loadFailed() && apps().length === 0 && !creating()}>
          <p class={styles.credentialListEmpty}>
            No app registrations. Register one to let a program ask for access to accounts on this hub.
          </p>
        </Show>
        {/*
          The state of the hub-wide switch, stated where its effects land. It
          is a fact an administrator changed under Administration, Apps -- but
          reading it only there would put it beside no app at all, and the
          hub-wide rows this panel already lists for an administrator are the
          ones the switch admits company for.
        */}
        <Show when={!loading() && !loadFailed() && openRegistration()}>
          <p class={styles.credentialListEmpty} data-testid="open-registration-note">
            Open registration is on: an app can register itself at /oauth/register without an
            administrator. Administrators turn it off under Administration, Apps.
          </p>
        </Show>
        {/*
          Keyed by object identity, which setApps PRESERVES for unchanged rows
          (see refresh): <For> reconciles a refresh that changed nothing, and
          an open EditAppForm keeps its row -- and its typed edits -- through
          one.
        */}
        <For each={apps()}>
          {app => (
            <div class="vstack gap-3">
              <div class={styles.credentialRow} data-testid={`app-registration-${app.clientId}`}>
                <div class={styles.credentialInfo}>
                  <span class={styles.credentialName}>
                    {app.clientName || 'Unnamed app'}
                    {' '}
                    <Show
                      when={app.verified}
                      fallback={
                        // UNVERIFIED is the default and the consent screen warns
                        // about it, so the same fact appears here rather than
                        // only where a stranger meets it. The hub's one rule
                        // (a vouch or a built-in) travels in this field, so a
                        // built-in registration reads verified here exactly as
                        // the consent page renders it.
                        <span class={styles.credentialBadgeWarning}>unverified</span>
                      }
                    >
                      <span class={styles.credentialBadge}>
                        verified
                        {app.verifiedByUsername ? ` by ${app.verifiedByUsername}` : ''}
                      </span>
                    </Show>
                    <Show when={app.visibility === AppVisibility.HUB_WIDE}>
                      {' '}
                      <span class={styles.credentialBadge}>hub-wide</span>
                    </Show>
                    <Show when={app.elevationAllowed}>
                      {' '}
                      <span class={styles.credentialBadge}>step-up allowed</span>
                    </Show>
                    <Show when={app.revokedAt}>
                      {' '}
                      <span class={styles.credentialBadgeWarning}>retired</span>
                    </Show>
                  </span>
                  <span class={styles.credentialSubRow}>
                    <code>{app.clientId}</code>
                    <Show when={app.clientType === AppClientType.CONFIDENTIAL} fallback={<span>public app</span>}>
                      <span>confidential app</span>
                    </Show>
                  </span>
                  <span class={styles.credentialMeta}>{describe(app)}</span>
                  <Show when={ceiling(app).length > 0}>
                    <span class={styles.credentialScopeLine} data-testid={`app-ceiling-${app.clientId}`}>
                      <For each={ceiling(app)}>
                        {scope => <span class={styles.credentialScope}>{scope}</span>}
                      </For>
                    </span>
                  </Show>
                </div>
                <div class={styles.credentialActions}>
                  {/*
                    Edit rewrites where a consent redirects, which is the most
                    dangerous write in the feature -- so it sits behind the
                    row's elevation demand like every other write here, and a
                    built-in registration's fields are constants of the build
                    that the hub refuses to change.
                  */}
                  <Show
                    when={app.registrationSource !== 'builtin'}
                    fallback={(
                      <Tooltip text="This app ships with the hub, so it cannot be edited.">
                        <button type="button" disabled>Edit</button>
                      </Tooltip>
                    )}
                  >
                    <button
                      type="button"
                      onClick={() => setEditing(editing() === app.clientId ? null : app.clientId)}
                      disabled={busy() || app.revokedAt !== undefined}
                    >
                      {editing() === app.clientId ? 'Close' : 'Edit'}
                    </button>
                  </Show>
                  {/*
                    The step-up allowance is the one field a built-in
                    registration may still change, so it is offered on every
                    row. Allowing it multiplies what the app's grant reaches, so
                    it asks; refusing only reduces access, so it does not.
                  */}
                  <Show
                    when={app.elevationAllowed}
                    fallback={(
                      <button
                        type="button"
                        onClick={() => setElevating(app)}
                        disabled={busy() || app.revokedAt !== undefined}
                      >
                        Allow step-up
                      </button>
                    )}
                  >
                    <button
                      type="button"
                      onClick={() => void setElevation(app, false)}
                      disabled={busy() || app.revokedAt !== undefined}
                    >
                      Refuse step-up
                    </button>
                  </Show>
                  {/*
                    The vouch is an administrator's statement that the consent
                    screen may state a name without hedging. Withdrawing it is
                    the ordinary way to correct a mistake.
                  */}
                  <Show when={vouchAvailable()}>
                    <button
                      type="button"
                      onClick={() => void setVerified(app, !app.verified)}
                      disabled={busy() || app.revokedAt !== undefined}
                    >
                      {!app.verified ? 'Vouch' : 'Withdraw vouch'}
                    </button>
                  </Show>
                  {/*
                    A built-in registration's fields are constants of the build,
                    so the hub refuses to retire one. The control says so rather
                    than being offered and then failing.

                    The tooltip takes NO ariaLabel: the button already reads
                    "Retire", and ariaLabel would REPLACE that name with the whole
                    sentence -- a screen reader would announce a reason where the
                    verb belongs, and every by-name lookup would stop matching.
                    The reason reaches a screen-reader user through
                    aria-describedby instead, which is the only route to one on a
                    disabled control.
                  */}
                  <Show
                    when={app.registrationSource !== 'builtin'}
                    fallback={(
                      <Tooltip text="This app ships with the hub, so it cannot be retired.">
                        <button type="button" class={styles.credentialDanger} disabled>Retire</button>
                      </Tooltip>
                    )}
                  >
                    <button
                      type="button"
                      class={styles.credentialDanger}
                      onClick={() => setTarget(app)}
                      disabled={busy() || app.revokedAt !== undefined}
                    >
                      Retire
                    </button>
                  </Show>
                </div>
              </div>
              <Show when={editing() === app.clientId}>
                <EditAppForm
                  app={app}
                  busy={busy()}
                  onCancel={() => setEditing(null)}
                  onSaved={(text, app) => {
                    setEditing(null)
                    setMessage({ type: 'success', text })
                    patchRow(app)
                  }}
                  onError={text => setMessage({ type: 'error', text })}
                  setBusy={setBusy}
                />
              </Show>
            </div>
          )}
        </For>

        <Show
          when={creating()}
          fallback={(
            <div>
              <button type="button" onClick={() => setCreating(true)} disabled={busy()}>
                Register an app
              </button>
            </div>
          )}
        >
          <RegisterAppForm
            busy={busy()}
            onCancel={() => setCreating(false)}
            onRegistered={(text, app) => {
              setCreating(false)
              setMessage({ type: 'success', text })
              prependRow(app)
            }}
            onError={text => setMessage({ type: 'error', text })}
            setBusy={setBusy}
          />
        </Show>

        <StatusLine message={message()} />
      </div>

      <Show when={target()}>
        {app => (
          <ConfirmDialog
            title="Retire this app?"
            confirmLabel="Retire"
            danger
            busy={busy()}
            onCancel={() => setTarget(null)}
            onConfirm={() => void revoke(app())}
          >
            <p>
              {app().clientName || 'This app'}
              {' '}
              will stop working immediately, and every credential it holds — for
              every account on this hub — is revoked.
            </p>
          </ConfirmDialog>
        )}
      </Show>

      {/*
        Allowing the step-up stage is a widening write, so it asks first: a
        confirmed app can spend its whole grant on an elevated call, and that
        is a different decision from registering it.
      */}
      <Show when={elevating()}>
        {app => (
          <ConfirmDialog
            title="Allow the step-up stage?"
            confirmLabel="Allow"
            busy={busy()}
            onCancel={() => setElevating(null)}
            onConfirm={() => void setElevation(app(), true)}
          >
            <p>
              {app().clientName || 'This app'}
              {' '}
              will be able to ask whoever uses it for a fresh proof and then
              spend its whole permission grant on elevated calls, for as long
              as the elevation window lasts. Refusing it later closes every
              live window on the app's next request.
            </p>
          </ConfirmDialog>
        )}
      </Show>
    </>
  )
}
