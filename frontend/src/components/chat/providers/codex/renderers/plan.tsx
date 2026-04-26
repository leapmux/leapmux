import type { CodexMessageRenderer } from '../defineRenderer'
import { Show } from 'solid-js'
import { isObject } from '~/lib/jsonPick'
import { CODEX_ITEM, CODEX_METHOD } from '~/types/toolMessages'
import { TodoListMessage } from '../../../todoListMessage'
import { MarkdownPlanLayout } from '../../../widgets/MarkdownPlanLayout'
import { defineCodexRenderer } from '../defineRenderer'
import { codexPlanItemMarkdown, codexTurnPlanFromParams } from '../extractors/plan'

function extractPlanParams(parsed: unknown): Record<string, unknown> | null {
  if (!isObject(parsed))
    return null
  if (parsed.method === CODEX_METHOD.TURN_PLAN_UPDATED && isObject(parsed.params))
    return parsed.params as Record<string, unknown>
  if (Array.isArray(parsed.plan))
    return parsed
  return null
}

// Registry-only: dispatched by `item.type === 'plan'` via `CODEX_RENDERERS`
// (loaded from `renderers/registerAll.ts`). Same layout pattern as ExitPlanMode.
defineCodexRenderer({
  itemTypes: [CODEX_ITEM.PLAN],
  render: (props) => {
    const text = (): string | null => codexPlanItemMarkdown(props.item)
    return (
      <Show when={text()}>
        {t => <MarkdownPlanLayout toolName="Plan" title="Proposed Plan" planText={t()} context={props.context} />}
      </Show>
    )
  },
})

/**
 * Renders Codex turn/plan/updated notifications with the same todo-list UI
 * pattern as TodoWrite. Not registered with `defineCodexRenderer` because
 * its dispatch shape is `parent.method === 'turn/plan/updated'` rather than
 * `item.type` — the codex plugin calls it explicitly.
 */
export const CodexTurnPlanRenderer: CodexMessageRenderer = (props) => {
  const source = (): ReturnType<typeof codexTurnPlanFromParams> => codexTurnPlanFromParams(extractPlanParams(props.parsed))
  return (
    <Show when={source()}>
      {s => <TodoListMessage source={s()} context={props.context} />}
    </Show>
  )
}
