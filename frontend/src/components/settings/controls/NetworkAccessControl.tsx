import type { CustomEditorComponent } from '../types'
import type { AddressRow } from './networkAddress'
import type { GetListenStatusResponse } from '~/generated/proto/leapmux/v1/admin_pb'
import { createEffect, createSignal, For, onMount, Show, untrack } from 'solid-js'
import { createStore } from 'solid-js/store'
import { adminNetworkClient, userClient } from '~/api/clients'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { Alert } from '~/components/common/Alert'
import { DropdownMenu, DropdownMenuCheckableItem } from '~/components/common/DropdownMenu'
import { passwordCanSubmit, PasswordFields } from '~/components/common/PasswordFields'
import { Spinner } from '~/components/common/Spinner'
import { StatusLine } from '~/components/common/StatusLine'
import { useAuth } from '~/context/AuthContext'
import { ADDRESS_SOURCE_LISTEN, ADDRESS_SOURCE_MERGED, MAX_EXTRA_LISTEN_ADDRESSES } from '~/generated/contracts/listen'
import { isValidPort } from '~/lib/ipAddress'
import { loadSystemInfo, soloPasswordSet } from '~/lib/systemInfo'
import { fieldLabel } from '../account/accountFields.css'
import { createAccountAction } from '../account/createAccountAction'
import * as styles from './NetworkAccessControl.css'
import { ANY_HOST, mergeNotes, newRow, portOf, rowFromAddress, rowIsLoopback, toAddress } from './networkAddress'

/**
 * The Network access panel: which addresses this hub answers on, and the
 * password that guards them.
 *
 * The two belong in ONE form because they are one decision. Publishing an
 * address without a password would put the whole app behind nothing, and the
 * hub's rule says as much: once the account holds a password every network
 * address asks for it, so the panel refuses to apply an address until one
 * exists.
 *
 * It writes the address list through the row's BINDING -- the same admin
 * settings store the rest of the dialog uses -- so the list has one home and
 * the row's own Customized/Reset chrome stays correct. The bespoke RPC beside
 * it is a READ: the machine's interfaces and what the sockets actually hold,
 * neither of which a settings row can carry.
 */
