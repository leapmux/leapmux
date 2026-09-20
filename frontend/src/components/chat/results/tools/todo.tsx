import type { ToolKindRenderer } from './renderer'
import type { TodoItem } from '~/models/todo'
import ListTodo from 'lucide-solid/icons/list-todo'
import { Show } from 'solid-js'
import { todosToMarkdown } from '~/lib/messageParser'
import { pluralize } from '~/lib/plural'
import { toolCallStatusOutcome } from '../../model/toolCallStatus'
import { toolInputSummary } from '../../toolStyles.css'
import { CollapsibleContent } from '../CollapsibleContent'
import { TodoListBody } from '../todoListBody'

/**
 * What the row states over a checklist the turn cut short.
 *
 * A stopped call KEEPS the list it collected, because those tasks are what the reader
 * asked to see. The list then looks exactly like a finished one, and an empty one reads
 * as `To-do list cleared` -- a claim the call never made. This says which of the two a
 * reader holds. It states a fact about the list and no error: the row's own
 * `Interrupted` header already carries the outcome.
 */
export const TODO_PARTIAL_LIST_NOTICE = 'The turn stopped, so this list is partial'

/** One checklist and the note under it, as the markdown the toolbar copies and quotes. */
function todoCopyable(items: TodoItem[], note: string | undefined): string | null {
  return [todosToMarkdown(items), note].filter(Boolean).join('\n\n') || null
}

export const todoRenderer: ToolKindRenderer<'todo'> = {
  icon: ListTodo,
  label: 'Todo',
  title(call) {
    // The call's own words lead: a plan states WHY it moved beside its size, and a
    // single-task card states which half of the task it acted on.
    if (call.title)
      return call.title
    const items = call.result?.items ?? call.request.items
    // 'To-do list', not 'To-do list cleared': the BODY one line below already states
    // that the list is empty, in `emptyText`, and a header that said it too printed
    // the same sentence twice on the same row.
    return items.length ? pluralize(items.length, 'task') : 'To-do list'
  },
  request(call, view) {
    // A result row draws the saved list in its own result slot; a request or
    // update row states the list the call CARRIED, which is the newest fact the
    // reader has.
    //
    // A row that reaches this guard has no result row beside it, so it DRAWS the
    // result itself -- and `call.result === undefined` is what keeps the two apart. A
    // Claude `Task*` call fills both slots from one payload, so the lone row drew the
    // checklist and its note once here and once again in the result slot below.
    return (
      <Show when={view.role !== 'result' && !view.hasResultRow && call.result === undefined}>
        <TodoListBody todos={call.request.items} />
        <Show when={call.request.note}>
          {note => <CollapsibleContent kind="markdown-tool-result" text={note()} isCollapsed={false} {...(view.context !== undefined ? { context: view.context } : {})} />}
        </Show>
      </Show>
    )
  },
  result(call, view) {
    // The ROW's own status, and not a field of the list. The interruption is a fact
    // about the CALL, which every provider already folds into that one word, so the
    // marker needs no producer to carry a second copy of it.
    return (
      <>
        <TodoListBody todos={call.result.items} {...(call.result.emptyText !== undefined ? { emptyText: call.result.emptyText } : {})} />
        <Show when={toolCallStatusOutcome(call.status) === 'interrupted'}>
          <div class={toolInputSummary}>{TODO_PARTIAL_LIST_NOTICE}</div>
        </Show>
        {call.result.note ? <CollapsibleContent kind="markdown-tool-result" text={call.result.note} isCollapsed={false} {...(view.context !== undefined ? { context: view.context } : {})} /> : null}
      </>
    )
  },
  /**
   * The list the call CARRIED, offered while no result answers for it.
   *
   * A running `TodoWrite` is a whole row -- header, checklist, note -- with no result
   * behind it, so `resultMeta` never runs and the row had NOTHING to copy and nothing
   * to quote. `hasResult` is the same fact the body's own guard reads: once any result
   * exists, the row draws the SAVED list instead and `resultMeta` states that one.
   */
  requestMeta(call, hasResult) {
    return {
      collapsible: false,
      hasDiff: false,
      copyableContent: () => hasResult ? null : todoCopyable(call.request.items, call.request.note),
    }
  },
  resultMeta(call) {
    return {
      collapsible: false,
      hasDiff: false,
      copyableContent: () => todoCopyable(call.result.items, call.result.note),
    }
  },
}
