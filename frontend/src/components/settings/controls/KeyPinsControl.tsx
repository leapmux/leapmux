import type { Component } from 'solid-js'
import type { KeyPinEntry } from '~/lib/keyPinStore'
import { createEffect, createSignal, For, onCleanup, Show } from 'solid-js'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { ClippedText } from '~/components/common/ClippedText'
import { ConfirmButton } from '~/components/common/ConfirmButton'
import { onStorageAccountChange, storageAccountId } from '~/lib/browserStorage'
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
  const [revision, setRevision] = createSignal(0)
  /**
   * The last list that read cleanly, and the storage account it was read
   * under. Held rather than re-derived, because a failure must not blank a
   * view whose data is still correct.
   */
  const [known, setKnown] = createSignal<{ pins: KeyPinEntry[], account: string | null } | null>(null)
  const [readError, setReadError] = createSignal<string | null>(null)
  const [removeError, setRemoveError] = createSignal<string | null>(null)
  const [busy, setBusy] = createSignal(false)

  // A failure keeps the list only while the account it was read under is
  // still the live one. A same-account failure left the pins where they were;
  // an account change makes the list data of an account this document no
  // longer holds.
  function keepKnownAfterFailure(): void {
    setKnown(prev => (prev !== null && prev.account !== null && storageAccountId() === prev.account
      ? prev
      : null))
  }

  function readPins(): void {
    // The account is captured BEFORE the read leaves. A read answered for an
    // account this document no longer holds is disregarded — publishing it
    // would show one account's pins as another's.
    const account = storageAccountId()
    void (async () => {
      try {
        const pins = await listKeyPins()
        if (account !== storageAccountId())
          return
        setKnown({ pins, account })
        setReadError(null)
      }
      catch (err) {
        if (account !== storageAccountId())
          return
        setReadError(formatErrorMessage(err, 'Could not read the trusted worker keys.'))
        keepKnownAfterFailure()
      }
    })()
  }

  createEffect(() => {
    void revision()
    readPins()
  })

  // Durable pin state moves underneath this view: a committed update or
  // removal in any tab, a noticed expiry, and a move of the account namespace
  // all mean "what is on screen may be stale". Re-read on each.
  onCleanup(onKeyPinsChanged(() => setRevision(r => r + 1)))
  // Show previously read rows only for the account they were read under: an
  // account change drops the list at once rather than presenting it to its
  // successor.
  onCleanup(onStorageAccountChange(() => setKnown(null)))

  async function removeOne(workerId: string): Promise<void> {
    if (busy())
      return
    setBusy(true)
    setRemoveError(null)
    try {
      await clearKeyPin(workerId)
      setRevision(r => r + 1)
    }
    catch (err) {
      setRemoveError(formatErrorMessage(err, 'Could not remove the trusted worker key. Try again.'))
      // The removal failed, so the pin is still pinned: the last known list
      // stays on screen beside the reason rather than losing its row.
      keepKnownAfterFailure()
    }
    finally {
      setBusy(false)
    }
  }

  async function removeAll(): Promise<void> {
    if (busy())
      return
    setBusy(true)
    setRemoveError(null)
    try {
      await clearAllKeyPins()
      setRevision(r => r + 1)
    }
    catch (err) {
      setRemoveError(formatErrorMessage(err, 'Could not remove the trusted worker keys. Try again.'))
      keepKnownAfterFailure()
    }
    finally {
      setBusy(false)
    }
  }

  return (
    <div class="vstack gap-2" data-testid="key-pins">
      <Show when={readError()}>
        <div class={errorText} role="alert" data-testid="key-pins-read-error">
          {readError()}
          {' '}
          <button type="button" class="outline" onClick={() => setRevision(r => r + 1)} data-testid="key-pins-retry">
            Retry
          </button>
        </div>
      </Show>
      <Show when={removeError()}>
        <div class={errorText} role="alert" data-testid="key-pins-remove-error">
          {removeError()}
        </div>
      </Show>
      <Show
        when={(known()?.pins.length ?? 0) > 0}
        fallback={<div class={styles.empty}>No worker keys pinned on this device.</div>}
      >
        <For each={known()?.pins ?? []}>
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
                  onClick={() => void removeOne(pin.workerId)}
                >
                  Remove
                </ConfirmButton>
              </div>
            </div>
          )}
        </For>
        <div class={actionsFooter}>
          <ConfirmButton data-variant="danger" disabled={busy()} onClick={() => void removeAll()} data-testid="key-pins-remove-all">
            Remove all
          </ConfirmButton>
        </div>
      </Show>
    </div>
  )
}
