import type { Component, JSX } from 'solid-js'
import type { StatusMessage } from '~/components/common/StatusLine'
import type { App } from '~/generated/proto/leapmux/v1/app_pb'
import type { Scope } from '~/generated/proto/leapmux/v1/scope_pb'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import { createMemo, createSignal, For, onMount, Show, untrack } from 'solid-js'
import { appClient } from '~/api/clients'
import { RelativeTimeAgo } from '~/components/chat/RelativeTime'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { ConfirmDialog } from '~/components/common/ConfirmDialog'
import { PillGroup } from '~/components/common/PillGroup'
import { Spinner } from '~/components/common/Spinner'
import { StatusLine } from '~/components/common/StatusLine'
import { Tooltip } from '~/components/common/Tooltip'
import { useAuth } from '~/context/AuthContext'
import { isGrantableScope } from '~/generated/contracts/scopes'
import { AppClientType, AppVisibility } from '~/generated/proto/leapmux/v1/app_pb'
import { useCopyButton } from '~/hooks/useCopyButton'
import { formatErrorMessage } from '~/lib/errors'
import * as styles from './credentialList.css'
import { fetchAllPages, MAX_PAGES, PAGE_SIZE } from './fetchAllPages'
import { closeScopes, impliedScopes, SCOPE_CATEGORIES } from './scopeCatalogue'
import { scopeToken } from './scopeToken'

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

  /**
   * The scopes the ticked set implies, whether or not they were ticked.
   *
   * A memo, not a plain function: the checkbox list reads it twice per row
   * (checked and disabled), so a plain function would re-run the fixed
   * point for every attribute of every row on every tick -- one derivation
   * per change is the whole point.
   *
   * The hub closes a grant at the mint (RegisterApp stores
   * `scopes.Close()`), so an implied scope renders CHECKED and DISABLED --
   * unticking it would state a boundary the hub cannot deliver -- and the
   * submitted set carries it, so what the owner ticked is exactly what the
   * next ListApps reads back.
   */
  const implied = createMemo(() => impliedScopes(scopes()))

  const redirectList = () =>
    redirects().split('\n').map(line => line.trim()).filter(line => line !== '')

  /** The submitted ceiling: the ticked set, closed the way the hub stores it. */
  const closedScopes = () => closeScopes(scopes())

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
        {/*
          Grouped the way scope.proto's own sections group it, each scope with
          the sentence its proto comment states: a ceiling is a decision, and a
          bare list of tokens makes the reader guess at what "workspace:write"
          reaches. The accessible name of each checkbox stays the bare token,
          which is the name a consent screen and a stored grant both read.
        */}
        <ul class={styles.scopeChoiceList}>
          <For each={SCOPE_CATEGORIES}>
            {category => (
              <li class={styles.scopeChoiceCategory}>
                <span class={styles.scopeChoiceLabel}>{category.label}</span>
                <ul class={styles.scopeChoiceEntries}>
                  <For each={category.entries}>
                    {({ scope, description }) => (
                      <li class={styles.scopeChoiceEntry}>
                        <label>
                          <input
                            type="checkbox"
                            checked={scopes().includes(scope) || implied().has(scope)}
                            disabled={implied().has(scope)}
                            onChange={() => toggleScope(scope)}
                          />
                          {' '}
                          <span class={styles.scopeChoiceToken}>{scopeToken(scope)}</span>
                        </label>
                        <span class={styles.scopeChoiceDescription}>{description}</span>
                      </li>
                    )}
                  </For>
                </ul>
              </li>
            )}
          </For>
        </ul>
      </fieldset>
    </>
  )

  return { Fields, name, uri, scopes, closedScopes, redirectList, setName }
}

/**
 * The shared register/edit footer: Cancel (outline, left) before the primary
 * verb (right), matching every dialog footer in the frontend.
 *
 * One component, because the two forms' footers were a verbatim pair -- the
 * shared disable rule included -- and a pair is where the next edit lands on
 * one side. The gate stays the caller's: both forms disable the primary on
 * the same empty-name-or-no-scopes condition, expressed once at each call
 * site from the fields it owns.
 */
