import type { Component } from 'solid-js'
import type { KeyPinEntry } from '~/lib/keyPinStore'
import { batch, createEffect, createSignal, For, onCleanup, Show } from 'solid-js'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { ClippedText } from '~/components/common/ClippedText'
import { ConfirmButton } from '~/components/common/ConfirmButton'
import { useAuth } from '~/context/AuthContext'
import { CorruptKeyPinMapError, discardCorruptKeyPinMap, onStorageAccountChange } from '~/lib/browserStorage'
import { formatLocalDateTime } from '~/lib/dateFormat'
import { formatErrorMessage } from '~/lib/errors'
import { clearAllKeyPins, clearKeyPin, listKeyPins, onKeyPinsChanged } from '~/lib/keyPinStore'
import { errorText } from '~/styles/shared.css'
import * as styles from './KeyPinsControl.css'

/**
 * The trusted-worker-keys editor: every TOFU pin with its worker id, first
 * pinned time, a remove button per row, and a "Remove all" danger action.
 * Reads re-render from a local revision signal, which a committed pin change
 * in any tab and a move of the account namespace both bump — the view shows
 * durable state, not a snapshot it trusts forever.
 *
 * Layout: on desktop the id, date, and Remove share one row (id clips with a
 * tooltip when long). On phone the id takes the full card width and the date
 * + Remove sit underneath, so a long id never collapses into a one-character
 * column beside the button.
 *
 * Both remove actions use ConfirmButton (arm, then confirm) so a stray tap
 * cannot wipe pins. Per-key Remove keeps the small outline chrome.
 *
 * The pin store is account-scoped, and both a read and a removal can fail.
 * A failure is shown with what to do next. It also keeps the last list on
 * screen when the failure left this account's pins where they were: a
 * same-account failure is a failure of the storage, not of the data. An
 * account change drops that list instead — the pins belong to an account this
 * document no longer holds.
 */
export const KeyPinsControl: Component = () => {
  const auth = useAuth()
  const [revision, setRevision] = createSignal(0)
  const [known, setKnown] = createSignal<KeyPinEntry[] | null>(null)
  const [readError, setReadError] = createSignal<string | null>(null)
  const [corruptMap, setCorruptMap] = createSignal(false)
  const [removeError, setRemoveError] = createSignal<string | null>(null)
  const [busy, setBusy] = createSignal(false)

  let accountGeneration = 0
  let latestReadId = 0
  const componentLifetime = new AbortController()

  function refreshPins(): void {
    setRevision(value => value + 1)
  }

  async function readPins(): Promise<void> {
    const readId = ++latestReadId
    try {
      const pins = await listKeyPins()
      if (readId !== latestReadId)
        return
      batch(() => {
        setKnown(pins)
        setReadError(null)
        setCorruptMap(false)
      })
    }
    catch (err) {
      if (readId === latestReadId) {
        setReadError(formatErrorMessage(err, 'Could not read the trusted worker keys.'))
        setCorruptMap(err instanceof CorruptKeyPinMapError)
      }
    }
  }

  createEffect(() => {
    void revision()
    void readPins()
  })

  // Retire reads and removals before the account-change notification refreshes
  // the list. A switch away and back still starts a new lifetime.
  const stopAccount = onStorageAccountChange(() => {
    accountGeneration++
    latestReadId++
    batch(() => {
      setKnown(null)
      setReadError(null)
      setCorruptMap(false)
      setRemoveError(null)
      setBusy(false)
    })
  })
  const stopChanges = onKeyPinsChanged(refreshPins)
  onCleanup(() => {
    componentLifetime.abort()
    stopAccount()
    stopChanges()
    accountGeneration++
    latestReadId++
  })

  async function discardCorruptPins(): Promise<void> {
    const scope = auth.workerTrustScope()
    if (!scope.userId || !scope.storageAccount)
      throw new Error('Sign in before discarding trusted Worker keys.')
    await discardCorruptKeyPinMap({
      accountId: scope.storageAccount,
      signal: AbortSignal.any([scope.signal, componentLifetime.signal]),
    })
  }

  async function removePins(remove: () => Promise<void>, failureMessage: string): Promise<void> {
    if (busy())
      return
    const removalGeneration = accountGeneration
    setBusy(true)
    setRemoveError(null)
    try {
      // A committed removal publishes its own invalidation, which refreshes
      // the list when this account is still active.
      await remove()
    }
    catch (err) {
      if (removalGeneration === accountGeneration)
        setRemoveError(formatErrorMessage(err, failureMessage))
    }
    finally {
      if (removalGeneration === accountGeneration)
        setBusy(false)
    }
  }

  return (
    <div class="vstack gap-2" data-testid="key-pins">
      <Show when={readError()}>
        <div class={errorText} role="alert" data-testid="key-pins-read-error">
          {readError()}
          {' '}
          <button type="button" class="outline" onClick={refreshPins} data-testid="key-pins-retry">
            Retry
          </button>
          <Show when={corruptMap()}>
            <p>This discards every trusted Worker key in the corrupt map for this account. Each Worker will require trust to be established again.</p>
            <ConfirmButton
              data-variant="danger"
              disabled={busy()}
              data-testid="key-pins-discard-corrupt"
              onClick={() => void removePins(
                discardCorruptPins,
                'Could not discard the corrupt trusted Worker keys. The stored data was preserved.',
              )}
            >
              Discard corrupt pin map
            </ConfirmButton>
          </Show>
        </div>
      </Show>
      <Show when={removeError()}>
        <div class={errorText} role="alert" data-testid="key-pins-remove-error">
          {removeError()}
        </div>
      </Show>
      <Show when={known()?.length === 0 && readError() === null}>
        <div class={styles.empty}>No worker keys pinned on this device.</div>
      </Show>
      <Show when={(known()?.length ?? 0) > 0}>
        <For each={known()}>
          {pin => (
            <div class={styles.pinRow} data-worker={pin.workerId}>
              <ClippedText text={pin.workerId} class={styles.pinWorker} testId={`key-pin-id-${pin.workerId}`} />
              <div class={styles.pinMeta}>
                <span class={styles.pinDate}>{formatLocalDateTime(new Date(pin.firstSeen))}</span>
                {/*
                  No `<Tooltip>` here. This button shows a word, so its children
                  already give it a name, and `ConfirmButton` swaps that word
                  for "Confirm?" when it arms. A wrapper with `ariaLabel` pinned
                  the accessible name to "Remove" in BOTH states, so a screen
                  reader heard "Remove" for the confirming click as well and
                  never learnt that the first click had armed anything.
                */}
                <ConfirmButton
                  class="small outline"
                  disabled={busy()}
                  data-testid={`key-pin-remove-${pin.workerId}`}
                  onClick={() => void removePins(
                    () => clearKeyPin(pin.workerId),
                    'Could not remove the trusted worker key. Try again.',
                  )}
                >
                  Remove
                </ConfirmButton>
              </div>
            </div>
          )}
        </For>
        <div class={actionsFooter}>
          <ConfirmButton
            data-variant="danger"
            disabled={busy()}
            onClick={() => void removePins(clearAllKeyPins, 'Could not remove the trusted worker keys. Try again.')}
            data-testid="key-pins-remove-all"
          >
            Remove all
          </ConfirmButton>
        </div>
      </Show>
    </div>
  )
}
