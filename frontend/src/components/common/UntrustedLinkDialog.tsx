import type { Component } from 'solid-js'
import type { UntrustedLinkConfirmRequest } from '~/lib/untrustedLinks'
import { Show } from 'solid-js'
import { ConfirmDialog } from '~/components/common/ConfirmDialog'
import { labelRow } from '~/components/common/Dialog.css'
import * as styles from './UntrustedLinkDialog.css'

interface UntrustedLinkDialogProps {
  request: UntrustedLinkConfirmRequest
  /** True opens the link, false drops it. Closes the dialog either way. */
  onResolve: (approved: boolean) => void
}

/**
 * The one sentence that states what is wrong, strongest case first.
 *
 * A terminal prints whatever a program tells it to, including an OSC 8
 * hyperlink whose text names one site and whose address points at another. The
 * user reads the text, so the text is what the sentence answers.
 */
function leadSentence(request: UntrustedLinkConfirmRequest): string {
  // "looks like an address" rather than "is one": the same test accepts a
  // filename, and this sentence has to stay true of `README.md`.
  if (request.misleadingLabel)
    return 'The link text looks like an address. It is not the address that this link opens.'
  if (request.labelMismatch)
    return 'The link text does not name the address that it opens.'
  return 'This address is not encrypted.'
}

/**
 * Asks the user before an untrusted link opens: a hyperlink a program printed
 * into a terminal, or an anchor an agent wrote into markdown.
 *
 * ONE dialog for both, because both carry the same risk. It appears only for a
 * link that gives a reason to ask: the shown text disagrees with the address,
 * or the address is `http:` to a host that is not loopback. A link whose text
 * spells its own `https:` address opens with no prompt, because a prompt on
 * every link teaches the reader to confirm without reading.
 */
export const UntrustedLinkDialog: Component<UntrustedLinkDialogProps> = props => (
  <ConfirmDialog
    title="Open this link?"
    confirmLabel="Open link"
    // Two-click arming for the two reasons that carry real risk: a label that
    // states an address it does not open, and an address that puts what the
    // reader sends on a network in the clear. A prose label such as `Click
    // here` states no destination to misstate, so it prompts without arming --
    // it is also the most common shape a program prints, and arming it would
    // only teach the reader to double-click.
    danger={props.request.misleadingLabel !== null || props.request.insecure}
    data-testid="untrusted-link-dialog"
    confirmTestId="untrusted-link-open"
    cancelTestId="untrusted-link-cancel"
    onConfirm={() => props.onResolve(true)}
    onCancel={() => props.onResolve(false)}
  >
    <div class="vstack gap-3">
      <p>{leadSentence(props.request)}</p>
      <Show when={props.request.label !== ''}>
        <div>
          <div class={labelRow}>Shown</div>
          <div class={styles.linkValue} data-testid="untrusted-link-shown">{props.request.label}</div>
        </div>
      </Show>
      <div>
        <div class={labelRow}>Opens</div>
        <div class={styles.linkValue} data-testid="untrusted-link-target">{props.request.uri}</div>
      </div>
      <Show when={props.request.insecure}>
        <p class={styles.note}>
          The address uses http, not https. Anything that you send to it travels unencrypted.
        </p>
      </Show>
    </div>
  </ConfirmDialog>
)
