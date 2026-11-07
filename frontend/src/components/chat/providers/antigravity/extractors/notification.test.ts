import { describe, expect, it } from 'vitest'
import { antigravityNotificationEntry } from './notification'

describe('antigravityNotificationEntry', () => {
  it.each([
    [
      { userErrorMessage: ' user ', shortError: ' short ' },
      'user\n\nshort',
    ],
    [
      { userErrorMessage: ' same ', shortError: 'same' },
      'same',
    ],
    [
      { modelErrorMessage: ' model ' },
      'model',
    ],
    [
      { fullError: ' full ' },
      'full',
    ],
    [
      { future: 'retained' },
      'Antigravity CLI reported an error.',
    ],
  ])('formats native error details %j', (error, text) => {
    expect(antigravityNotificationEntry({
      type: 'CORTEX_STEP_TYPE_ERROR_MESSAGE',
      errorMessage: { error },
    })).toEqual([
      {
        kind: 'text',
        text: text === 'Antigravity CLI reported an error.'
          ? text
          : `Antigravity CLI reported an error: ${text}`,
      },
    ])
  })
})
