import type { Component } from 'solid-js'
import type { AgentProvider, AvailableOptionGroup } from '~/generated/proto/leapmux/v1/agent_pb'
import type { TurnRetryOffer } from '~/stores/chatTurnRetryOffer'
import Info from 'lucide-solid/icons/info'
import { createEffect, createMemo, createSignal, on, Show } from 'solid-js'
import { OPTION_ID_MODEL, optionGroup } from '~/components/chat/settingsGroups'
import { ConfirmDialog } from '~/components/common/ConfirmDialog'
import { Icon } from '~/components/common/Icon'
import { getCachedSettingsLabel } from '~/lib/settingsLabelCache'
import { UNTRUSTED_LINK_ATTRIBUTE } from '~/lib/untrustedLinkClicks'
import { srOnly } from '~/styles/shared.css'
import * as styles from './TurnRetryOfferBanner.css'

const BANNER_TEXT = 'If you would rather not wait, retry on a faster model. A faster model can be less capable on complex tasks.'

export interface TurnRetryOfferBannerProps {
  /** The live offer of the worker, or undefined when it made none. */
  offer?: TurnRetryOffer | undefined
  /** The provider of the agent, for the display label of the model. */
  provider?: AgentProvider | undefined
  /** The live option groups of the agent, for a label that the cache did not ingest yet. */
  optionGroups?: AvailableOptionGroup[] | undefined
  /**
   * Stop the offered attempt and send its input again on the offered model.
   * It resolves true when the worker accepts the retry and false when the
   * worker refuses it. The handler reports a refusal itself, and it must never
   * reject.
   */
  onRetry: (turnId: string, model: string) => Promise<boolean>
}

/**
 * Offer to stop the current attempt and send its input again on a faster model.
 *
 * Keep one status node mounted before the offer arrives.
 * Some screen readers announce nothing when a live region appears with its content.
 * A second copy would announce the sentence twice and make text lookups ambiguous.
 *
 * Without an offer, the node uses `srOnly` and stays outside the composer's flex flow.
 * An offer makes the same node visible, without another copy of the sentence.
 * `AgentInputQueuePauseBanner` uses the same live-region pattern.
 *
 * "Keep waiting" dismisses this turn's offer from either the banner or its dialog.
 * A later turn shows its own offer.
 * A replacement or withdrawal closes the dialog and enables the next offer's button.
 *
 * A confirmed retry keeps its button disabled until the worker withdraws the offer.
 * A refusal enables the button again because the offer can still apply.
 */
export const TurnRetryOfferBanner: Component<TurnRetryOfferBannerProps> = (props) => {
  const [dismissedTurnId, setDismissedTurnId] = createSignal<string>()
  // The turn of the open confirm dialog, or undefined while the dialog is closed.
  const [confirmingTurnId, setConfirmingTurnId] = createSignal<string>()
  // The turn of the retry that the user confirmed and the worker did not refuse.
  const [retriedTurnId, setRetriedTurnId] = createSignal<string>()

  // The memo notifies only when the turn ID changes. A new offer object for the
  // same turn, such as an announcement with another model, keeps both states.
  const offerTurnId = createMemo(() => props.offer?.turnId)
  createEffect(on(offerTurnId, () => {
    setConfirmingTurnId(undefined)
    setRetriedTurnId(undefined)
  }, { defer: true }))

  const visible = () => props.offer !== undefined && props.offer.turnId !== dismissedTurnId()
  // Both reads compare the turn of the live offer, so the dialog closes and the
  // button enables in the same update that replaces the offer. The effect above
  // only clears the stale turn IDs afterwards.
  const confirmingOffer = () => {
    const offer = props.offer
    return offer !== undefined && offer.turnId === confirmingTurnId() ? offer : undefined
  }
  const retrying = () => props.offer !== undefined && props.offer.turnId === retriedTurnId()
  const label = () => {
    const offer = props.offer
    if (!offer)
      return ''
    return getCachedSettingsLabel(props.provider, OPTION_ID_MODEL, offer.model)
      ?? optionGroup(props.optionGroups, OPTION_ID_MODEL)?.options.find(option => option.id === offer.model)?.name
      ?? offer.model
  }

  const keepWaiting = (turnId: string) => {
    setConfirmingTurnId(undefined)
    setDismissedTurnId(turnId)
  }

  // Counts the confirmed retries, so a reply can tell whether a later retry
  // started after its own.
  let latestAttempt = 0
  const retry = async ({ turnId, model }: TurnRetryOffer) => {
    const attempt = ++latestAttempt
    setConfirmingTurnId(undefined)
    setRetriedTurnId(turnId)
    let accepted = false
    try {
      accepted = await props.onRetry(turnId, model)
    }
    finally {
      // Only a refusal of the latest retry enables the button again. A late
      // refusal of an earlier retry must not enable it while a later retry is
      // in flight, for this turn or for another one.
      if (!accepted && attempt === latestAttempt && retriedTurnId() === turnId)
        setRetriedTurnId(undefined)
    }
  }

  return (
    <>
      <div
        class={visible() ? styles.banner : srOnly}
        role="status"
        aria-live="polite"
        data-testid={visible() ? 'turn-retry-offer-banner' : undefined}
      >
        <Show when={visible()}>
          <div class={styles.notice}>
            <Icon icon={Info} size="xs" />
            <span class={styles.text}>{BANNER_TEXT}</span>
          </div>
          <div class={styles.actions}>
            <button
              class={`outline ${styles.action}`}
              type="button"
              disabled={retrying()}
              onClick={() => {
                const offer = props.offer
                if (offer)
                  setConfirmingTurnId(offer.turnId)
              }}
              data-testid="turn-retry-offer-retry"
            >
              {`Retry with ${label()}`}
            </button>
            <button
              class={`outline ${styles.action}`}
              type="button"
              onClick={() => {
                const offer = props.offer
                if (offer)
                  keepWaiting(offer.turnId)
              }}
              data-testid="turn-retry-offer-keep-waiting"
            >
              Keep waiting
            </button>
            <Show when={props.offer?.learnMoreUrl}>
              {url => (
                <a
                  class={styles.action}
                  href={url()}
                  target="_blank"
                  rel="noopener noreferrer nofollow"
                  {...{ [UNTRUSTED_LINK_ATTRIBUTE]: '' }}
                >
                  Learn more
                </a>
              )}
            </Show>
          </div>
        </Show>
      </div>
      <Show when={confirmingOffer()}>
        {offer => (
          // ConfirmDialog sends its cancel button, Escape and a click outside
          // through `onCancel`. Each one declines the retry for this turn.
          <ConfirmDialog
            title="Stop this attempt and retry?"
            confirmLabel="Stop and retry"
            cancelLabel="Keep waiting"
            data-testid="turn-retry-confirm-dialog"
            // Read the offer before the close unmounts this Show: its accessor
            // is stale once the dialog is gone.
            onConfirm={() => void retry(offer())}
            onCancel={() => keepWaiting(offer().turnId)}
          >
            {`This stops the current attempt and removes it from the native conversation history. LeapMux keeps the stopped attempt in this transcript. File changes and other actions that the agent already took stay. LeapMux sends your message again on ${label()}, a faster model that can be less capable on complex tasks.`}
          </ConfirmDialog>
        )}
      </Show>
    </>
  )
}
