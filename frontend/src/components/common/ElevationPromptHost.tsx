import type { Component } from 'solid-js'
import { createSignal, onCleanup, Show } from 'solid-js'
import { Dialog } from '~/components/common/Dialog'
import { ElevateForm } from '~/components/common/ElevateForm'
import { setElevationPrompter } from '~/lib/elevationPrompt'

/**
 * The one step-up prompt the app can open, and the only thing that registers
 * it.
 *
 * The transport opens it (see the elevation interceptor in `~/api/transport`),
 * so no call site opts in and no list of gated procedures exists in the
 * frontend to drift from the hub's. This component supplies the dialog the
 * transport asks for and nothing else: it holds no queued action, because the
 * transport retries the refused request itself.
 *
 * It mounts at the APP ROOT, beside the other app-wide dialogs, and it wraps
 * nothing. Both facts follow from where the trigger moved. The interceptor
 * refuses on an RPC from ANY surface, so a host scoped to one panel answers
 * only the refusals raised inside that panel; every other surface renders the
 * hub's raw refusal text with no prompt and no way forward. Today the Account
 * panel happens to issue every elevation-gated call, so a host inside it broke
 * nothing -- and that is the hazard, not a defense. The coupling is invisible
 * at both ends: the next gated call from any other surface fails this way, and
 * nothing marks the dependency. Scoping the prompt to one panel while the
 * trigger sits in the transport reinstates the per-surface opt-in the
 * interceptor exists to remove, at a coarser grain.
 *
 * It registers through a module-level setter rather than a Solid context, so
 * no descendant has to sit under it.
 *
 * ONE registration. A second mount replaces the first, so mounting this twice
 * is a bug rather than a stacked dialog -- which is what the gate this
 * replaced had to build a provider to prevent.
 */
export const ElevationPromptHost: Component = () => {
  const [resolve, setResolve] = createSignal<((proven: boolean) => void) | null>(null)

  const settle = (proven: boolean) => {
    const done = resolve()
    setResolve(null)
    done?.(proven)
  }

  setElevationPrompter(() => new Promise<boolean>((done) => {
    setResolve(() => done)
  }))
  // Unregister on unmount, so a transport refusal after this host is gone
  // rethrows the hub's message instead of awaiting a promise nothing can
  // settle.
  onCleanup(() => setElevationPrompter(null))

  return (
    <Show when={resolve() !== null}>
      {/*
        Dismissing REPORTS the cancellation. Silence is the wrong answer: a
        dropped action would leave the page looking exactly as it did before
        the click, with the new password still typed in and nothing saying it
        was not saved. Resolving false makes the transport rethrow the hub's
        own refusal, which every surface already renders.
      */}
      <Dialog title="Verify your identity" onClose={() => settle(false)}>
        <div class="vstack gap-4">
          <p>This change needs a recent sign-in.</p>
          <ElevateForm
            // The OAuth arm leaves this document entirely, so it cannot
            // resume the refused request. The hub sends the browser back
            // here once the provider re-authenticated the user, and "/" is
            // the app itself: Preferences is a dialog rather than a route,
            // so there is no deeper address to return to. The user re-opens
            // it and repeats the action, which now succeeds on the first
            // attempt because the session is elevated.
            oauthRedirect="/"
            onElevated={() => settle(true)}
          />
        </div>
      </Dialog>
    </Show>
  )
}
