import type { Component } from 'solid-js'
import type { TabBusyReason } from './tabBusyProbe'
import { ConfirmDialog } from '~/components/common/ConfirmDialog'
import { TabBusyDetails } from './TabBusyDetails'

export interface BusyTabConfirmState {
  /** The tab's label, so the prompt names what it is about to interrupt. */
  tabTitle: string
  reason: TabBusyReason
  resolve: (confirmed: boolean) => void
}

/**
 * Confirmation shown when the user closes a tab that is still working.
 *
 * Only for a tab the worktree prompt does NOT cover. A tab that is the last one
 * for its worktree keeps LastTabCloseDialog, busy or not, so one click never
 * raises two dialogs.
 *
 * `danger` makes the primary a ConfirmButton, so the destructive answer takes
 * two clicks and Enter cannot bypass the arming.
 */
export const BusyTabCloseDialog: Component<{
  state: BusyTabConfirmState
  /** Called after resolve() to clear the dialog from the parent. */
  onDismiss: () => void
}> = (props) => {
  const answer = (confirmed: boolean) => {
    props.state.resolve(confirmed)
    props.onDismiss()
  }
  return (
    <ConfirmDialog
      title="Close tab"
      data-testid="busy-tab-close-dialog"
      confirmLabel="Close anyway"
      confirmTestId="busy-tab-close-confirm"
      cancelTestId="busy-tab-close-cancel"
      danger
      onConfirm={() => answer(true)}
      onCancel={() => answer(false)}
    >
      <section>
        <p>
          <strong>{props.state.tabTitle}</strong>
        </p>
        <TabBusyDetails reason={props.state.reason} />
      </section>
    </ConfirmDialog>
  )
}
