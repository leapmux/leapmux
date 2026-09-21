import type { LucideIcon } from 'lucide-solid'
import type { JSX } from 'solid-js'
import type { TaskResult, TaskStatus } from '../model/tools/task'
import type { ToolResultRenderContext } from '../renderContext'
import ClockFading from 'lucide-solid/icons/clock-fading'
import { Show } from 'solid-js'
import { getToolResultExpanded } from '../messageRenderers'
import { toolMessage } from '../toolStyles.css'
import { CollapsibleContent } from './CollapsibleContent'
import { ENDED_OUTCOME_ICON } from './endedOutcomeIcon'
import { CommandInputBody } from './multiLineCommandBody'
import { ToolStatusHeader } from './ToolStatusHeader'
import { textNeedsCollapse, useCollapsedLines } from './useCollapsedLines'

/**
 * One glyph for each outcome. Annotated, so a fifth outcome fails the build here.
 *
 * The three ENDED glyphs come from the shared table, which the subagent card reads
 * too: the two used to spell the same three mappings under different words, so a
 * change to the "it stopped" glyph reached one card and not the other.
 */
const OUTCOME_ICON: Record<TaskStatus, LucideIcon> = {
  ...ENDED_OUTCOME_ICON,
  // A task surface that has not answered yet. The subagent card has no glyph for its
  // own `running`, because absence is what tells it the run has not ended.
  running: ClockFading,
}

/** Whether a task note exceeds the collapsed display. */
export function taskResultCollapsible(result: TaskResult): boolean {
  return textNeedsCollapse(result.output)
}

export function StatusResultBody(props: { source: TaskResult, context?: ToolResultRenderContext }): JSX.Element {
  const output = () => props.source.output
  const collapsed = useCollapsedLines({ text: output, expanded: () => getToolResultExpanded(props.context) })
  const body = () => (
    <>
      <Show when={props.source.command}>
        {command => <CommandInputBody command={command()} {...(props.context !== undefined ? { context: props.context } : {})} />}
      </Show>
      <Show when={output()}>
        <CollapsibleContent kind="ansi-or-pre" text={output()} display={collapsed.display()} isCollapsed={collapsed.isCollapsed()} {...(props.context !== undefined ? { context: props.context } : {})} />
      </Show>
    </>
  )
  // A header needs WORDS. `TaskResult.title` is optional -- the surface states no state
  // word for some answers -- and drawing the header anyway put a lone coloured glyph
  // above the note, which tells a reader that something ended and not what. The note
  // keeps its own wrapper, and the row's shared outcome header states the call's
  // outcome instead (`taskRenderer.statesOwnOutcome` asks exactly this question).
  return (
    <Show when={props.source.title} fallback={<div class={toolMessage}>{body()}</div>}>
      {title => (
        <ToolStatusHeader icon={OUTCOME_ICON[props.source.outcome]} title={title()}>
          {body()}
        </ToolStatusHeader>
      )}
    </Show>
  )
}
