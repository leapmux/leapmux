import type { Accessor } from 'solid-js'
import type { StatusMessage } from '~/components/common/StatusLine'
import { createSignal } from 'solid-js'
import { formatErrorMessage } from '~/lib/errors'

export interface AccountActionRun<T> {
  /**
   * The message when the work throws and the error carries no text of its own.
   *
   * The hub answers a wrong password, a rate-limit refusal and a taken username
   * with sentences a user can act on, so `formatErrorMessage` prints those
   * verbatim and this covers only a failure with nothing to say.
   */
  fallback: string
  /**
   * The work, and the success message it wants -- `null` to report nothing.
   *
   * Everything the outcome depends on belongs INSIDE this callback, the
   * account re-read included. `busy` clears when this resolves, so a request
   * left unawaited here re-enables the control while the cached account still
   * holds the value the action just changed.
   */
  work: () => Promise<string | null>
  /**
   * What `busy()` answers while the work runs. It defaults to `true`, which is
   * the whole answer for a surface with one control; a surface with a control
   * per row passes that row's id, so only that row's control is disabled.
   */
  token?: T
}

export interface AccountAction<T> {
  /** The token of the work in flight, or null. */
  busy: Accessor<T | null>
  /** Whether any work is in flight. */
  running: Accessor<boolean>
  /** The outcome of the last action, or null. */
  message: Accessor<StatusMessage | null>
  /** Report a refusal this surface decided itself, before any request. */
  reject: (text: string) => void
  /** Clear the outcome. */
  clear: () => void
  /** Run one action, and report its outcome. */
  run: (options: AccountActionRun<T>) => Promise<void>
}

/**
 * The state one account editor keeps: what is in flight, and what happened.
 *
 * Four of the Account rows carried a byte-identical copy of it -- a `saving`
 * signal, a `StatusMessage` signal, and a try/catch/finally that cleared the
 * first and wrote the second. Two of them already drifted: they left the
 * account re-read unawaited, so the `finally` re-enabled the control while the
 * cached user still listed the provider that the hub just detached, and a
 * second click asked the hub to detach it again. The hub answered NotFound and
 * the panel reported a failure for an operation that succeeded.
 *
 * The ordering lives here now, in one place: `busy` clears only after `work`
 * resolves, and `work` is where a caller puts its own re-read.
 *
 * `AccountPasskeys` and `AccountConnectedApps` keep their own state. Each
 * holds a list, a modal and a second message channel, so a shared pair of
 * signals would carry a fraction of what they need.
 */
export function createAccountAction<T = true>(): AccountAction<T> {
  const [busy, setBusy] = createSignal<T | null>(null)
  const [message, setMessage] = createSignal<StatusMessage | null>(null)

  const reject = (text: string) => setMessage({ type: 'error', text })
  const clear = () => setMessage(null)

  const run = async (options: AccountActionRun<T>) => {
    setBusy(() => (options.token ?? true) as T)
    setMessage(null)
    try {
      const success = await options.work()
      if (success !== null)
        setMessage({ type: 'success', text: success })
    }
    catch (e) {
      setMessage({ type: 'error', text: formatErrorMessage(e, options.fallback) })
    }
    finally {
      setBusy(null)
    }
  }

  return {
    busy,
    running: () => busy() !== null,
    message,
    reject,
    clear,
    run,
  }
}
