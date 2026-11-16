import type { Component } from 'solid-js'
import type { TabBusyReason } from './tabBusyProbe'
import { createMemo, For, Match, Show, Switch } from 'solid-js'
import { BACKGROUND_TASK_ROLE_CAPABILITIES } from '~/generated/contracts/background-task-roles'
import { pluralize } from '~/lib/plural'
import { busyDetails } from './TabBusyDetails.css'

const AgentBusyDetails: Component<{ reason: Extract<TabBusyReason, { kind: 'agent-work' }> }> = (props) => {
  const tasks = createMemo(() => props.reason.activeRows.filter(row => BACKGROUND_TASK_ROLE_CAPABILITIES[row.role]?.countsAsTask))
  const childActivities = createMemo(() => props.reason.activeRows.filter((row) => {
    const role = BACKGROUND_TASK_ROLE_CAPABILITIES[row.role]
    return role?.canonicalChildActivity === true && !role.countsAsTask
  }))
  const taskCount = () => tasks().length
  const childCount = () => childActivities().length
  return (
    <>
      {/* Exact working state can come from a root turn, a task, or child activity.
          The wire does not identify which one caused it. */}
      <p>This agent is still working. Closing the tab stops it.</p>
      <Show when={taskCount() > 0}>
        <p>
          {`${pluralize(taskCount(), 'background task')} ${taskCount() === 1
            ? 'is'
            : 'are'} active:`}
        </p>
        <ul class={busyDetails} data-testid="busy-background-tasks">
          <For each={tasks()}>
            {task => <li>{task.title}</li>}
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
          <For each={childActivities()}>{child => <li>{child.title}</li>}</For>
        </ul>
      </Show>
    </>
  )
}

/**
 * One busy tab's reason and the work it would interrupt.
 *
 * Shared by the single-tab close prompt and the aggregated bulk-close prompt,
 * so a single close and a tile close state the same facts in the same words.
 * It lives in its own module for that reason: a bulk-close dialog importing
 * from the single-tab dialog reads as a dependency that is not there.
 *
 * Switch/Match rather than a Show with a fallback. The fallback form needs a
 * cast to name the other case, and that cast routes ANY future reason kind into
 * the agent branch, where reading its `activeRows` throws at render.
 */
export const TabBusyDetails: Component<{ reason: TabBusyReason }> = (props) => {
  return (
    <Switch>
      <Match when={props.reason.kind === 'agent-work'
        ? props.reason
        : null}
      >
        {reason => <AgentBusyDetails reason={reason()} />}
      </Match>
      <Match when={props.reason.kind === 'terminal-processes'
        ? props.reason
        : null}
      >
        {reason => (
          <>
            <p>
              {`${pluralize(reason().processes.length, 'process', 'processes')} still ${reason().processes.length === 1
                ? 'runs'
                : 'run'} in this terminal. Closing the tab stops ${reason().processes.length === 1
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
        )}
      </Match>
    </Switch>
  )
}
