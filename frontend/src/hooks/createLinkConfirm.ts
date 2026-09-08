import type { DialogState } from './createDialogState'
import type { UntrustedLinkConfirm, UntrustedLinkConfirmRequest } from '~/lib/untrustedLinks'

/** What the link prompt acts on while it is open. */
export interface LinkConfirmState {
  request: UntrustedLinkConfirmRequest
  resolve: (approved: boolean) => void
}

/**
 * Build the prompt that every untrusted link -- a terminal hyperlink, an
 * anchor in agent-authored markdown -- must pass.
 *
 * ONE function for both surfaces, because they carry the same risk and must
 * not answer it differently. It lives beside the dialog handle rather than in
 * either surface's own module for the same reason.
 *
 * The dialog holds ONE request. A second click while the first prompt is up
 * answers the first with `false` and takes its place, so the user's latest
 * click is what the dialog asks about and no promise is left pending -- an
 * abandoned resolver would park the earlier `activateUntrustedLink` call
 * forever.
 */
export function createLinkConfirm(dialog: DialogState<LinkConfirmState>): UntrustedLinkConfirm {
  return request => new Promise<boolean>((resolve) => {
    dialog.value()?.resolve(false)
    dialog.open({ request, resolve })
  })
}
