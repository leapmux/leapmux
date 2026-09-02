import type { CustomEditorComponent } from '../types'
import type { GetListenStatusResponse } from '~/generated/proto/leapmux/v1/admin_pb'
import { createSignal, For, onMount, Show } from 'solid-js'
import { createStore } from 'solid-js/store'
import { adminNetworkClient, userClient } from '~/api/clients'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { Alert } from '~/components/common/Alert'
import { DropdownMenu, DropdownMenuCheckableItem } from '~/components/common/DropdownMenu'
import { passwordCanSubmit, PasswordFields } from '~/components/common/PasswordFields'
import { Spinner } from '~/components/common/Spinner'
import { StatusLine } from '~/components/common/StatusLine'
import { formatErrorMessage } from '~/lib/errors'
import { loadSystemInfo, soloPasswordSet } from '~/lib/systemInfo'
import { fieldLabel } from '../account/accountFields.css'
import * as styles from './NetworkAccessControl.css'

/**
 * The host every "All interfaces" row carries.
 *
 * The hub's own spelling: `listenset` canonicalises an empty host and `*` to
 * this, so a row built here and a row read back from the hub compare equal.
 */
const ANY_HOST = '*'

/** One editable address in the panel. */
interface AddressRow {
  /** Stable across re-renders, so `<For>` does not rebuild a row being typed in. */
  id: number
  /** `*` for every interface, else an IP literal (with a zone where it has one). */
  host: string
  /** Kept as TEXT: a half-typed port is not a number, and clearing the field is not 0. */
  port: string
}

let nextRowID = 0

/** Splits a canonical `host:port` back into the two fields the row edits. */
function rowFromAddress(address: string): AddressRow {
  const lastColon = address.lastIndexOf(':')
  if (lastColon < 0)
    return { id: nextRowID++, host: address, port: '' }
  const host = address.slice(0, lastColon)
  return {
    id: nextRowID++,
    // An IPv6 literal arrives bracketed inside an address and unbracketed on
    // its own; the row edits the bare form and `toAddress` brackets it back.
    host: host.startsWith('[') && host.endsWith(']') ? host.slice(1, -1) : host,
    port: address.slice(lastColon + 1),
  }
}

/** Renders one row as the canonical address the hub stores. */
function toAddress(row: AddressRow): string {
  const host = row.host.includes(':') ? `[${row.host}]` : row.host
  return `${host}:${row.port}`
}

/** A port a client can actually connect to. */
function portIsValid(port: string): boolean {
  if (!/^\d{1,5}$/.test(port))
    return false
  const n = Number(port)
  return n >= 1 && n <= 65535
}

/** Whether a row names an address only this machine can reach. */
function rowIsLoopback(row: AddressRow, status: GetListenStatusResponse | null): boolean {
  if (row.host === ANY_HOST)
    return false
  for (const iface of status?.interfaces ?? []) {
    for (const addr of iface.addresses) {
      if (addr.ip === row.host)
        return addr.loopback
    }
  }
  // An address this machine no longer holds is treated as exposed. The safe
  // answer to "could another machine reach this" is yes.
  return false
}

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
  const [busy, setBusy] = createSignal(false)
  const [message, setMessage] = createSignal<{ type: 'success' | 'error', text: string } | null>(null)

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
    catch (e) {
      // A separate flag, never an empty list: "this hub answers nowhere" and
      // "the hub did not answer" look identical otherwise, and the first is
      // alarming while the second is a hiccup.
      setLoadFailed(true)
      setMessage({ type: 'error', text: formatErrorMessage(e, 'Failed to read the network status') })
    }
    finally {
      setLoading(false)
    }
  }

  onMount(() => {
    setState('rows', storedAddresses().map(rowFromAddress))
    void refresh()
  })

  const addRow = () => {
    // Seeded with the port -listen already uses, because publishing the hub on
    // the port it already answers on is the common case and the merge handles
    // the overlap.
    const defaultPort = status()?.defaultAddress?.split(':').pop() ?? '4327'
    setState('rows', state.rows.length, { id: nextRowID++, host: ANY_HOST, port: portIsValid(defaultPort) ? defaultPort : '4327' })
  }

  const removeRow = (id: number) => setState('rows', prev => prev.filter(r => r.id !== id))
  const updateRow = (id: number, patch: Partial<AddressRow>) =>
    setState('rows', r => r.id === id, patch)

  const rowsAreValid = () => rows().every(r => r.host !== '' && portIsValid(r.port))
  const exposesTheHub = () => rows().some(r => !rowIsLoopback(r, status()))
  /**
   * The password half is shown only while the account has none. Once it does,
   * Preferences → Account owns changing it, so the two surfaces never offer
   * the same field at the same time.
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
    setBusy(true)
    setMessage(null)
    try {
      // The password FIRST. A failure here leaves no address published, where
      // the reverse would publish an address nobody can authenticate against.
      // The reply carries this browser's new session: storing the first
      // password is what starts demanding one, so without it the page that
      // made the write is signed out of the form it is standing in.
      if (needsPassword() && passwordCanSubmit(pwProps)) {
        await userClient.changePassword({ newPassword: password() })
        setPassword('')
        setConfirmPassword('')
      }
      await props.binding.set({ addresses: rows().map(toAddress) })
      // Read the hub back rather than trusting the request: a stored address
      // the hub could not bind is reported here, and the password half has to
      // disappear now that one exists.
      await Promise.all([refresh(), loadSystemInfo(true)])
      setMessage({ type: 'success', text: 'Network access updated.' })
    }
    catch (e) {
      setMessage({ type: 'error', text: formatErrorMessage(e, 'Failed to apply the network settings') })
    }
    finally {
      setBusy(false)
    }
  }

  const cancel = () => {
    setState('rows', storedAddresses().map(rowFromAddress))
    setPassword('')
    setConfirmPassword('')
    setMessage(null)
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
                  aria-invalid={row.port !== '' && !portIsValid(row.port)}
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
          <button type="button" class="outline small" onClick={addRow}>+ Add address</button>
        </div>
      </div>

      <Show when={rows().length > 0 && rowsAreValid()}>
        <div class={styles.preview}>
          <div>
            After apply:
            {' '}
            <span class={styles.previewAddresses}>{rows().map(toAddress).join(', ')}</span>
          </div>
          <MergeNote defaultAddress={status()?.defaultAddress ?? ''} rows={rows()} />
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

      <StatusLine message={message()} />

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
  const sourceNote = (source: string) => {
    switch (source) {
      case 'listen':
        return 'from -listen'
      // The distinction the panel exists to make legible: the -listen address
      // is not gone, it is served by this wider one.
      case 'merged':
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
 * States which address a wildcard will absorb.
 *
 * Without it "Serving now" shows 127.0.0.1:4327 before Apply and only *:4327
 * after, which reads as an address that disappeared.
 */
function MergeNote(props: { defaultAddress: string, rows: AddressRow[] }) {
  const merged = () => {
    if (props.defaultAddress === '')
      return ''
    const port = props.defaultAddress.split(':').pop()
    const absorbing = props.rows.find(r => r.host === ANY_HOST && r.port === port)
    return absorbing ? `${props.defaultAddress} merges into ${toAddress(absorbing)}.` : ''
  }
  return <Show when={merged()}>{note => <div>{note()}</div>}</Show>
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
