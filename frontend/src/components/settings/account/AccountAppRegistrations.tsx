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
 * One scope's RFC 6749 section 3.3 token, from the generated enum name.
 *
 * SCOPE_WORKSPACE_READ becomes workspace:read: the first underscore-separated
 * part is the family and the rest is the action. The Go side builds its tokens
 * from an explicit bijection (grantableTokens in internal/authscope) rather
 * than from the enum names, so this derivation matches it by convention, not
 * by construction -- and TestScopeTokenBijectionMatchesEnumNames pins the two
 * together on the Go side, failing the suite the moment a token stops
 * following the FAMILY_ACTION shape this function assumes.
 */
function scopeToken(scope: Scope): string {
  const name = Scope[scope]
  if (typeof name !== 'string')
    return ''
  const parts = name.split('_')
  if (parts.length < 2)
    return ''
  return `${parts[0]!.toLowerCase()}:${parts.slice(1).join('_').toLowerCase()}`
}

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
  onRegistered: (message: string) => void
  onError: (message: string) => void
  setBusy: (value: boolean) => void
}> = (props) => {
  const fields = createAppFormFields()
  const [clientType, setClientType] = createSignal(AppClientType.PUBLIC)
  // The secret, once. The hub stores only its hash, so this signal is the
  // ONLY place it ever exists on this machine -- which is why the form stays
  // open showing it rather than closing on success like every other write.
  const [secret, setSecret] = createSignal('')
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
        return
      }
      props.onRegistered('App registered.')
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
          <button type="button" onClick={() => props.onRegistered('App registered.')}>Done</button>
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
  onSaved: (message: string) => void
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
      await appClient.updateApp({
        clientId: props.app.clientId,
        clientName: fields.name().trim(),
        clientUri: fields.uri().trim(),
        replaceRedirectUris: true,
        redirectUris: fields.redirectList(),
        replaceScopes: true,
        scopes: fields.scopes(),
      })
      props.onSaved('App updated.')
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

  const refresh = async () => {
    setLoading(true)
    setLoadFailed(false)
    try {
      // EVERY page, like the Connected-apps panel beside it: an administrator
      // with open registration on can pass the default page size without
      // lifting a finger, and a registration the panel never drew was one
      // whose Edit and Retire controls did not exist.
      const MAX_PAGES = 20
      const collected: App[] = []
      const seen = new Set<string>()
      let cursor: string | undefined
      for (let page = 0; page < MAX_PAGES; page++) {
        const resp = await appClient.listApps({ cursor, limit: 100 })
        for (const app of resp.apps) {
          if (!seen.has(app.clientId)) {
            seen.add(app.clientId)
            collected.push(app)
          }
        }
        setOpenRegistration(resp.openRegistrationEnabled)
        cursor = resp.nextCursor === '' ? undefined : resp.nextCursor
        if (cursor === undefined)
          break
      }
      setApps(collected)
    }
    catch (e) {
      setLoadFailed(true)
      setMessage({ type: 'error', text: formatErrorMessage(e, 'Failed to load app registrations') })
    }
    finally {
      setLoading(false)
    }
  }

  onMount(() => {
    void refresh()
  })

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
  const ceiling = (app: App) => app.scopes.map(scopeToken).filter(t => t !== '')

  // SetAppElevationAllowed, from the list. It is the one field even a
  // built-in registration may change, so -- unlike Edit -- it is offered on
  // every row.
  const setElevation = async (app: App, allowed: boolean) => {
    setBusy(true)
    setMessage(null)
    try {
      await appClient.setAppElevationAllowed({ clientId: app.clientId, allowed })
      await refresh()
      setMessage({
        type: 'success',
        text: allowed
          ? 'The app may now run the step-up leg.'
          : 'The app can no longer run the step-up leg. Any live elevation window closes on its next request.',
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
      await appClient.verifyApp({ clientId: app.clientId, verified })
      await refresh()
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
        <For each={apps()}>
          {app => (
            <div class="vstack gap-3">
              <div class={styles.credentialRow} data-testid={`app-registration-${app.clientId}`}>
                <div class={styles.credentialInfo}>
                  <span class={styles.credentialName}>
                    {app.clientName || 'Unnamed app'}
                    {' '}
                    <Show
                      when={app.verifiedAt}
                      fallback={
                        // UNVERIFIED is the default and the consent screen warns
                        // about it, so the same fact appears here rather than
                        // only where a stranger meets it.
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
                      onClick={() => void setVerified(app, app.verifiedAt === undefined)}
                      disabled={busy() || app.revokedAt !== undefined}
                    >
                      {app.verifiedAt === undefined ? 'Vouch' : 'Withdraw vouch'}
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
                  onSaved={(text) => {
                    setEditing(null)
                    setMessage({ type: 'success', text })
                    void refresh()
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
            onRegistered={(text) => {
              // NOT async: this is a tracked scope, and Solid's reactivity
              // follows synchronous reads alone. The refresh runs detached.
              setCreating(false)
              setMessage({ type: 'success', text })
              void refresh()
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
        Allowing the step-up leg is a widening write, so it asks first: a
        confirmed app can spend its whole grant on an elevated call, and that
        is a different decision from registering it.
      */}
      <Show when={elevating()}>
        {app => (
          <ConfirmDialog
            title="Allow the step-up leg?"
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
