import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import { For } from 'solid-js'
import { isObject } from '~/lib/jsonPick'
import { toolMessage, toolResultPrompt } from '../../../toolStyles.css'

/** AskUserQuestion result view: shows questions with selected answers. */
export function AskUserQuestionResultView(props: {
  toolUseResult: Record<string, unknown>
  context?: RenderContext
}): JSX.Element {
  const questions = () => Array.isArray(props.toolUseResult.questions)
    ? props.toolUseResult.questions as Array<Record<string, unknown>>
    : []
  const answers = () => isObject(props.toolUseResult.answers)
    ? props.toolUseResult.answers as Record<string, string>
    : {}

  return (
    <div class={toolMessage}>
      <For each={questions()}>
        {(q) => {
          const header = String(q.header || '')
          const answer = answers()[header]
          return (
            <div class={toolResultPrompt}>
              <strong>
                {header}
                {': '}
              </strong>
              {answer || <em>Not answered</em>}
            </div>
          )
        }}
      </For>
    </div>
  )
}
