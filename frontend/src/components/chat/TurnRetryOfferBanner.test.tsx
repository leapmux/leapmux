import type { TurnRetryOffer } from '~/stores/chatTurnRetryOffer'
import { create } from '@bufbuild/protobuf'
import { fireEvent, render, screen, within } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider, AvailableOptionGroupSchema } from '~/generated/proto/leapmux/v1/agent_pb'
import { clearSettingsLabelCache, updateSettingsLabelCache } from '~/lib/settingsLabelCache'
import { deferred, flush } from '~/test-support/async'
import { TurnRetryOfferBanner } from './TurnRetryOfferBanner'

const OFFER: TurnRetryOffer = {
  turnId: 'turn-1',
  model: 'gpt-6-luna',
  learnMoreUrl: 'https://help.openai.com/en/articles/20001326',
}

const MODEL_GROUPS = [
  create(AvailableOptionGroupSchema, {
    id: 'model',
    label: 'Model',
    options: [{ id: 'gpt-6-luna', name: 'GPT-6 Luna' }],
  }),
]

const BANNER_TEXT = 'Giving this request a little extra thought. If you would rather not wait, retry with a faster model. It may be less capable on complex requests.'

/** A retry handler that accepts every retry, as a worker that accepts the offer does. */
const accept = () => Promise.resolve(true)

/** Open the confirm dialog from the retry button of the banner. */
async function openConfirmDialog(): Promise<HTMLElement> {
  await fireEvent.click(screen.getByTestId('turn-retry-offer-retry'))
  return screen.getByTestId('turn-retry-confirm-dialog')
}

/** Confirm the retry in the open confirm dialog. */
async function confirmRetry(): Promise<void> {
  const dialog = await openConfirmDialog()
  await fireEvent.click(within(dialog).getByRole('button', { name: 'Stop and retry' }))
}

// The settings-label cache is shared across tests. Clear it so one case cannot
// change the labels of another case.
afterEach(() => {
  clearSettingsLabelCache()
})

