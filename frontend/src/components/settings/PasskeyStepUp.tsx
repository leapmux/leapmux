import type { Component } from 'solid-js'
import { Show } from 'solid-js'
import { successText } from '~/styles/shared.css'

/**
 * The step-up control shared by every passkey-management surface: offer a
 * passkey verification ("Verify with passkey") and render the verified
 * state once a proof exists. `onVerify` runs the reauth ceremony and stores
 * the proof; `proof` decides which half renders.
 */
export const PasskeyStepUp: Component<{
  proof: string
  busy: boolean
  onVerify: () => void
}> = (props) => {
  return (
    <Show
      when={!props.proof}
      fallback={<span class={successText}>Passkey verified.</span>}
    >
      <button type="button" onClick={() => props.onVerify()} disabled={props.busy}>
        Verify with passkey
      </button>
    </Show>
  )
}
