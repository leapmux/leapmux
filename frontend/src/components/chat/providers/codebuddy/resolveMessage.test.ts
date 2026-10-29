import { describe, expect, it } from 'vitest'
import { codebuddyResultDivider } from './extractors/resultDivider'
import { resolveCodebuddyMessage } from './resolveMessage'

describe('resolveCodebuddyMessage', () => {
  const frame = {
    type: 'result',
    is_error: true,
    _requestId: 'request-1',
    session_id: 'session-1',
    errors: ['The request failed.'],
  }
  const extra = { requestId: 'request-1', sessionId: 'session-1', repeatedError: 'The request failed.' }
  const parsed = {
    parentObject: frame,
    topLevel: frame,
    rawText: '',
    wrapper: null,
    supplementalContent: extra,
  }

  it('keeps a failed divider without repeating the preceding diagnostic', () => {
    const resolved = resolveCodebuddyMessage(parsed)
    expect(codebuddyResultDivider(resolved)).toEqual({ label: 'Turn failed', isError: true })
    expect(frame.errors).toEqual(['The request failed.'])
    expect(extra.repeatedError).toBe('The request failed.')
  })

  it('keeps distinct errors in their original order', () => {
    const resolved = resolveCodebuddyMessage({
      ...parsed,
      parentObject: {
        ...frame,
        errors: [
          ...frame.errors,
          ...frame.errors,
          'Another error.',
          'Last error.',
        ],
      },
    })
    expect(resolved?.errors).toEqual(['Another error.', 'Last error.'])
    expect(codebuddyResultDivider(resolved)).toEqual({ label: 'Turn failed — Another error.', isError: true })
  })

  it('keeps the native error when no supplement exists', () => {
    expect(resolveCodebuddyMessage({ ...parsed, supplementalContent: undefined })).toBeUndefined()
    expect(codebuddyResultDivider(frame)).toEqual({ label: 'Turn failed — The request failed.', isError: true })
  })

  it.each([
    null,
    {},
    { ...extra, requestId: '' },
    { ...extra, requestId: 'other' },
    { ...extra, sessionId: ' ' },
    { ...extra, sessionId: 'other' },
    { ...extra, repeatedError: '' },
    { ...extra, repeatedError: 'Other error.' },
    { ...extra, repeatedError: 0 },
  ])('rejects an absent, invalid, or unrelated supplement: %j', (supplementalContent) => {
    expect(resolveCodebuddyMessage({ ...parsed, supplementalContent })).toBeUndefined()
  })

  it.each([
    { ...frame, type: 'assistant' },
    { ...frame, is_error: false },
    { ...frame, errors: [] },
    { ...frame, errors: null },
    { ...frame, errors: 'The request failed.' },
    {
      ...frame,
      errors: ['Another error.', 'The request failed.'],
    },
  ])('does not change another native outcome: %j', (parentObject) => {
    expect(resolveCodebuddyMessage({ ...parsed, parentObject })).toBeUndefined()
  })
})
