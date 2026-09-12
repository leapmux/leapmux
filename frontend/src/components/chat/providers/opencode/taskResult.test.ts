import { describe, expect, it } from 'vitest'
import { openCodeTaskResult } from './taskResult'

const wrap = (text: string, state = 'completed', tag = 'task_result') => `<task id="child" state="${state}">\n<${tag}>\n${text}\n</${tag}>\n</task>`

describe('opencode task output', () => {
  it('preserves nested task tags, newlines, and Markdown inside the report', () => {
    const report = `**Report**\n\n${wrap('Nested task example')}\nTrailing text\n`
    const source = openCodeTaskResult(wrap(report), { sessionId: 'child', model: { modelID: 'example-model' } }, { description: 'Inspect the code' })
    expect(source?.body).toBe(report)
    expect(source?.outcome).toBe('completed')
    expect(source?.metadata).toContainEqual({ label: 'Model', value: 'example-model' })
  })

  it('keeps an empty completed report distinct from a launch', () => {
    expect(openCodeTaskResult(wrap(''), null, { prompt: 'Do not substitute this prompt' })).toMatchObject({ outcome: 'completed', body: '', bodyLabel: undefined })
  })

  it('retains a failed task report', () => {
    expect(openCodeTaskResult(wrap('The worker failed', 'error', 'task_error'), null, {})).toMatchObject({ outcome: 'failed', body: 'The worker failed' })
  })

  it.each([
    'plain output',
    `${wrap('report')}\nExtra output`,
    wrap('report').replace('</task>', ''),
    wrap('report', 'error'),
    wrap('report', 'completed', 'task_error'),
    wrap('report').replace('<task_result>', '<summary>Missing summary end\n<task_result>'),
    '<task id="child" state="completed">\n<task_result>\n</task_result>\n</task>',
  ])('leaves an unrecognized or incomplete result to the fallback (%s)', (text) => {
    expect(openCodeTaskResult(text, null, {})).toBeNull()
  })

  it('rejects a wrapper for a different native session', () => {
    expect(openCodeTaskResult(wrap('report'), { sessionId: 'another-child' }, {})).toBeNull()
  })
})
