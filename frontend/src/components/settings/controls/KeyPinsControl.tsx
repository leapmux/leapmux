import type { Component } from 'solid-js'
import { createMemo, createSignal, For, Show } from 'solid-js'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { ClippedText } from '~/components/common/ClippedText'
import { ConfirmButton } from '~/components/common/ConfirmButton'
import { Tooltip } from '~/components/common/Tooltip'
import { formatLocalDateTime } from '~/lib/dateFormat'
import { clearAllKeyPins, clearKeyPin, listKeyPins } from '~/lib/keyPinStore'
import * as styles from './KeyPinsControl.css'

/**
 * The trusted-worker-keys editor: every TOFU pin with its worker id, first
 * pinned time, a remove button per row, and a "Remove all" danger action.
 * Reads re-render from a local revision signal — the pin store is plain
 * browser storage, not a reactive source.
 *
 * Layout: on desktop the id, date, and Remove share one row (id clips with a
 * tooltip when long). On phone the id takes the full card width and the date
 * + Remove sit underneath, so a long id never collapses into a one-character
 * column beside the button.
 *
 * Both remove actions use ConfirmButton (arm, then confirm) so a stray tap
 * cannot wipe pins. Per-key Remove keeps the small outline chrome.
 */
export const KeyPinsControl: Component = () => {
  const [revision, setRevision] = createSignal(0)
  // Memoized: the guard below and the list both read it, and each read
  // re-parses the stored pin document and re-sorts it.
  const pins = createMemo(() => {
    void revision()
    return listKeyPins()
  })

  const remove = (workerId: string) => {
    clearKeyPin(workerId)
    setRevision(r => r + 1)
  }

  const removeAll = () => {
    clearAllKeyPins()
    setRevision(r => r + 1)
  }

  return (
    <div class="vstack gap-2" data-testid="key-pins">
      <Show
        when={pins().length > 0}
        fallback={<div class={styles.empty}>No worker keys pinned on this device.</div>}
      >
        <For each={pins()}>
          {pin => (
            <div class={styles.pinRow} data-worker={pin.workerId}>
              <ClippedText text={pin.workerId} class={styles.pinWorker} testId={`key-pin-id-${pin.workerId}`} />
              <div class={styles.pinMeta}>
                <span class={styles.pinDate}>{formatLocalDateTime(new Date(pin.firstSeen))}</span>
                <Tooltip text="Remove" ariaLabel>
                  <ConfirmButton
                    class="small outline"
                    data-testid={`key-pin-remove-${pin.workerId}`}
                    onClick={() => remove(pin.workerId)}
                  >
                    Remove
                  </ConfirmButton>
                </Tooltip>
              </div>
            </div>
          )}
        </For>
        <div class={actionsFooter}>
          <ConfirmButton data-variant="danger" onClick={removeAll} data-testid="key-pins-remove-all">
            Remove all
          </ConfirmButton>
        </div>
      </Show>
    </div>
  )
}
