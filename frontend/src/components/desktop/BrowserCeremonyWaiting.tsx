import type { Component } from 'solid-js'
import type { BrowserAuthFlow } from '~/lib/browserAuth'
import { Show } from 'solid-js'
import { openExternalUrl } from '~/api/platformBridge'
import { copyTextToClipboard } from '~/lib/clipboard'
import * as ceremonyPage from './ceremonyPage.css'

export const BrowserCeremonyWaiting: Component<{
  flow: BrowserAuthFlow | null
  descriptionClass?: string
  onCancel?: () => void | Promise<void>
}> = props => (
  <>
    <p class={props.descriptionClass}>Waiting for your browser...</p>
    <div class={ceremonyPage.waitingActions}>
      <button
        type="button"
        disabled={props.flow === null}
        onClick={() => {
          if (props.flow)
            void openExternalUrl(props.flow.url)
        }}
      >
        Open again
      </button>
      <button
        type="button"
        disabled={props.flow === null}
        onClick={() => {
          if (props.flow)
            void copyTextToClipboard(props.flow.url)
        }}
      >
        Copy link
      </button>
      <Show when={props.onCancel}>
        <button
          type="button"
          disabled={props.flow === null}
          onClick={() => void props.onCancel?.()}
        >
          Cancel
        </button>
      </Show>
    </div>
  </>
)
