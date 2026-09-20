import { describe, expect, it } from 'vitest'
import { TOOL_CALL_STATUSES } from '../../model/toolCallStatus'
import { fileChangeFailed } from './fileChanges'

describe('fileChangeFailed', () => {
  // The three states where the tool applied nothing, so the file took nothing.
  // `declined` is the one a reader REFUSED: the tool never ran, so a row that drew
  // its changes drew the identical body a PENDING row draws.
  it.each(['failed', 'cancelled', 'declined'] as const)('answers true for a %s call, which changed no file', (status) => {
    expect(fileChangeFailed(status)).toBe(true)
  })

  it.each(['unstated', 'pending', 'in_progress', 'completed', 'incomplete'] as const)('answers false for a %s call, whose changes still stand', (status) => {
    expect(fileChangeFailed(status)).toBe(false)
  })

  // Every status takes one side, so a new one cannot arrive with no answer here.
  it('answers for every row status the model declares', () => {
    const answered = TOOL_CALL_STATUSES.filter(status => typeof fileChangeFailed(status) === 'boolean')
    expect(answered).toEqual([...TOOL_CALL_STATUSES])
  })
})
