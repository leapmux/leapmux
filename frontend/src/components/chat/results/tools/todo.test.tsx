import { render } from '@solidjs/testing-library'
import { beforeAll, describe, expect, it } from 'vitest'
import { failedResult } from '~/components/chat/ir/toolCall'
import { checkKindModule } from '~/test-support/kindTestHarness'
import { toolCallIr, toolRow } from '~/test-support/toolCallIr'
import { ToolMessage } from '../ToolMessage'
import { parsedCall } from './renderer'
import { TODO_PARTIAL_LIST_NOTICE, todoRenderer } from './todo'

// jsdom does not provide ResizeObserver, which the shared layouts observe with.
beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
})

// The one task held as a VALUE: an index into the list reads as possibly
// undefined, and the marker fixtures below spread THIS object.
const ONE_TASK_ITEM = { id: '1', rowKey: '1', content: 'One thing', status: 'pending' as const, activeForm: '' }
const ONE_TASK = [ONE_TASK_ITEM]

describe('todo renderer', () => {
  checkKindModule({
    kind: 'todo',
    request: { items: ONE_TASK, note: 'plan note' },
    titlePart: 'RAW TITLE',
    minimalTitlePart: 'RAW TITLE',
    result: { items: ONE_TASK, emptyText: 'Nothing left' },
    resultPart: 'One thing',
  })

  // The BODY states that a cleared list is empty, in `emptyText` one line below the
  // header. A header that said it too printed the same sentence twice on one row.
  it('heads a cleared list without repeating what the body says', () => {
    const call = toolCallIr('todo', { request: { items: [] } })
    expect(todoRenderer.title(parsedCall(call), undefined)).toBe('To-do list')
  })

  // The header counts the list the row HOLDS: the carried one while the answer is
  // absent, the saved one once it lands.
  it('heads a list that carries tasks with their count', () => {
    const carried = toolCallIr('todo', { status: 'in_progress', request: { items: ONE_TASK } })
    expect(todoRenderer.title(parsedCall(carried), undefined)).toBe('1 task')

    const saved = toolCallIr('todo', { request: { items: [] }, result: { items: ONE_TASK } })
    expect(todoRenderer.title(parsedCall(saved), undefined)).toBe('1 task')
  })

  /**
   * One row that carries BOTH halves draws the checklist once.
   *
   * A Claude `Task*` call fills the request and the result from one payload. The row
   * has no result row beside it, so it draws the result itself -- and the request
   * guard passed as well, so the checklist and its note appeared twice on that row.
   */
  it('draws the checklist once on a row that carries both halves', () => {
    const call = toolCallIr('todo', {
      request: { items: [{ ...ONE_TASK_ITEM, content: 'MARKER-ONE' }], note: 'MARKER-NOTE' },
      result: { items: [{ ...ONE_TASK_ITEM, content: 'MARKER-ONE' }], note: 'MARKER-NOTE' },
    })
    const { container } = render(() => <ToolMessage row={toolRow(call, 'update')} />)
    const text = container.textContent ?? ''
    expect(text.match(/MARKER-ONE/g)).toHaveLength(1)
    expect(text.match(/MARKER-NOTE/g)).toHaveLength(1)
  })

  // A row whose answer has NOT landed still states the list the call carried, which is
  // the newest fact the reader has.
  it('draws the carried checklist while the answer is absent', () => {
    const call = toolCallIr('todo', { status: 'in_progress', request: { items: [{ ...ONE_TASK_ITEM, content: 'MARKER-ONE' }] } })
    const { container } = render(() => <ToolMessage row={toolRow(call, 'update')} />)
    expect(container.textContent).toContain('MARKER-ONE')
  })

  /**
   * A checklist the turn cut short keeps its tasks AND says that it is partial.
   *
   * The tasks that arrived are what the reader asked to see, so the row keeps them. The
   * list then looks exactly like a finished one, and nothing below the header separates
   * the two. The marker reads the ROW's own status, so no producer carries a second copy
   * of a fact every provider already folds into that one word.
   */
  it('marks a checklist the turn stopped as partial', () => {
    const call = toolCallIr('todo', { status: 'cancelled', result: { items: [{ ...ONE_TASK_ITEM, content: 'MARKER-ONE' }] } })
    const { container } = render(() => <ToolMessage row={toolRow(call)} />)
    expect(container.textContent).toContain('MARKER-ONE')
    expect(container.textContent).toContain(TODO_PARTIAL_LIST_NOTICE)
  })

  // The EMPTY case is the one the marker matters most for: the body states
  // `To-do list cleared`, which is a claim the stopped call never made.
  it('marks an empty checklist the turn stopped, which the body calls cleared', () => {
    const call = toolCallIr('todo', { status: 'cancelled', result: { items: [] } })
    const { container } = render(() => <ToolMessage row={toolRow(call)} />)
    expect(container.textContent).toContain('To-do list cleared')
    expect(container.textContent).toContain(TODO_PARTIAL_LIST_NOTICE)
  })

  // Only an INTERRUPTED row. A completed list is whole, and a failed or declined one
  // carries its own outcome header rather than a statement about the list.
  //
  // A DECLINED call never ran, so the refusal is the only result it can state: the
  // declined half of invariant I4 refuses a typed list under that word.
  it.each(['completed', 'failed', 'declined'] as const)('states nothing about a partial list on a %s row', (status) => {
    const result = status === 'declined' ? failedResult('The reader refused the list.') : { items: ONE_TASK }
    const call = toolCallIr('todo', { status, result })
    const { container } = render(() => <ToolMessage row={toolRow(call)} />)
    expect(container.textContent).not.toContain(TODO_PARTIAL_LIST_NOTICE)
  })
})