export const NetworkAccessControl: CustomEditorComponent = (props) => {
  const auth = useAuth()
  const [status, setStatus] = createSignal<GetListenStatusResponse | null>(null)
  const [loading, setLoading] = createSignal(true)
  const [loadFailed, setLoadFailed] = createSignal(false)
  /*
   * A STORE, not a signal of an array. `<For>` reconciles by reference, so
   * replacing a row object on every keystroke would rebuild that row's DOM --
   * and a keyboard user editing the port would lose focus to the document body
   * between characters. A store updates the one field in place.
   */
  const [state, setState] = createStore<{ rows: AddressRow[] }>({ rows: [] })
  const rows = () => state.rows
  const [password, setPassword] = createSignal('')
  const [confirmPassword, setConfirmPassword] = createSignal('')
  /*
   * The same in-flight-and-outcome pair the Account rows keep, from the same
   * helper: this panel writes the account's password, so a divergent copy here
   * would be the fifth spelling of one behaviour. `run` clears `busy` only
   * after `work` resolves, which is what keeps Apply disabled through the
   * read-back below rather than through the write alone.
   */
  const action = createAccountAction()
  const busy = action.running
  /**
   * Whether THIS apply already stored the password, so a retry after a refused
   * address write does not store it again. See `apply`.
   */
  const [passwordStored, setPasswordStored] = createSignal(false)

  const pwProps = { password, confirmPassword }

  /** The stored list, from the settings store the panel writes through. */
  const storedAddresses = (): string[] => {
    const value = props.binding.value()
    if (typeof value !== 'object' || value === null)
      return []
    const addresses = (value as { addresses?: unknown }).addresses
    return Array.isArray(addresses) ? addresses.filter(a => typeof a === 'string') : []
  }

  const refresh = async () => {
    setLoading(true)
    try {
      const resp = await adminNetworkClient.getListenStatus({})
      setStatus(resp)
      setLoadFailed(false)
    }
    catch {
      // A separate flag, never an empty list: "this hub answers nowhere" and
      // "the hub did not answer" look identical otherwise, and the first is
      // alarming while the second is a hiccup.
      //
      // The FLAG alone, never the shared status line. `apply` reads the hub
      // back after it writes, and writing the failure there let the success
      // message that follows overwrite it -- the panel then showed a green
      // "Network access updated." beside a "The hub did not report its
      // listeners." it had just replaced. The Serving-now list states this
      // failure, which is the one place it belongs.
      setLoadFailed(true)
    }
    finally {
      setLoading(false)
    }
  }

  /**
   * The rows follow the STORED list whenever the panel is not mid-edit.
   *
   * The row above this editor renders its own Reset, which clears the stored
   * document. Seeding once at mount left the deleted addresses on screen after
   * that Reset, the preview promising to publish them, and the next Apply
   * writing them straight back -- silently undoing what the operator had just
   * done.
   *
   * `dirty` is what keeps the effect from overwriting an edit in progress: a
   * write of this key publishes a new snapshot, which re-runs this, and a
   * half-typed port must survive that. Apply and Cancel both clear it, so the
   * rows re-seed from what the hub actually stored.
   */
  const [dirty, setDirty] = createSignal(false)
  createEffect(() => {
    const stored = storedAddresses()
    if (!untrack(dirty))
      setState('rows', stored.map(rowFromAddress))
  })

  onMount(() => {
    void refresh()
  })

  const addRow = () => {
    // Seeded with the port -listen already uses, because publishing the hub on
    // the port it already answers on is the common case and the merge handles
    // the overlap.
    const defaultPort = portOf(status()?.defaultAddress ?? '')
    setDirty(true)
    setState('rows', state.rows.length, newRow(ANY_HOST, isValidPort(defaultPort) ? defaultPort : '4327'))
  }

  /**
   * Whether another row may be added.
   *
   * The cap is the hub's, from the contract both sides generate from. Without
   * it the button built a row the settings validator then refused, so the
   * operator learned the limit from a rejected Apply.
   */
  const canAddRow = () => rows().length < MAX_EXTRA_LISTEN_ADDRESSES

  const removeRow = (id: number) => {
    setDirty(true)
    setState('rows', prev => prev.filter(r => r.id !== id))
  }
  const updateRow = (id: number, patch: Partial<AddressRow>) => {
    setDirty(true)
    setState('rows', r => r.id === id, patch)
  }

  const rowsAreValid = () => rows().every(r => r.host !== '' && isValidPort(r.port))
  const exposesTheHub = () => rows().some(r => !rowIsLoopback(r))
  /**
   * The password half is shown only while the account has none, and then only
   * because this panel cannot apply an address without one. Preferences →
   * Account → Password sets and replaces it at any time; this half exists so
   * that publishing an address does not send the operator to another category
   * mid-edit, and it disappears the moment either surface stores one.
   */
  const needsPassword = () => !soloPasswordSet()
  const passwordSatisfied = () =>
    !needsPassword() || !exposesTheHub() || passwordCanSubmit(pwProps)

  const canApply = () => !busy() && rowsAreValid() && passwordSatisfied()

  /**
   * The standing warning, in the three states the rule has.
   *
   * It reads the SAME two predicates Apply does, because a warning that
   * demands what the button does not is worse than no warning: an operator
   * adding a loopback address was told to set a password first, beside an
   * enabled Apply that never asked for one.
   */
  const warning = () => {
    if (!needsPassword()) {
      return 'Every network address asks for a sign-in as “solo”, 127.0.0.1 included. '
        + 'The desktop app’s local socket is the only exception.'
    }
    if (exposesTheHub()) {
      return 'Applying this asks every network address for a sign-in, 127.0.0.1 included. '
        + 'The desktop app’s local socket is the only exception. Set the password below first.'
    }
    return 'While the “solo” account has no password, this hub authenticates everyone who can reach it. '
      + 'An address other machines can reach demands one, and Apply asks for it then.'
  }

  const apply = async () => {
    if (!canApply())
      return
    await action.run({
      fallback: 'Failed to apply the network settings',
      work: async () => {
        // The password FIRST. A failure here leaves no address published, where
        // the reverse would publish an address nobody can authenticate against.
        // The reply carries this browser's new session: storing the first
        // password is what starts demanding one, so without it the page that
        // made the write is signed out of the form it is standing in.
        //
        // LATCHED, and the fields are cleared only once the whole apply lands.
        // Clearing them beside this call left the operator with no way to retry
        // a REFUSED address write: the password was stored, the panel's snapshot
        // still said it was not, and Apply demanded a password whose fields it
        // had just emptied. The latch is what keeps the retry from re-hashing
        // and revoking credentials for a password the account already holds.
        if (needsPassword() && !passwordStored() && passwordCanSubmit(pwProps)) {
          await userClient.changePassword({ newPassword: password() })
          setPasswordStored(true)
        }
        await props.binding.set({ addresses: rows().map(toAddress) })
        setPassword('')
        setConfirmPassword('')
        setPasswordStored(false)
        // The rows follow the stored list again: what the hub kept is now the
        // answer, and a bind failure it reports below belongs to that list.
        setDirty(false)
        // Read the hub back rather than trusting the request: a stored address
        // the hub could not bind is reported here, and the password half has to
        // disappear now that one exists.
        //
        // AWAITED inside the work, so Apply stays disabled until the panel shows
        // what the hub kept. allSettled, because BOTH writes already committed:
        // loadSystemInfo rejects by design, so a transient failure of the
        // read-back -- likeliest right here, on a connection whose
        // authentication rule just changed -- reported "Failed to apply the
        // network settings" for an apply that succeeded. Each read states its
        // own failure where it belongs.
        //
        // The ACCOUNT joins them because this panel can store the account's
        // password, and Account → Password reads the account to choose between
        // "Set Password" and "Change Password". Without it that row offers to
        // set a first password this Apply already stored. It runs on every
        // apply, including one that stored none: the alternative reads the
        // `passwordStored` latch before the reset below clears it, and one
        // cheap read on a hand-driven admin action does not earn that ordering.
        await Promise.allSettled([refresh(), loadSystemInfo(true), auth.refreshUser()])
        return 'Network access updated.'
      },
    })
  }

  const cancel = () => {
    setDirty(false)
    setState('rows', storedAddresses().map(rowFromAddress))
    setPassword('')
    setConfirmPassword('')
    setPasswordStored(false)
    action.clear()
  }

  return (
    <div class="vstack gap-4">
      <Alert variant="warning">{warning()}</Alert>

      <div class="vstack gap-2">
        <div class={styles.sectionHeading}>Serving now</div>
        <Show when={loading()} fallback={<ServingList status={status()} failed={loadFailed()} />}>
          <Spinner />
        </Show>
      </div>

      <div class="vstack gap-2">
        <div class={styles.sectionHeading}>Additional addresses</div>
        <Show
          when={rows().length > 0}
          fallback={<div class={styles.servingNote}>No additional addresses. This hub answers on the address -listen gives it.</div>}
        >
          <For each={rows()}>
            {row => (
              <div class={styles.addressRow} data-testid="network-address-row">
                <InterfaceMenu
                  host={row.host}
                  status={status()}
                  onSelect={host => updateRow(row.id, { host })}
                />
                <input
                  type="text"
                  inputMode="numeric"
                  class={styles.portInput}
                  aria-label="Port"
                  aria-invalid={row.port !== '' && !isValidPort(row.port)}
                  value={row.port}
                  onInput={e => updateRow(row.id, { port: e.currentTarget.value })}
                />
                <button
                  type="button"
                  class="outline"
                  data-variant="danger"
                  aria-label={`Remove ${toAddress(row)}`}
                  onClick={() => removeRow(row.id)}
                >
                  ✕
                </button>
              </div>
            )}
          </For>
        </Show>
        <div>
          <button type="button" class="outline small" onClick={addRow} disabled={!canAddRow()}>+ Add address</button>
          <Show when={!canAddRow()}>
            <span class={styles.servingNote}>
              {` At most ${MAX_EXTRA_LISTEN_ADDRESSES} addresses. Use “All interfaces” to serve every one of them from a single entry.`}
            </span>
          </Show>
        </div>
      </div>

      <Show when={rows().length > 0 && rowsAreValid()}>
        <div class={styles.preview}>
          <div>
            After apply:
            {' '}
            <span class={styles.previewAddresses}>{rows().map(toAddress).join(', ')}</span>
          </div>
          <MergeNotes defaultAddress={status()?.defaultAddress ?? ''} rows={rows()} />
        </div>
      </Show>

      <Show
        when={needsPassword()}
        fallback={(
          <div class={styles.servingNote}>
            The “solo” account has a password. Change it in Account → Password.
          </div>
        )}
      >
        <div class="vstack gap-2">
          <div class={styles.sectionHeading}>Sign-in password for the “solo” account</div>
          <PasswordFields
            password={password}
            setPassword={setPassword}
            confirmPassword={confirmPassword}
            setConfirmPassword={setConfirmPassword}
            labelClass={fieldLabel}
          />
        </div>
      </Show>

      <StatusLine message={action.message()} />

      <div class={actionsFooter}>
        <button type="button" class="outline" onClick={cancel} disabled={busy()}>Cancel</button>
        <button type="button" onClick={() => void apply()} disabled={!canApply()}>
          {busy() ? 'Applying...' : 'Apply'}
        </button>
      </div>
    </div>
  )
}

