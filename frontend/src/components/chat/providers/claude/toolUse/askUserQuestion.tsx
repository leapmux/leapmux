import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import Vote from 'lucide-solid/icons/vote'
import { For, Show } from 'solid-js'
import { isObject } from '~/lib/jsonPick'
import { ToolUseLayout } from '../../../toolRenderers'
import { toolInputSummary } from '../../../toolStyles.css'

/** Render AskUserQuestion tool_use with questions and options. Returns null if input is invalid. */
export function renderAskUserQuestion(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element | null {
  const input = toolUse.input
  if (!isObject(input))
    return null

  const questions = (input as Record<string, unknown>).questions as Array<Record<string, unknown>> | undefined
  if (!Array.isArray(questions) || questions.length === 0)
    return null

  const title = questions.length === 1
    ? String(questions[0].question || questions[0].header || 'Question')
    : `${questions.length} questions`

  return (
    <ToolUseLayout
      icon={Vote}
      toolName="AskUserQuestion"
      title={title}
      alwaysVisible={true}
      context={context}
    >
      <For each={questions}>
        {(q) => {
          const header = String(q.header || '')
          const options = Array.isArray(q.options) ? q.options as Array<Record<string, unknown>> : []
          return (
            <div style={{ 'margin-top': '4px' }}>
              <Show when={questions.length > 1}>
                <div><strong>{header}</strong></div>
              </Show>
              <For each={options}>
                {opt => <div class={toolInputSummary}>{String(opt.label || '')}</div>}
              </For>
            </div>
          )
        }}
      </For>
    </ToolUseLayout>
  )
}
