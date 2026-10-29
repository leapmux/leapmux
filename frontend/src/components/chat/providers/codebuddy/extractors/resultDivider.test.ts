import { describe, expect, it } from 'vitest'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { codebuddyResultDivider } from './resultDivider'

describe('codebuddyResultDivider', () => {
  it('appends the first result error to the failed divider', () => {
    expect(codebuddyResultDivider({
      type: 'result',
      is_error: true,
      errors: [' Request refused ', 'Another error'],
    }))
      .toEqual({ label: 'Turn failed — Request refused', isError: true })
  })

  it.each([
    undefined,
    null,
    [],
    'invalid',
    [7, 'later'],
    ['  '],
    {},
  ])('ignores malformed or absent errors %s', (errors) => {
    expect(codebuddyResultDivider({ type: 'result', is_error: true, errors }))
      .toEqual({ label: 'Turn failed', isError: true })
  })

  it('keeps the interruption and success classifications', () => {
    expect(codebuddyResultDivider({
      type: 'result',
      is_error: true,
      errors: ['Interrupted'],
    }, MessageCompletion.INTERRUPTED))
      .toEqual({ label: 'Turn interrupted' })
    expect(codebuddyResultDivider({
      type: 'result',
      is_error: false,
      errors: ['unused'],
    }))
      .toEqual({ label: 'Turn ended' })
  })

  it('keeps the failed subtype and native duration', () => {
    expect(codebuddyResultDivider({ type: 'result', subtype: 'error_during_execution', duration_ms: 2000 }))
      .toEqual({ label: 'Turn failed (2.0s)', isError: true })
  })

  it('rejects another frame or malformed input', () => {
    expect(codebuddyResultDivider(null)).toBeNull()
    expect(codebuddyResultDivider({ type: 'system' })).toBeNull()
  })
})
