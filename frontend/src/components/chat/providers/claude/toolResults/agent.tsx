import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { ClaudeAgentResult } from '../extractors/agent'
import Bot from 'lucide-solid/icons/bot'
import Check from 'lucide-solid/icons/check'
import { createMemo, For, Show } from 'solid-js'
import { clipFirstLine } from '~/lib/clipFirstLine'
import { getToolResultExpanded } from '../../../messageRenderers'
import { CollapsibleContent } from '../../../results/CollapsibleContent'
import { ToolStatusHeader } from '../../../results/ToolStatusHeader'
import { useCollapsedFlag } from '../../../results/useCollapsedLines'
import { toolMetaLabel, toolMetaList, toolMetaRow, toolMetaValue, toolResultPrompt } from '../../../toolStyles.css'
import { claudeAgentResultBody, claudeAgentResultIsLaunch } from '../extractors/agent'

function formatAgentStatus(status: string): string {
  switch (status) {
    case 'async_launched': return 'launched asynchronously'
    case 'remote_launched': return 'launched remotely'
    default: return status
  }
}

/**
 * The longest task description the header shows. The same cap the tool_use
 * titles apply to model prose (toolUse/title.tsx, toolUse/summary.tsx).
 */
const DESCRIPTION_LIMIT = 80

/**
 * The first line of the task description, capped.
 *
 * `description` is free model prose with no length limit and no line limit --
 * unlike every model-supplied title the backend stores, which passes
 * bgtask.FirstLine + bgtask.TruncateRunes. The header is ONE clipped line that
 * ENDS with the outcome, so an overlong description pushed `completed` /
 * `failed` / `launched asynchronously` off the right edge, and the icon
 * separates only `completed` from everything else -- so a failed run read as a
 * launched one.
 */
function clipDescription(description: string): string {
  return clipFirstLine(description, DESCRIPTION_LIMIT)
}

/**
 * The header line. A launch gives the TASK, because that is the thing the user
 * recognizes and the only human-written string in the payload; the agent id is a
 * generated token that says nothing about the work. A finished sync run carries
 * no description at all, so it falls back to the id.
 */
function agentTitle(source: ClaudeAgentResult): string {
  const status = formatAgentStatus(source.status)
  const description = clipDescription(source.description)
  if (description)
    return `Agent '${description}' ${status}`
  const id = source.agentId || source.taskId
  return id ? `Agent ${id} ${status}` : `Agent ${status}`
}

/**
 * The identity and location rows.
 *
 * A launch is the one result the user may need to act on later -- to message the
 * agent, or to look at where it writes its output -- and every field
 * needed for that arrives here and nowhere else. The CLI tells the model not to
 * surface the agent id; that instruction protects the model's own reply, not
 * this UI, which is the user's own view of their own session.
 */
function metaRows(source: ClaudeAgentResult): Array<{ label: string, value: string }> {
  const rows: Array<{ label: string, value: string }> = []
  if (source.agentId)
    rows.push({ label: 'Agent ID', value: source.agentId })
  if (source.taskId)
    rows.push({ label: 'Task ID', value: source.taskId })
  if (source.sessionUrl)
    rows.push({ label: 'Session', value: source.sessionUrl })
  // A mid-run model swap is worth showing in full: `resolvedModel` alone reports
  // only where the run ended up, which reads as though it ran on that model
  // throughout.
  if (source.modelsUsed.length > 1)
    rows.push({ label: 'Models', value: source.modelsUsed.join(' → ') })
  else if (source.resolvedModel)
    rows.push({ label: 'Model', value: source.resolvedModel })
  if (source.worktreeBranch)
    rows.push({ label: 'Branch', value: source.worktreeBranch })
  if (source.worktreePath)
    rows.push({ label: 'Worktree', value: source.worktreePath })
  if (source.outputFile)
    rows.push({ label: 'Output', value: source.outputFile })
  return rows
}

/**
 * The Agent result card: a title that gives the task, the structured fields as a
 * label/value list, and one collapsible body -- the agent's report when it
 * finished, the prompt it was given when it has only just launched.
 */
export function AgentResultView(props: {
  source: ClaudeAgentResult
  context?: RenderContext
}): JSX.Element {
  const body = () => claudeAgentResultBody(props.source)
  // One evaluation, one array identity. metaRows builds fresh row objects, and
  // it is read twice below -- by the Show gate and by the For -- so without the
  // memo the field list is rebuilt on each read and <For> gets items it can
  // never reconcile by identity.
  const rows = createMemo(() => metaRows(props.source))
  const isCollapsed = useCollapsedFlag({
    text: body,
    expanded: () => getToolResultExpanded(props.context),
  })
  const icon = () => props.source.status === 'completed' ? Check : Bot
  // A launch's body is the instruction it was given, which needs saying: without
  // the label it reads as though the agent already answered at length.
  const bodyLabel = () => claudeAgentResultIsLaunch(props.source) ? 'Prompt' : ''

  return (
    <ToolStatusHeader icon={icon()} title={agentTitle(props.source)}>
      <Show when={rows().length > 0}>
        <div class={toolMetaList}>
          <For each={rows()}>
            {row => (
              <div class={toolMetaRow}>
                <span class={toolMetaLabel}>{`${row.label}:`}</span>
                <span class={toolMetaValue}>{row.value}</span>
              </div>
            )}
          </For>
        </div>
      </Show>
      <Show when={body()}>
        <Show when={bodyLabel()}>
          <div class={toolResultPrompt}>{bodyLabel()}</div>
        </Show>
        <CollapsibleContent
          kind="markdown-tool-result"
          text={body()}
          isCollapsed={isCollapsed()}
          context={props.context}
        />
      </Show>
    </ToolStatusHeader>
  )
}
