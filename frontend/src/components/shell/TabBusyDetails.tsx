import type { Component } from 'solid-js'
import type { TabBusyReason } from './tabBusyProbe'
import { For, Match, Show, Switch } from 'solid-js'
import { pluralize } from '~/lib/plural'
import { busyDetails } from './TabBusyDetails.css'

const AgentBusyDetails: Component<{ reason: Extract<TabBusyReason, { kind: 'agent-turn' }> }> = (props) => {
  const taskCount = () => props.reason.activeTasks.length
  return (
    <>
      {/*
        The Worker reports a root busy for a turn OR for a running background
        task, and the wire carries only the answer, not which one made it true.
        So the turn is claimed only when nothing else can explain the state: a
        turn that ended while a subagent kept running would otherwise be
        announced as "in progress" after it finished.
      */}
      <p>
        {taskCount() === 0
          ? 'This agent\'s turn is in progress. Closing the tab stops it.'
          : 'This agent is still working. Closing the tab stops it.'}
      </p>
      <Show when={taskCount() > 0}>
        <p>{`${pluralize(taskCount(), 'background task')} ${taskCount() === 1 ? 'is' : 'are'} active:`}</p>
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
 * Shared by the single-tab close prompt and the aggregated bulk-close prompt,
 * so a single close and a tile close state the same facts in the same words.
 * It lives in its own module for that reason: a bulk-close dialog importing
 * from the single-tab dialog reads as a dependency that is not there.
 *
 * Switch/Match rather than a Show with a fallback. The fallback form needs a
 * cast to name the other case, and that cast routes ANY future reason kind into
 * the agent branch, where reading its `activeTasks` throws at render.
 */
export const TabBusyDetails: Component<{ reason: TabBusyReason }> = (props) => {
  return (
    <Switch>
      <Match when={props.reason.kind === 'agent-turn' ? props.reason : null}>
        {reason => <AgentBusyDetails reason={reason()} />}
      </Match>
      <Match when={props.reason.kind === 'terminal-processes' ? props.reason : null}>
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
      </Match>
    </Switch>
  )
}
