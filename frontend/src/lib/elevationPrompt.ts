import { Code, ConnectError } from '@connectrpc/connect'
import { createSignal } from 'solid-js'
import { createLogger } from '~/lib/logger'

const log = createLogger('elevationPrompt')

/**
 * The one step-up prompt, reached from the transport.
 *
 * The hub refuses an un-elevated sensitive action with a marker
 * (see isElevationRequired), and the remedy is always the same: prove a factor
 * and try the request again. That used to be an OPT-IN at each call site --
 * every sensitive `userClient` call had to wrap itself in `gate.run(...)` -- so
 * a call site that forgot rendered the hub's raw refusal text with no prompt
 * and no way forward, and nothing said so.
 *
 * Now the transport does it, so the frontend holds no list of procedures that
 * require elevation to drift from the hub's, and no call site to forget.
 *
 * Two consequences worth stating, because both are improvements rather than
 * accidents:
 *
 *   - The retry is the ONE REFUSED REQUEST, not the caller's whole closure.
 *     Adding a passkey wrapped Begin, the browser prompt and Finish in one
 *     closure, so a refused Begin re-ran the whole ceremony and asked the
 *     authenticator twice.
 *   - Concurrent refusals share one prompt. Two controls refused at once used
 *     to open two dialogs, and proving a factor in one dropped the other's
 *     action.
 */

/**
 * The header the hub sets on a refusal whose remedy is "prove a factor and
 * retry". Mirrors service.ElevationRequiredHeader.
 *
 * Keyed on a header rather than on the message, because the message is
 * user-facing prose that somebody will reword; and rather than on the code alone,
 * because FailedPrecondition also carries refusals a prompt cannot fix
 * ("this account has no password", "set a replacement password first").
 */
const ELEVATION_REQUIRED_HEADER = 'leapmux-elevation-required'

/**
 * Whether this failure is one a step-up prompt would resolve.
 *
 * It lives HERE, with the prompt, rather than in `~/lib/elevation` with the
 * ceremony calls -- and the reason is mechanical, not taste. The transport
 * reads this predicate, `~/lib/elevation` reaches `~/api/clients` for the
 * ceremony RPCs, and `~/api/clients` builds itself from the transport. In
 * that cycle `~/api/clients` evaluated first and captured an UNDEFINED
 * transport, so every RPC in the app failed, including sign-in. This module
 * imports `~/lib/logger` alone, which itself imports nothing, so it can never
 * close a loop. `~/test-support/noImportCycles.test.ts` now fails the suite on
 * any cycle.
 */
export function isElevationRequired(err: unknown): boolean {
  return err instanceof ConnectError
    && err.code === Code.FailedPrecondition
    && err.metadata.get(ELEVATION_REQUIRED_HEADER) !== null
}

/** Opens the prompt and resolves with whether a factor was proven. */
export type ElevationPrompter = () => Promise<boolean>

let prompter: ElevationPrompter | null = null

/**
 * Registers the prompt. The provider that mounts the dialog is the one caller,
 * exactly as AuthContext is the one caller of setOnAuthError.
 */
export function setElevationPrompter(fn: ElevationPrompter | null): void {
  prompter = fn
}

const [prompting, setPrompting] = createSignal(false)

/**
 * True while a step-up prompt is open or the transport retries a refused
 * request.
 *
 * A surface disables its sensitive controls on this, for the reason the gate's
 * own `busy()` existed: one prompt serves the whole app, and a second action
 * started underneath it would queue behind the same dialog.
 */
export { prompting as elevationPrompting }

/**
 * One in-flight prompt, shared.
 *
 * Two requests refused at the same instant must not open two dialogs. The
 * second awaits the first and then retries on its answer, which is right:
 * the factor the user proves admits both.
 */
let inFlight: Promise<boolean> | null = null

/**
 * Runs the prompt, or reports that nothing can run it.
 *
 * `false` when no prompter is registered, so the transport rethrows the hub's
 * refusal rather than discarding it: a surface outside the provider must still
 * see why its call failed. That branch is what makes a page with no host --
 * the desktop launcher, an app-root ErrorBoundary that already caught -- behave
 * as every surface did before the prompt existed: act, and let the hub's own
 * refusal reach the user.
 *
 * One `false` covers a dismissal and an absent prompter, and nothing needs to
 * tell them apart. Every caller reaches this AFTER the hub refused a request
 * it already made, and the answer to both is the same: rethrow that refusal.
 */
export async function promptForElevation(): Promise<boolean> {
  if (!prompter)
    return false
  if (inFlight)
    return inFlight
  const open = prompter
  setPrompting(true)
  // `openPrompt` never throws and never rejects, so this `finally` always
  // runs. That is what stops `inFlight` and `prompting` from staying set.
  inFlight = openPrompt(open).finally(() => {
    inFlight = null
    setPrompting(false)
  })
  return inFlight
}

/**
 * Calls the prompter and converts every failure into a dismissal.
 *
 * `ElevationPrompter` returns `Promise<boolean>`, and a prompter can break that
 * contract in two ways: it can reject, and it can throw before it builds the
 * promise. Neither may reach the caller.
 *
 * A rejection that escapes replaces the hub's own refusal. The transport awaits
 * `promptForElevation` inside the `catch` that holds the FailedPrecondition, so
 * the surface then renders the prompter's internal error in place of the reason
 * the request failed.
 *
 * A synchronous throw is worse. `setPrompting(true)` already ran, and the
 * assignment to `inFlight` never happens, so nothing clears either signal and
 * every sensitive control stays disabled for the life of the page.
 *
 * `false` is the correct answer for both: it is what a dismissal gives, and the
 * transport rethrows the hub's refusal on it.
 */
async function openPrompt(open: ElevationPrompter): Promise<boolean> {
  try {
    return await open()
  }
  catch (err) {
    log.error('the step-up prompt failed; treating it as a dismissal', err)
    return false
  }
}
