import type { ClaudeToolRow } from './toolCommon'
import { describe, expect, it } from 'vitest'
import { typedResult } from '../../../model/toolCall'
import { claudeTaskSpec } from './task'
import { claudeRequestFor } from './toolRequests'

function row(toolUseResult: Record<string, unknown>): ClaudeToolRow {
  return { input: {}, toolUseResult, resultContent: '' } as ClaudeToolRow
}

const args = { toolName: 'TaskOutput', input: { task_id: 'task-42' }, toolUseResult: null, resultContent: '' } as unknown as ClaudeToolRow
const request = claudeRequestFor('task', args.input, { toolName: args.toolName, result: undefined, context: {} })

/** The state a call reports, in the words the shared outcome vocabulary states. */
function outcomeOf(status: string): string | undefined {
  const payload = claudeTaskSpec(request, args, row({ task: { status, task_id: 'task-42' } }))
  const result = typedResult(payload)
  return result && 'outcome' in result ? result.outcome : undefined
}

/**
 * Claude's own status words, mapped onto the shared run vocabulary.
 *
 * The task card and the subagent card read ONE vocabulary now, so this mapping is what
 * keeps Claude's wire words out of it. `completed` maps to itself, which is the point:
 * before the merge it had to be rewritten to `succeeded` for this surface alone.
 */
describe('claudeTaskSpec outcome', () => {
  it.each([
    ['completed', 'completed'],
    ['failed', 'failed'],
    ['error', 'failed'],
    ['killed', 'stopped'],
    ['stopped', 'stopped'],
    ['cancelled', 'stopped'],
  ])('reads the %s status as %s', (status, outcome) => {
    expect(outcomeOf(status)).toBe(outcome)
  })

  // Anything the four words do not cover is a task still working, which the card draws
  // with the waiting glyph rather than an ended one.
  it.each(['running', 'pending', '', 'something-newer'])('reads the %s status as still running', (status) => {
    expect(outcomeOf(status)).toBe('running')
  })

  it('states no result for a call that has not answered', () => {
    const payload = claudeTaskSpec(request, args, undefined)
    expect(payload.result).toBeUndefined()
    expect(payload.request.taskId).toBe('task-42')
  })
})

/**
 * A FAILED task call states its reason under the failed brand.
 *
 * A failed call carries no `task` object and no `message`, so both fall-through rungs
 * are the ones a failure reaches. `unparsedResult` there claims the call COMPLETED,
 * which invariant I4 rejects under the failed status the row carries -- and the two
 * brands draw the same pixels, so nothing else states the difference.
 */
describe('claudeTaskSpec failure rung', () => {
  const failedRow = (toolUseResult: Record<string, unknown> | undefined): ClaudeToolRow =>
    ({ input: {}, toolUseResult, resultContent: 'No such task', isError: true }) as ClaudeToolRow

  it('states the reason alone for a TaskOutput the tool failed', () => {
    const payload = claudeTaskSpec(request, args, failedRow(undefined))
    expect(payload.result).toStrictEqual({ failure: true, text: 'No such task' })
  })

  it('states the reason alone for a TaskStop the tool failed', () => {
    const stopArgs = { ...args, toolName: 'TaskStop' } as ClaudeToolRow
    const payload = claudeTaskSpec(request, stopArgs, failedRow({}))
    expect(payload.result).toStrictEqual({ failure: true, text: 'No such task' })
  })

  // The rung leads even when a payload rides beside the reason. The stop card takes
  // its `message` from the result CONTENT when the payload states none, so without the
  // rung the error sentence drew as the title of the task the call stopped.
  it('states the reason alone for a failed stop that carries a payload', () => {
    const stopArgs = { ...args, toolName: 'TaskStop' } as ClaudeToolRow
    const payload = claudeTaskSpec(request, stopArgs, failedRow({ message: 'Task already ended' }))
    expect(payload.result).toStrictEqual({ failure: true, text: 'No such task' })
  })

  // A stop that did NOT fail keeps its card, and that card words the outcome `stopped`.
  it('keeps the stop card of a call the tool did not fail', () => {
    const stopArgs = { ...args, toolName: 'TaskStop' } as ClaudeToolRow
    const payload = claudeTaskSpec(request, stopArgs, row({ message: 'Task stopped' }))
    // A record that states no command OMITS the key rather than stating undefined.
    expect(payload.result).toStrictEqual({ title: 'Task stopped', outcome: 'stopped', output: '' })
  })
})
