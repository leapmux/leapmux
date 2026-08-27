import type { Component } from 'solid-js'
import { useLocation } from '@solidjs/router'
import { createSignal, onCleanup, Show } from 'solid-js'
import { Dialog } from '~/components/common/Dialog'
import { ElevateForm } from '~/components/common/ElevateForm'
import { setElevationPrompter } from '~/lib/elevationPrompt'

/**
 * The one step-up prompt the app can open, and the only thing that registers
 * it.
 *
 * The transport opens it (see the elevation interceptor in `~/api/transport`),
 * so no call site opts in, and the frontend keeps no list of the procedures
 * that require elevation to drift from the hub's. This component supplies the
 * dialog the transport asks for and nothing else: it holds no queued action,
 * because the transport retries the refused request itself.
 *
 * It mounts at the APP ROOT, beside the other app-wide dialogs, and it wraps
 * nothing. Both facts follow from where the trigger moved. The interceptor
 * refuses on an RPC from ANY surface, so a host scoped to one panel answers
 * only the refusals raised inside that panel; every other surface renders the
 * hub's raw refusal text with no prompt and no way to continue. Today the
 * Account panel happens to issue every call that requires elevation, so a host
 * inside it broke nothing -- and that is the hazard, not a defense. The
 * coupling is invisible at both ends: a call that requires elevation from any
 * other surface fails this way, and nothing marks the dependency. Scoping the
 * prompt to one panel while the trigger sits in the transport reinstates the
 * per-surface opt-in the interceptor exists to remove, at a coarser grain.
 *
 * It registers through a module-level setter rather than a Solid context, so
 * no descendant has to sit under it.
 *
 * ONE registration. A second mount replaces the first, so mounting this twice
 * is a bug rather than a stacked dialog -- which is what the gate this
 * replaced needed a provider to prevent.
 *
 * THE HOST OWNS THE STACK, so no surface has to. A refusal raised from inside
 * an already-open dialog puts this prompt on top of it, and this component is
 * the layer that knows so. See `cover` below. The Account panel used to
 * pre-empt that stack per click -- it verified the mirrored deadline itself and
 * opened the prompt BEFORE its own dialog -- and the cost was that the next
 * dialog to raise a restricted call had to copy the same reasoning. It never
 * decided authorization: the interceptor runs on the request either way.
 */
export const ElevationPromptHost: Component = () => {
  const location = useLocation()
  const [resolve, setResolve] = createSignal<((proven: boolean) => void) | null>(null)

  /**
   * The address the browser is on, for the OAuth option to return to.
   *
   * The REAL one, not "/". Preferences carries its open state and its section
   * in the address (see `PREFERENCES_PARAM` in `~/components/shell/UserMenuState`),
   * so the hub sends the browser back to the panel the user was in rather than
   * to a bare app root.
   */
  const here = () => `${location.pathname}${location.search}${location.hash}`

  /**
   * The dialogs this prompt covers, and the element that held focus, recorded
   * when the prompt opens.
   */
  let covered: Element[] = []
  let focusedBefore: HTMLElement | null = null

  /**
   * Suppresses every dialog beneath this prompt, and records where focus was.
   *
   * `Dialog` uses the native modal `<dialog>`, so the browser already puts the
   * newest one at the top of the top layer and marks everything else inert.
   * This states the same thing on the elements themselves. Three reasons: it
   * makes the suppression the HOST's, which is the point -- a surface no
   * longer has to pre-empt the stack to keep a refusal from landing on top of
   * its own dialog; it holds for a future non-native dialog; and it is
   * observable, so a test can prove it.
   *
   * The query runs BEFORE `setResolve` renders this host's own dialog, so it
   * cannot include it. The statement order is the whole guarantee -- there is
   * no element to compare against yet.
   */
  const cover = () => {
    focusedBefore = document.activeElement instanceof HTMLElement ? document.activeElement : null
    covered = [...document.querySelectorAll('dialog[open]')]
    for (const dialog of covered)
      dialog.setAttribute('inert', '')
  }

  /**
   * Releases the covered dialogs and returns focus where the prompt found it.
   *
   * The release comes FIRST: focus does not move into an inert subtree, so
   * restoring before it would silently do nothing. The recorded element can be
   * detached by now (the surface that held it re-rendered), and `focus()` on a
   * detached element is a no-op, which is the right answer.
   */
  const uncover = () => {
    for (const dialog of covered)
      dialog.removeAttribute('inert')
    covered = []
    const target = focusedBefore
    focusedBefore = null
    target?.focus()
  }

  const settle = (proven: boolean) => {
    const done = resolve()
    setResolve(null)
    uncover()
    done?.(proven)
  }

  setElevationPrompter(() => new Promise<boolean>((done) => {
    cover()
    setResolve(() => done)
  }))
  onCleanup(() => {
    // Unregister, so a transport refusal after this host is gone rethrows the
    // hub's message instead of awaiting a promise nothing can settle.
    setElevationPrompter(null)
    // Then settle a prompt that is still open. Unregistering alone leaves that
    // promise pending for ever, and `promptForElevation` clears `inFlight` and
    // `prompting` only when it settles. The unregistration does not shield the
    // app either: the next host to mount registers again, so the "nobody can
    // prompt" branch stops applying and every later refusal receives the DEAD
    // promise. Each sensitive action then hangs with no error while its
    // controls stay disabled, for the life of the page.
    //
    // This host really does go away under a running app. The app-root
    // ErrorBoundary unmounts its whole subtree when it catches, and the desktop
    // launcher unmounts the connected subtree when the connection drops.
    //
    // `false` is the same answer a dismissal gives, so the transport rethrows
    // the hub's refusal and the surface renders it. Settling here also releases
    // the covered dialogs, which would otherwise stay inert with nothing left
    // to un-inert them.
    settle(false)
  })

  return (
    <Show when={resolve() !== null}>
      {/*
        Dismissing REPORTS the cancellation. Silence is the wrong answer: a
        dropped action would leave the page looking exactly as it did before
        the click, with the new password still typed in and nothing to say that
        the app did not save it. Resolving false makes the transport rethrow
        the hub's own refusal, which every surface already renders.
      */}
      <Dialog title="Verify your identity" onClose={() => settle(false)}>
        <div class="vstack gap-4">
          <p>This change needs a recent sign-in.</p>
          <ElevateForm
            // The OAuth option leaves this document entirely, so it cannot
            // resume the refused request. The hub sends the browser back to
            // the address it left, which now carries the open panel, so the
            // user lands where they were and repeats the action -- and it
            // succeeds on the first attempt, because the session is elevated.
            oauthRedirect={here()}
            onElevated={() => settle(true)}
          />
        </div>
      </Dialog>
    </Show>
  )
}
