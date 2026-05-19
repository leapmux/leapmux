import { createSignal } from 'solid-js'
import { apiLoadingTimeoutMs } from '~/api/transport'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { formatErrorMessage } from '~/lib/errors'

export interface UseDialogSubmitOptions {
  /** Loading-spinner timeout (ms). Defaults to `apiLoadingTimeoutMs()`. */
  timeoutMs?: number
  /**
   * Replacement for the default `'Operation failed'` shown when the
   * rejection isn't an `Error` (or has no `.message`). Per-dialog
   * callers pass their own voice (e.g. `'Delete failed'`).
   */
  fallback?: string
  /**
   * Full override of the error-mapping function. Use only when the
   * default `err.message ?? fallback` shape isn't enough; otherwise
   * prefer `fallback`.
   */
  formatError?: (err: unknown) => string
  /**
   * Replaces the default `setError(formatError(err))` behavior on
   * rejection. Use for callers that surface errors via toast (or any
   * other side channel) and need the raw `err` object instead of a
   * formatted string. When provided, the `error` signal is NOT set on
   * rejection — the callback owns the error UX end-to-end.
   */
  onError?: (err: unknown) => void
}

/**
 * Dialog submit-state primitive: bundles the debounced loading signal,
 * a string error sink, and a runner that wires both around an async
 * submit body. The runner clears the error before the body, sets it
 * (via `formatError`) on rejection, and always stops the loading signal
 * in `finally`. The body itself owns any post-success work — e.g.
 * `props.onClose()` runs after `await fn()` returns normally, never on
 * rejection.
 */
export function useDialogSubmit(options: UseDialogSubmitOptions = {}) {
  const submitting = createLoadingSignal(options.timeoutMs ?? apiLoadingTimeoutMs())
  const [error, setError] = createSignal<string | null>(null)
  const fallback = options.fallback ?? 'Operation failed'
  const formatError = options.formatError ?? ((err: unknown) => formatErrorMessage(err, fallback))

  // Reentrancy guard: form-submit paths already gate via
  // `formHandler`'s `disabled()` (which reads `submitting.loading()`),
  // but programmatic callers — E2E retry helpers, hotkey bindings,
  // parents that compose their own submit flow — bypass that gate. Two
  // overlapping `run(fn)` calls would otherwise fire `fn` in parallel
  // and double-execute side-effectful RPCs (CheckoutBranch,
  // DeleteBranch). The boolean trips before the first awaitable so a
  // synchronous-double-click can't slip both through.
  let inFlight = false
  const run = async (fn: () => Promise<void>): Promise<void> => {
    if (inFlight)
      return
    inFlight = true
    submitting.start()
    setError(null)
    try {
      await fn()
    }
    catch (err) {
      if (options.onError)
        options.onError(err)
      else
        setError(formatError(err))
    }
    finally {
      submitting.stop()
      inFlight = false
    }
  }

  /**
   * Form-submit handler factory. Returns an `onSubmit` that prevents the
   * default form submission, bails when `disabled()` reports true, and
   * runs `body` inside the standard {@link run} wrapper. `disabled` is
   * re-evaluated on every invocation, so the guard tracks the dialog's
   * live disabled state (not a snapshot taken at construction time).
   *
   * `run` / `formHandler` are listed in eslint.config.ts's
   * `customReactiveFunctions` allowlist so call sites can pass arrows
   * that close over reactive props without a per-site
   * `solid/reactivity` disable.
   */
  const formHandler = (disabled: () => boolean, body: () => Promise<void>) =>
    async (e: Event) => {
      e.preventDefault()
      if (disabled())
        return
      await run(body)
    }

  return { submitting, error, setError, run, formHandler }
}