function FormActions(props: {
  busy: boolean
  disabled: boolean
  label: string
  onCancel: () => void
  onSubmit: () => void
}) {
  return (
    <div class={actionsFooter}>
      <button type="button" class="outline" onClick={() => props.onCancel()} disabled={props.busy}>Cancel</button>
      <button type="button" onClick={() => props.onSubmit()} disabled={props.busy || props.disabled}>
        {props.label}
      </button>
    </div>
  )
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
  /**
   * What the registration reaches. PRIVATE is the user panel's constant; the
   * administration panel passes HUB_WIDE. It is a prop rather than a control
   * in the form because the hub refuses hub-wide for anybody but an
   * administrator -- a chooser most accounts are refused would be worse than
   * no chooser, so each panel states the one visibility its caller may mean.
   */
  visibility: AppVisibility
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
        // CLOSED, as the hub would close it anyway: the ceiling is stored
        // expanded, so submitting the bare ticked set and submitting this
        // differ only in who did the arithmetic.
        scopes: fields.closedScopes(),
        visibility: props.visibility,
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
            {/*
              CANCEL then the primary, matching every dialog footer in the
              frontend: the quiet outline escape on the left, the verb that
              does the thing on the right.
            */}
            <FormActions
              busy={props.busy}
              disabled={fields.name().trim() === '' || fields.scopes().length === 0}
              label="Register"
              onCancel={props.onCancel}
              onSubmit={() => void submit()}
            />
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
        <div class={actionsFooter}>
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
        // CLOSED, for the same reason the register path closes: the hub
        // stores the expanded ceiling, and the edit pre-fills from what it
        // stored -- a bare ticked set here would strip the implied scopes on
        // every round trip through this form.
        scopes: fields.closedScopes(),
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
      <FormActions
        busy={props.busy}
        disabled={fields.name().trim() === '' || fields.scopes().length === 0}
        label="Save"
        onCancel={props.onCancel}
        onSubmit={() => void submit()}
      />
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
 *
 * TWO panels render this one editor. The default (the user-level Apps
 * section) registers PRIVATE and lists what the caller's own listing answers.
 * The `hub-wide` variant (the administration section) registers HUB_WIDE and
 * asks the hub for the catalogue alone -- the registrations that reach
 * everybody -- so an administrator reads one catalogue, not their private
 * apps twice.
 */
export interface AppRegistrationsProps {
  /** `'hub-wide'` lists and registers the hub's own registrations. */
  variant?: 'user' | 'hub-wide'
}

export const AppRegistrations: Component<AppRegistrationsProps> = (props) => {
  const auth = useAuth()
  const hubWide = () => (props.variant ?? 'user') === 'hub-wide'
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
      // The reach rides the request, read ONCE before the pagination: the
      // variant is fixed for the panel's life, and a reactive read inside
      // the untracked page callback would re-evaluate per page for nothing.
      // eslint-disable-next-line solid/reactivity -- read once before the pagination on purpose, as the comment above states
      const reach = hubWide() ? AppVisibility.HUB_WIDE : undefined
      const collected = await fetchAllPages(
        async (cursor) => {
          // includeRevoked: a retired app stays readable because a live
          // credential on one must still be explainable (app.proto states the
          // contract), and the retired badge and the gates below key on it --
          // without the flag the row vanishes on the refresh that retires it.
          const resp = await appClient.listApps({
            cursor,
            limit: PAGE_SIZE,
            includeRevoked: true,
            visibility: reach,
          })
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
   * that lives on the credential and is what Connected apps shows. The hub
   * mints ceilings from grantable scopes only, so a value the guard drops is
   * hub corruption worth a loud log, not a row to render raw.
   */
  const ceiling = (app: App) => {
    const grantable = app.scopes.filter(isGrantableScope)
    if (grantable.length !== app.scopes.length)
      console.error(`registration ${app.clientId} carries a non-grantable scope`, app.scopes)
    return grantable.map(scopeToken)
  }

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

  /**
   * The live-credential count, the one fact the row states beside the date.
   * Empty when there are none, so the meta line renders no bare separator.
   */
  const liveCredentialNote = (app: App) => {
    const live = Number(app.liveCredentialCount)
    return live > 0 ? `${live} live credential${live === 1 ? '' : 's'}` : ''
  }

  return (
    <>
      <div class="vstack gap-4" data-testid={hubWide() ? 'hub-wide-app-registrations' : 'app-registrations'}>
        <Show when={loading()}>
          <div class={styles.credentialListLoading}><Spinner /></div>
        </Show>
        <Show when={!loading() && !loadFailed() && apps().length === 0 && !creating()}>
          <p class={styles.credentialListEmpty}>
            {hubWide()
              ? 'No hub-wide app registrations. Register one to let every account on this hub authorize it.'
              : 'No app registrations. Register one to let a program ask for access to accounts on this hub.'}
          </p>
        </Show>
        {/*
          The state of the hub-wide switch, stated where its effects land. It
          is a fact an administrator changed under Administration › Hub-wide
          Apps -- but reading it only there would put it beside no app at all,
          and the rows a listing already carries are the ones the switch
          admits company for. The path separator is ›, the same character the
          Preferences search breadcrumb spells paths with.
        */}
        <Show when={!loading() && !loadFailed() && openRegistration()}>
          <p class={styles.credentialListEmpty} data-testid="open-registration-note">
            Open registration is on: an app can register itself at /oauth/register without an
            administrator. Administrators turn it off under Administration › Hub-wide Apps.
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
                  {/*
                    The name's line carries every chip that QUALIFIES the name
                    -- the vouch, the reach, the app's kind -- so one glance
                    answers "what am I looking at" without a second line.
                  */}
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
                        <span class="badge" data-variant="warning">unverified</span>
                      }
                    >
                      <span class="badge" data-variant="success">
                        verified
                        {app.verifiedByUsername ? ` by ${app.verifiedByUsername}` : ''}
                      </span>
                    </Show>
                    {/*
                      The reach badge, absent from the hub-wide panel: every
                      row there reaches everybody, so the badge would repeat
                      the section's own title once per row.
                    */}
                    <Show when={app.visibility === AppVisibility.HUB_WIDE && !hubWide()}>
                      {' '}
                      <span class="badge outline">hub-wide</span>
                    </Show>
                    <Show when={app.elevationAllowed}>
                      {' '}
                      <span class="badge outline">step-up allowed</span>
                    </Show>
                    <Show when={app.revokedAt}>
                      {' '}
                      <span class="badge" data-variant="danger">retired</span>
                    </Show>
                    {' '}
                    <Show
                      when={app.clientType === AppClientType.CONFIDENTIAL}
                      fallback={<span class="badge outline">public app</span>}
                    >
                      <span class="badge outline">confidential app</span>
                    </Show>
                  </span>
                  {/*
                    The metadata line, every part delimited by a middot: when
                    it was registered, the registration date in the app's
                    relative form (the same <RelativeTimeAgo> the chat and the
                    file tree render, with the full local date and time on
                    hover), the client id a developer copies into a config,
                    and the live-credential count. A bare toLocaleDateString
                    answered "8/29/2026" to a reader whose real question was
                    "how long has this been here".
                  */}
                  <span class={styles.credentialMeta}>
                    <Show when={app.createdAt}>
                      {ts => (
                        <>
                          Registered
                          {' '}
                          <RelativeTimeAgo timestamp={timestampDate(ts()).toISOString()} />
                          {' · '}
                        </>
                      )}
                    </Show>
                    <code>{app.clientId}</code>
                    <Show when={liveCredentialNote(app)}>
                      {note => (
                        <>
                          {' · '}
                          {note()}
                        </>
                      )}
                    </Show>
                  </span>
                  <Show when={ceiling(app).length > 0}>
                    <span class={styles.credentialScopeLine} data-testid={`app-ceiling-${app.clientId}`}>
                      <For each={ceiling(app)}>
                        {scope => <span class={styles.credentialScope}>{scope}</span>}
                      </For>
                    </span>
                  </Show>
                </div>
                <div class={actionsFooter}>
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

                    An Oat danger OUTLINE, not a filled button: retiring asks in
                    a dialog of its own, and this control only opens it -- the
                    dialog's primary is where the danger weight belongs. The
                    tooltip takes NO ariaLabel: the button already reads
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
                        <button type="button" class="outline" data-variant="danger" disabled>Retire</button>
                      </Tooltip>
                    )}
                  >
                    <button
                      type="button"
                      class="outline"
                      data-variant="danger"
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
            <div class={actionsFooter}>
              <button type="button" onClick={() => setCreating(true)} disabled={busy()}>
                {hubWide() ? 'Register a hub-wide app' : 'Register an app'}
              </button>
            </div>
          )}
        >
          <RegisterAppForm
            busy={busy()}
            visibility={hubWide() ? AppVisibility.HUB_WIDE : AppVisibility.PRIVATE}
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
