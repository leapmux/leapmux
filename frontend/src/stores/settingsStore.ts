import type { SettingDescriptor, SettingValue } from '~/generated/proto/leapmux/v1/settings_pb'
import { createStore } from 'solid-js/store'
import { formatErrorMessage } from '~/lib/errors'
import { createKeyedQueue } from '~/lib/keyedQueue'
import { createKeyedSeq } from '~/lib/keyedSeq'

/** The setting key a failed write targeted, plus the reason it failed. */
export interface SettingWriteError {
  key: string
  message: string
}

export interface SettingsStoreState {
  descriptors: SettingDescriptor[]
  /** Live values by setting key (`valueJson`/`effectiveJson` parsed by rows). */
  values: Map<string, SettingValue>
  loading: boolean
  /** The most recent load failure, for the surface to render. */
  error: string | null
  /** The most recent failed write, for the row to render inline. */
  writeError: SettingWriteError | null
  /** Whether a load has completed since the catalogue was cleared. */
  loaded: boolean
}

/**
 * A settings store, shaped so it satisfies the row model's
 * `ProtoSettingsSource` directly — no adapter literal at the call site.
 */
export interface SettingsStore {
  state: SettingsStoreState
  /** Values by key, the accessor the proto registry reads. */
  values: () => Map<string, SettingValue>
  load: () => Promise<void>
  /** Discard the catalogue and invalidate pending load responses. */
  clear: () => void
  /** Server-first partial merge onto one key's public half. */
  update: (key: string, partialJson: string) => Promise<void>
  /** Server-first partial merge onto one key's SECRET half. */
  updateSecret: (key: string, partialJson: string) => Promise<void>
  /** Server-first removal of one key's stored value. */
  reset: (key: string) => Promise<void>
}

/** The RPC surface one scope supplies, plus its access guard. */
export interface SettingsStorePorts {
  list: () => Promise<{ descriptors: SettingDescriptor[], values: SettingValue[] }>
  update: (key: string, partialJson: string) => Promise<SettingValue | undefined>
  reset: (key: string) => Promise<SettingValue | undefined>
  updateSecret: (key: string, partialJson: string) => Promise<SettingValue | undefined>
  /**
   * While this reads false, `load` clears the state instead of asking, and
   * every mutation rejects with `guardMessage`.
   */
  enabled: () => boolean
  guardMessage: string
  loadErrorFallback: string
}

function emptySettingsState(): SettingsStoreState {
  return {
    descriptors: [],
    values: new Map(),
    loading: false,
    error: null,
    writeError: null,
    loaded: false,
  }
}

/**
 * The settings store over the HUB scope's RPC surface.
 *
 * One production caller builds it, `createAdminSettingsStore`. The ACCOUNT
 * scope does not come through here and cannot: `PreferencesContext`
 * publishes typed accessors, an optimistic write with rollback, and a
 * browser override tier over each key, none of which this shape carries.
 * The two scopes therefore share the ordering RULES rather than this
 * implementation — `~/lib/keyedSeq` and `~/lib/keyedQueue` are what both
 * of them hold.
 *
 * Loads stamp a monotonic sequence and only write back while still newest,
 * mirroring `useWorkspaceLoader`: two loads in flight must not let the
 * earlier ANSWER win over the later ASK.
 */