/** The read-only list of what the hub answers on right now. */
function ServingList(props: { status: GetListenStatusResponse | null, failed: boolean }) {
  // The tokens come from the contract the hub generates its own from, so the
  // two sides cannot drift on three words the proto carries as a plain string.
  const sourceNote = (source: string) => {
    switch (source) {
      case ADDRESS_SOURCE_LISTEN:
        return 'from -listen'
      // The distinction the panel exists to make legible: the -listen address
      // is not gone, it is served by this wider one.
      case ADDRESS_SOURCE_MERGED:
        return 'from -listen, merged'
      default:
        return 'extra'
    }
  }
  return (
    <Show
      when={!props.failed}
      fallback={<div class={styles.servingNote}>The hub did not report its listeners.</div>}
    >
      <Show
        when={(props.status?.bound.length ?? 0) > 0}
        fallback={<div class={styles.servingNote}>This hub answers on its local socket only.</div>}
      >
        <ul class={styles.servingList}>
          <For each={props.status?.bound ?? []}>
            {bound => (
              <li class={styles.servingRow}>
                <span class={styles.servingAddress}>{bound.address}</span>
                <Show
                  when={bound.error === ''}
                  fallback={<span class="badge" data-variant="danger">{`bind failed: ${bound.error}`}</span>}
                >
                  <span class={styles.servingNote}>{sourceNote(bound.source)}</span>
                </Show>
              </li>
            )}
          </For>
        </ul>
      </Show>
    </Show>
  )
}

