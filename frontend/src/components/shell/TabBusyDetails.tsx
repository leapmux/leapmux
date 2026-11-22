import type { Component } from 'solid-js'
import type { TabBusyReason } from './tabBusyProbe'
import { For, Match, Show, Switch } from 'solid-js'
import { pluralize } from '~/lib/plural'
import { busyDetails } from './TabBusyDetails.css'

export type TabBusyAction = 'close-tab' | 'quit' | 'switch-mode'

function actionPhraseFor(action: TabBusyAction): string {
  switch (action) {
    case 'quit':
      return 'Quitting'
    case 'switch-mode':
      return 'Switching mode'
    case 'close-tab':
      return 'Closing the tab'
  }
}

const AgentBusyDetails: Component<{
  reason: Extract<TabBusyReason, { kind: 'agent-work' }>
  actionPhrase: string
}> = (props) => {
  const tasks = () => props.reason.activeBackgroundTasks
  const childActivities = () => props.reason.activeChildAgents
  const taskCount = () => tasks().length
  const childCount = () => childActivities().length
  return (
    <>
      {/* Exact working state can come from a root turn, a task, or child activity.
          The wire does not identify which one caused it. */}
      <p>{`This agent is still working. ${props.actionPhrase} stops it.`}</p>
      <Show when={taskCount() > 0}>
        <p>
          {`${pluralize(taskCount(), 'background task')} ${taskCount() === 1
            ? 'is'
            : 'are'} active:`}
        </p>
        <ul class={busyDetails} data-testid="busy-background-tasks">
          <For each={tasks()}>
            {title => <li>{title}</li>}
          </For>
        </ul>
      </Show>
      <Show when={childCount() > 0}>
        <p>
          {`${pluralize(childCount(), 'child agent')} ${childCount() === 1
            ? 'is'
            : 'are'} active:`}
        </p>
        <ul class={busyDetails} data-testid="busy-child-activities">
          <For each={childActivities()}>{title => <li>{title}</li>}</For>
        </ul>
      </Show>
    </>
  )
}

/**
 * One busy tab's reason and the work it would interrupt.
 *
 * Shared by the single-tab close prompt, the aggregated bulk-close prompt, and
 * the desktop solo-mode quit/switch-mode prompt so every surface states the
 * same facts in the same words.
 *
 * Switch/Match rather than a Show with a fallback. The fallback form needs a
 * cast to name the other case, and that cast routes ANY future reason kind into
 * the agent branch, where reading its `activeBackgroundTasks` throws at render.
 */
export const TabBusyDetails: Component<{ reason: TabBusyReason, action?: TabBusyAction }> = (props) => {
  const actionPhrase = () => actionPhraseFor(props.action ?? 'close-tab')
  return (
    <Switch>
      <Match when={props.reason.kind === 'agent-work'
        ? props.reason
        : null}
      >
        {reason => <AgentBusyDetails reason={reason()} actionPhrase={actionPhrase()} />}
      </Match>
      <Match when={props.reason.kind === 'terminal-processes'
        ? props.reason
        : null}
      >
        {(reason) => {
          const processCount = () => Math.max(reason().totalCount, reason().processes.length)
          return (
            <>
              <p>
                {`${pluralize(processCount(), 'process', 'processes')} still ${processCount() === 1
                  ? 'runs'
                  : 'run'} in this terminal. ${actionPhrase()} stops ${processCount() === 1
                  ? 'it'
                  : 'them'}.`}
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
          )
        }}
      </Match>
    </Switch>
  )
}