export function createSettingsStore(ports: SettingsStorePorts): SettingsStore {
  const [state, setState] = createStore(emptySettingsState())

  /**
   * The newest LIST request issued.
   *
   * Unkeyed: one store loads one whole scope, so a second load supersedes
   * the first outright. The same rule the per-key write guard below holds.
   */
  const loadSeq = createKeyedSeq()
  const writeSeq = createKeyedSeq()
  let accessGeneration = 0

  const guard = (): Error => new Error(ports.guardMessage)

  const allowed = (): boolean => ports.enabled()

  // A fresh Map, never a mutation: solid-js/store treats a Map as an opaque
  // value and short-circuits on reference identity, so mutating in place
  // would notify no subscriber.
  const mergeValue = (value: SettingValue | undefined) => {
    if (!value)
      return
    const next = new Map(state.values)
    next.set(value.key, value)
    setState('values', next)
  }

  const clear = () => {
    accessGeneration++
    loadSeq.next()
    setState(emptySettingsState())
  }

  const load = async () => {
    if (!allowed()) {
      clear()
      return
    }
    const mySeq = loadSeq.next()
    const newest = () => loadSeq.isNewest(undefined, mySeq)
    const writesAtIssue = writeSeq.snapshot()
    setState('loading', true)
    const read = async () => {
      try {
        if (!newest())
          return
        if (!allowed()) {
          clear()
          return
        }
        const resp = await ports.list()
        if (!newest())
          return
        const responseValues = new Map(resp.values.map(value => [value.key, value]))
        const values = new Map(state.values)
        const keys = new Set([
          ...values.keys(),
          ...responseValues.keys(),
        ])
        for (const key of keys) {
          if (!writeSeq.isNewest(key, writesAtIssue.get(key) ?? 0))
            continue
          const value = responseValues.get(key)
          if (value)
            values.set(key, value)
          else
            values.delete(key)
        }
        setState({
          error: null,
          descriptors: resp.descriptors,
          values,
        })
      }
      catch (err) {
        if (newest())
          setState('error', formatErrorMessage(err, ports.loadErrorFallback))
      }
      finally {
        if (newest())
          setState({ loading: false, loaded: true })
      }
    }
    await read()
  }

  /**
   * The newest mutation issued per key.
   *
   * Every reply merges the server's value into `state.values`, so two writes
   * to one key that complete out of order would leave the OLDER value on
   * screen — two fast clicks on a toggle, and it snaps back. The sequence is
   * per key, not global: writes to different keys are independent and must
   * not cancel each other. The same guard `load` above holds against two
   * loads, and the same one `PreferencesContext` holds over the account
   * tier's writes.
   */
  /**
   * The mutation REQUESTS in flight per key.
   *
   * The sequence above decides which ANSWER is applied. It cannot decide
   * which REQUEST the hub commits first, and a partial merge under a row
   * lock keeps whatever commits LAST, so two requests that arrive out of
   * order leave the hub holding the older document while the screen shows
   * the newer one. The reply guard cannot see that: both replies report
   * success.
   */
  const writeQueue = createKeyedQueue()

  /** Run one server-first mutation: no optimistic apply, merge on success. */
  const mutate = async (
    key: string,
    call: () => Promise<SettingValue | undefined>,
  ): Promise<void> => {
    if (!allowed())
      throw guard()
    // Taken at ISSUE time, before the request waits its turn, so it still
    // matches the order the user asked in.
    const mySeq = writeSeq.next(key)
    const generationAtIssue = accessGeneration
    const currentAccess = () => generationAtIssue === accessGeneration && allowed()
    const newestIntent = () => currentAccess() && writeSeq.isNewest(key, mySeq)
    const request = writeQueue.run(key, async () => {
      if (!currentAccess())
        throw guard()
      const value = await call()
      if (!currentAccess())
        return

      // The queue publishes each commit before the next write to this key.
      // A later failed write leaves this value in the Hub and in the store.
      mergeValue(value)
      if (newestIntent())
        setState('writeError', null)
    })
    try {
      await request
    }
    catch (err) {
      // The store keeps the server's truth and the row shows the failure.
      if (newestIntent()) {
        setState('writeError', {
          key,
          message: formatErrorMessage(err, 'Failed to save setting'),
        })
      }
      throw err
    }
  }

  return {
    state,
    values: () => state.values,
    load,
    clear,
    update: (key, partialJson) => mutate(key, () => ports.update(key, partialJson)),
    updateSecret: (key, partialJson) => mutate(key, () => ports.updateSecret(key, partialJson)),
    reset: key => mutate(key, () => ports.reset(key)),
  }
}