/**
 * States which address the hub will absorb into which.
 *
 * Without it "Serving now" shows 127.0.0.1:4327 before Apply and only *:4327
 * after, which reads as an address that disappeared.
 *
 * `mergeNotes` is the browser's copy of the hub's merge rule, and
 * `testdata/listen_merge_conformance.json` keeps the two from drifting.
 */
function MergeNotes(props: { defaultAddress: string, rows: AddressRow[] }) {
  const notes = () => mergeNotes(props.defaultAddress, props.rows)
  return (
    <For each={notes()}>
      {note => <div>{`${note.absorbed} merges into ${note.into}.`}</div>}
    </For>
  )
}

/**
 * The interface picker: "All interfaces" plus every address this machine
 * holds, grouped by interface.
 *
 * A DropdownMenu of radio items, never a native `<select>`: the options carry
 * a second line and an interface grouping, which a `<select>` renders as
 * nothing at all.
 */
function InterfaceMenu(props: {
  host: string
  status: GetListenStatusResponse | null
  onSelect: (host: string) => void
}) {
  const label = () => (props.host === ANY_HOST ? 'All interfaces' : props.host)
  return (
    <DropdownMenu
      trigger={triggerProps => (
        <button
          type="button"
          class={`outline ${styles.interfaceTrigger}`}
          data-testid="network-interface-trigger"
          {...triggerProps}
        >
          <span class="clipped">{label()}</span>
          <span aria-hidden="true">▾</span>
        </button>
      )}
    >
      <DropdownMenuCheckableItem
        kind="radio"
        label="All interfaces"
        checked={props.host === ANY_HOST}
        onSelect={() => props.onSelect(ANY_HOST)}
      />
      <For each={props.status?.interfaces ?? []}>
        {iface => (
          <>
            <li class={styles.menuGroupHeader} role="presentation">
              {iface.name}
              <Show when={!iface.up}>{' (down)'}</Show>
            </li>
            <For each={iface.addresses}>
              {addr => (
                <DropdownMenuCheckableItem
                  kind="radio"
                  // The marker goes in the LABEL, which is the option's
                  // accessible name, rather than beside it: whether an address
                  // is loopback is what decides if publishing it demands a
                  // password, so it must reach a screen reader too.
                  label={addr.loopback ? `${addr.ip} (loopback)` : addr.ip}
                  checked={props.host === addr.ip}
                  onSelect={() => props.onSelect(addr.ip)}
                />
              )}
            </For>
          </>
        )}
      </For>
    </DropdownMenu>
  )
}
