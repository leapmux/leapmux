import type { Component } from 'solid-js'
import { Show } from 'solid-js'
import { DialogTopSection } from '~/components/common/Dialog'
import { errorText } from '~/styles/shared.css'

interface BlockedReasonNoticeProps {
  /**
   * Why submit is disabled — a precondition outside the dialog (no
   * placeable tile, an archived workspace). Nothing renders when empty.
   */
  reason: string | undefined
}

/**
 * The blocked-precondition notice shared by the tab-creating dialogs:
 * one shape, one testid, so New Agent, New Terminal, and the
 * Change-branch worktree flow cannot drift on how the reason renders.
 */
export const BlockedReasonNotice: Component<BlockedReasonNoticeProps> = props => (
  <Show when={props.reason}>
    {reason => (
      <DialogTopSection>
        <div class={errorText} data-testid="new-tab-blocked-reason">{reason()}</div>
      </DialogTopSection>
    )}
  </Show>
)