describe('TurnRetryOfferBanner', () => {
  it('renders the offer sentence and the model label', () => {
    render(() => (
      <TurnRetryOfferBanner offer={OFFER} provider={AgentProvider.CODEX} optionGroups={MODEL_GROUPS} onRetry={accept} />
    ))
    expect(screen.getByTestId('turn-retry-offer-banner')).toHaveTextContent(BANNER_TEXT)
    expect(screen.getByTestId('turn-retry-offer-retry')).toHaveTextContent('Retry with GPT-6 Luna')
  })

  // The icon marks the line as a notice. A screen reader reads the sentence
  // alone, so the icon stays out of the live region's announcement.
  it('shows an info icon that a screen reader skips', () => {
    render(() => (
      <TurnRetryOfferBanner offer={OFFER} provider={AgentProvider.CODEX} optionGroups={MODEL_GROUPS} onRetry={accept} />
    ))
    const icon = screen.getByTestId('turn-retry-offer-banner').querySelector('svg')
    expect(icon).not.toBeNull()
    expect(icon).toHaveAttribute('aria-hidden', 'true')
  })

  // The model can be one the label cache has no display name for. The raw id
  // keeps the button readable.
  it('falls back to the model id when no label exists', () => {
    render(() => (
      <TurnRetryOfferBanner offer={OFFER} provider={AgentProvider.CODEX} onRetry={accept} />
    ))
    expect(screen.getByTestId('turn-retry-offer-retry')).toHaveTextContent('Retry with gpt-6-luna')
  })

  // The catalog can drop a model that the worker still offers. The cached
  // display label keeps the button readable in that case too.
  it('shows the cached display label of the model', () => {
    updateSettingsLabelCache(AgentProvider.CODEX, [
      create(AvailableOptionGroupSchema, {
        id: 'model',
        label: 'Model',
        options: [{ id: 'gpt-6-luna', name: 'GPT-6 Luna' }],
      }),
    ])
    render(() => (
      <TurnRetryOfferBanner offer={OFFER} provider={AgentProvider.CODEX} optionGroups={[]} onRetry={accept} />
    ))
    expect(screen.getByTestId('turn-retry-offer-retry')).toHaveTextContent('Retry with GPT-6 Luna')
  })

  // The live region is always mounted. Its class swaps, so a screen reader
  // hears the sentence once when the offer arrives.
  it('renders no banner content while the worker made no offer', () => {
    render(() => (
      <TurnRetryOfferBanner provider={AgentProvider.CODEX} onRetry={accept} />
    ))
    expect(screen.queryByTestId('turn-retry-offer-banner')).not.toBeInTheDocument()
    expect(screen.getByRole('status')).toBeInTheDocument()
    expect(screen.getByRole('status').querySelector('svg')).toBeNull()
  })

  it('hides the banner when the worker withdraws the offer', () => {
    const [offer, setOffer] = createSignal<TurnRetryOffer | undefined>(OFFER)
    render(() => (
      <TurnRetryOfferBanner offer={offer()} provider={AgentProvider.CODEX} optionGroups={MODEL_GROUPS} onRetry={accept} />
    ))
    expect(screen.getByTestId('turn-retry-offer-banner')).toBeInTheDocument()

    setOffer(undefined)
    expect(screen.queryByTestId('turn-retry-offer-banner')).not.toBeInTheDocument()
    // The live region stays mounted for the next offer.
    expect(screen.getByRole('status')).toBeInTheDocument()
  })

  it('keeps the banner hidden for a dismissed turn and shows it again for the next', async () => {
    const [offer, setOffer] = createSignal(OFFER)
    render(() => (
      <TurnRetryOfferBanner offer={offer()} provider={AgentProvider.CODEX} optionGroups={MODEL_GROUPS} onRetry={accept} />
    ))
    await fireEvent.click(screen.getByTestId('turn-retry-offer-keep-waiting'))
    expect(screen.queryByTestId('turn-retry-offer-banner')).not.toBeInTheDocument()

    // The worker can announce the offer again for the same turn. The user
    // already chose to keep waiting, so the banner stays hidden.
    setOffer({ ...OFFER, turnId: 'turn-1' })
    expect(screen.queryByTestId('turn-retry-offer-banner')).not.toBeInTheDocument()

    setOffer({ ...OFFER, turnId: 'turn-2' })
    expect(screen.getByTestId('turn-retry-offer-banner')).toBeInTheDocument()
  })

  it('links the provider help page behind the untrusted-link guard', () => {
    render(() => (
      <TurnRetryOfferBanner offer={OFFER} provider={AgentProvider.CODEX} onRetry={accept} />
    ))
    const link = screen.getByRole('link', { name: 'Learn more' })
    expect(link).toHaveAttribute('href', OFFER.learnMoreUrl)
    expect(link).toHaveAttribute('target', '_blank')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer nofollow')
    expect(link.hasAttribute('data-untrusted-link')).toBe(true)
  })

  it('shows no learn-more link when the offer gives no help page', () => {
    const offer = { turnId: 'turn-1', model: 'gpt-6-luna' }
    render(() => (
      <TurnRetryOfferBanner offer={offer} provider={AgentProvider.CODEX} optionGroups={MODEL_GROUPS} onRetry={accept} />
    ))
    expect(screen.queryByRole('link')).not.toBeInTheDocument()
  })

  it('opens the confirm dialog from the retry button', async () => {
    render(() => (
      <TurnRetryOfferBanner offer={OFFER} provider={AgentProvider.CODEX} optionGroups={MODEL_GROUPS} onRetry={accept} />
    ))
    const dialog = await openConfirmDialog()

    expect(within(dialog).getByText('Stop this attempt and retry?')).toBeInTheDocument()
    expect(within(dialog).getByText(
      'This stops the current attempt and removes it from the native conversation history. LeapMux keeps the stopped attempt in this transcript. File changes and other actions that the agent already took stay. LeapMux sends your message again on GPT-6 Luna, which can be less capable on complex tasks.',
    )).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Keep waiting' })).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Stop and retry' })).toBeInTheDocument()
  })

  it('sends the offered turn and model on confirm', async () => {
    const onRetry = vi.fn(accept)
    render(() => (
      <TurnRetryOfferBanner offer={OFFER} provider={AgentProvider.CODEX} optionGroups={MODEL_GROUPS} onRetry={onRetry} />
    ))
    await confirmRetry()

    expect(onRetry).toHaveBeenCalledOnce()
    expect(onRetry).toHaveBeenCalledWith('turn-1', 'gpt-6-luna')
    expect(screen.queryByTestId('turn-retry-confirm-dialog')).not.toBeInTheDocument()
  })

  // The worker can announce the offer of the same turn again with another
  // model while the dialog is open. The retry must use the live model, never
  // the model that the dialog opened with.
  it('sends the model that is live when the user confirms', async () => {
    const onRetry = vi.fn(accept)
    const [offer, setOffer] = createSignal(OFFER)
    render(() => (
      <TurnRetryOfferBanner offer={offer()} provider={AgentProvider.CODEX} optionGroups={MODEL_GROUPS} onRetry={onRetry} />
    ))
    const dialog = await openConfirmDialog()
    setOffer({ ...OFFER, model: 'gpt-7-quicker' })
    expect(within(dialog).getByText(/LeapMux sends your message again on gpt-7-quicker,/)).toBeInTheDocument()
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Stop and retry' }))

    expect(onRetry).toHaveBeenCalledWith('turn-1', 'gpt-7-quicker')
  })

  // The dialog asks about the attempt that the user saw. An offer for another
  // turn is a new decision, so the dialog of the earlier turn must close.
  it('closes the confirm dialog when the worker replaces the offer with one for another turn', async () => {
    const onRetry = vi.fn(accept)
    const [offer, setOffer] = createSignal(OFFER)
    render(() => (
      <TurnRetryOfferBanner offer={offer()} provider={AgentProvider.CODEX} optionGroups={MODEL_GROUPS} onRetry={onRetry} />
    ))
    await openConfirmDialog()

    setOffer({ ...OFFER, turnId: 'turn-2' })
    expect(screen.queryByTestId('turn-retry-confirm-dialog')).not.toBeInTheDocument()
    expect(screen.getByTestId('turn-retry-offer-banner')).toBeInTheDocument()
    expect(onRetry).not.toHaveBeenCalled()
  })

  // "Keep waiting" in the dialog means the same as "Keep waiting" in the
  // banner: the user declines the retry for this turn.
  it('hides the banner for the turn when the user keeps waiting in the dialog', async () => {
    const onRetry = vi.fn(accept)
    const [offer, setOffer] = createSignal(OFFER)
    render(() => (
      <TurnRetryOfferBanner offer={offer()} provider={AgentProvider.CODEX} optionGroups={MODEL_GROUPS} onRetry={onRetry} />
    ))
    const dialog = await openConfirmDialog()
    await fireEvent.click(within(dialog).getByRole('button', { name: 'Keep waiting' }))

    expect(onRetry).not.toHaveBeenCalled()
    expect(screen.queryByTestId('turn-retry-confirm-dialog')).not.toBeInTheDocument()
    expect(screen.queryByTestId('turn-retry-offer-banner')).not.toBeInTheDocument()

    // A later turn is a new decision, so its offer shows the banner again.
    setOffer({ ...OFFER, turnId: 'turn-2' })
    expect(screen.getByTestId('turn-retry-offer-banner')).toBeInTheDocument()
  })

  it('closes the confirm dialog without a retry when the worker withdraws the offer', async () => {
    const onRetry = vi.fn(accept)
    const [offer, setOffer] = createSignal<TurnRetryOffer | undefined>(OFFER)
    render(() => (
      <TurnRetryOfferBanner offer={offer()} provider={AgentProvider.CODEX} optionGroups={MODEL_GROUPS} onRetry={onRetry} />
    ))
    await openConfirmDialog()

    setOffer(undefined)
    expect(screen.queryByTestId('turn-retry-confirm-dialog')).not.toBeInTheDocument()
    expect(onRetry).not.toHaveBeenCalled()
  })

  // A withdrawal ends the decision that the dialog asked for. The next offer,
  // for the next turn or for the same turn again, must wait for a click.
  it.each(['turn-2', 'turn-1'])('opens no dialog without a click for an offer of %s after a withdrawal', async (turnId) => {
    const [offer, setOffer] = createSignal<TurnRetryOffer | undefined>(OFFER)
    render(() => (
      <TurnRetryOfferBanner offer={offer()} provider={AgentProvider.CODEX} optionGroups={MODEL_GROUPS} onRetry={accept} />
    ))
    await openConfirmDialog()

    setOffer(undefined)
    setOffer({ ...OFFER, turnId })
    expect(screen.getByTestId('turn-retry-offer-banner')).toBeInTheDocument()
    expect(screen.queryByTestId('turn-retry-confirm-dialog')).not.toBeInTheDocument()
  })

  // The worker withdraws the offer only after it accepts the retry. Until the
  // withdrawal arrives, a second retry of the same turn can only fail.
  it('disables the retry button after the confirm while the offer of the turn stays', async () => {
    const onRetry = vi.fn(accept)
    const [offer, setOffer] = createSignal(OFFER)
    render(() => (
      <TurnRetryOfferBanner offer={offer()} provider={AgentProvider.CODEX} optionGroups={MODEL_GROUPS} onRetry={onRetry} />
    ))
    await confirmRetry()
    await flush()
    expect(screen.getByTestId('turn-retry-offer-retry')).toBeDisabled()
    await fireEvent.click(screen.getByTestId('turn-retry-offer-retry'))
    expect(screen.queryByTestId('turn-retry-confirm-dialog')).not.toBeInTheDocument()

    // An announcement of the same turn again keeps the button disabled.
    setOffer({ ...OFFER, model: 'gpt-7-quicker' })
    expect(screen.getByTestId('turn-retry-offer-retry')).toBeDisabled()

    // An offer for another turn is a new decision.
    setOffer({ ...OFFER, turnId: 'turn-2' })
    expect(screen.getByTestId('turn-retry-offer-retry')).toBeEnabled()
    expect(onRetry).toHaveBeenCalledOnce()
  })

  it('keeps the retry button disabled while the retry request is in flight', async () => {
    const reply = deferred<boolean>()
    render(() => (
      <TurnRetryOfferBanner offer={OFFER} provider={AgentProvider.CODEX} optionGroups={MODEL_GROUPS} onRetry={() => reply.promise} />
    ))
    await confirmRetry()
    expect(screen.getByTestId('turn-retry-offer-retry')).toBeDisabled()

    reply.resolve(true)
    await flush()
    expect(screen.getByTestId('turn-retry-offer-retry')).toBeDisabled()
  })

  // The same turn can come back after a withdrawal when the retry fails before
  // the worker stops the turn. The user can then retry again.
  it('enables the retry button again when the worker withdraws the offer and offers the same turn again', async () => {
    const [offer, setOffer] = createSignal<TurnRetryOffer | undefined>(OFFER)
    render(() => (
      <TurnRetryOfferBanner offer={offer()} provider={AgentProvider.CODEX} optionGroups={MODEL_GROUPS} onRetry={accept} />
    ))
    await confirmRetry()
    await flush()
    expect(screen.getByTestId('turn-retry-offer-retry')).toBeDisabled()

    setOffer(undefined)
    setOffer(OFFER)
    expect(screen.getByTestId('turn-retry-offer-retry')).toBeEnabled()
  })

  // A refusal reports itself through the handler. The offer can still stand,
  // for example after a lost connection, so the user can try again.
  it('enables the retry button again when the worker refuses the retry', async () => {
    const reply = deferred<boolean>()
    const onRetry = vi.fn(() => reply.promise)
    render(() => (
      <TurnRetryOfferBanner offer={OFFER} provider={AgentProvider.CODEX} optionGroups={MODEL_GROUPS} onRetry={onRetry} />
    ))
    await confirmRetry()
    expect(screen.getByTestId('turn-retry-offer-retry')).toBeDisabled()

    reply.resolve(false)
    await flush()
    expect(screen.getByTestId('turn-retry-offer-retry')).toBeEnabled()
    await confirmRetry()
    expect(onRetry).toHaveBeenCalledTimes(2)
  })

  // A refusal of an earlier turn arrives after the offer moved on. It must not
  // enable the button of a turn that the user retried in the meantime.
  it('ignores a late refusal of an earlier turn', async () => {
    const first = deferred<boolean>()
    const second = deferred<boolean>()
    const replies = [first.promise, second.promise]
    const [offer, setOffer] = createSignal(OFFER)
    render(() => (
      <TurnRetryOfferBanner offer={offer()} provider={AgentProvider.CODEX} optionGroups={MODEL_GROUPS} onRetry={() => replies.shift()!} />
    ))
    await confirmRetry()
    setOffer({ ...OFFER, turnId: 'turn-2' })
    await confirmRetry()
    expect(screen.getByTestId('turn-retry-offer-retry')).toBeDisabled()

    first.resolve(false)
    await flush()
    expect(screen.getByTestId('turn-retry-offer-retry')).toBeDisabled()
    second.resolve(true)
  })

  // The worker can offer the same turn again after a withdrawal. A late
  // refusal of the first attempt must not enable the button while the second
  // attempt of that turn is in flight.
  it('ignores a late refusal of an earlier attempt of the same turn', async () => {
    const first = deferred<boolean>()
    const second = deferred<boolean>()
    const replies = [first.promise, second.promise]
    const [offer, setOffer] = createSignal<TurnRetryOffer | undefined>(OFFER)
    render(() => (
      <TurnRetryOfferBanner offer={offer()} provider={AgentProvider.CODEX} optionGroups={MODEL_GROUPS} onRetry={() => replies.shift()!} />
    ))
    await confirmRetry()
    setOffer(undefined)
    setOffer(OFFER)
    await confirmRetry()
    expect(screen.getByTestId('turn-retry-offer-retry')).toBeDisabled()

    first.resolve(false)
    await flush()
    expect(screen.getByTestId('turn-retry-offer-retry')).toBeDisabled()

    // The refusal of the latest attempt enables the button again.
    second.resolve(false)
    await flush()
    expect(screen.getByTestId('turn-retry-offer-retry')).toBeEnabled()
  })
})
