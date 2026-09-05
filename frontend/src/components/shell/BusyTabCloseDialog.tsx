import type { Component } from 'solid-js'
import type { TabBusyReason } from './tabBusyProbe'
import { For, Show } from 'solid-js'
import { ConfirmDialog } from '~/components/common/ConfirmDialog'
import { pluralize } from '~/lib/plural'
import { busyDetails } from './BusyTabCloseDialog.css'

export interface BusyTabConfirmState {
  /** The tab's label, so the prompt names what it is about to interrupt. */
  tabTitle: string
  reason: TabBusyReason
  resolve: (confirmed: boolean) => void
}

const AgentBusyDetails: Component<{ reason: Extract<TabBusyReason, { kind: 'agent-turn' }> }> = (props) => {
  return (
    <>
      <p>This agent's turn is in progress. Closing the tab stops it.</p>
      <Show when={props.reason.activeTasks.length > 0}>
        <p>{`${pluralize(props.reason.activeTasks.length, 'background task')} ${props.reason.activeTasks.length === 1 ? 'is' : 'are'} active:`}</p>
        <ul class={busyDetails} data-testid="busy-background-tasks">
          <For each={props.reason.activeTasks}>
            {task => <li>{task.title}</li>}
          </For>
        </ul>
      </Show>
    </>
  )
}

/**
 * One busy tab's reason and the work it would interrupt.
 *
 * Shared with the aggregated bulk-close prompt, so a single close and a
 * tile close state the same facts in the same words.
 */
export const TabBusyDetails: Component<{ reason: TabBusyReason }> = (props) => {
  return (
    <Show
      when={props.reason.kind === 'terminal-processes' ? props.reason : null}
      fallback={<AgentBusyDetails reason={props.reason as Extract<TabBusyReason, { kind: 'agent-turn' }>} />}
    >
      {reason => (
        <>
          <p>
            {`${pluralize(reason().processes.length, 'process', 'processes')} still ${reason().processes.length === 1 ? 'runs' : 'run'} in this terminal. Closing the tab stops ${reason().processes.length === 1 ? 'it' : 'them'}.`}
          </p>
          <ul class={busyDetails} data-testid="busy-processes">
            <For each={reason().processes}>
              {proc => (
                // The name can be empty: macOS resolves a long name through a
                // second syscall that fails for another user's process, and the
                // worker reports the pid rather than dropping the process.
                <li>{`${proc.name || 'unnamed process'} (pid ${proc.pid})`}</li>
              )}
            </For>
            <Show when={reason().totalCount > reason().processes.length}>
              <li>{`and ${reason().totalCount - reason().processes.length} more`}</li>
            </Show>
          </ul>
        </>
      )}
    </Show>
  )
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
