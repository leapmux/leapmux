import type { MockModelRequestRecord } from './mockModelScript'
import type { NativeScenarioContext } from './nativeScenario'
import { randomUUID } from 'node:crypto'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { exerciseModelError, exerciseTransientModelFailure, failedTurnRequests, nativeErrorMarker } from './nativeModelError'

const errorRows = vi.hoisted(() => ({
  count: 1,
  afterReload: 1,
  checks: [] as number[],
  events: [] as string[],
}))
vi.mock('./ui', () => ({
  sendMessage: vi.fn(async () => {}),
  waitForAgentIdle: async () => { errorRows.events.push('idle') },
  resumeQueueAfterFailure: vi.fn(async () => {}),
  resumePausedQueue: vi.fn(async () => { errorRows.events.push('resume') }),
  visibleOnly: (value: unknown) => value,
  messageContents: () => ({
    filter: () => ({ errorRows: true }),
  }),
}))
vi.mock('./nativeConversation', () => ({
  sendNativeAnswer: vi.fn(async () => {
    errorRows.events.push('answer')
  }),
}))
vi.mock('./autoRetry', () => ({
  pauseQueueUnlessPaused: vi.fn(async () => {
    errorRows.events.push('pause')
    return true
  }),
  deleteAutoRetryRow: vi.fn(async (_page: unknown, kind: string) => { errorRows.events.push(`delete ${kind}`) }),
}))
vi.mock('@playwright/test', () => ({
  expect: (value: unknown) => value && typeof value === 'object' && 'errorRows' in value
    ? {
        first: () => value,
        toBeVisible: async () => { expect(errorRows.count).toBeGreaterThan(0) },
        toHaveCount: async (count: number) => {
          errorRows.checks.push(count)
          expect(errorRows.count).toBe(count)
        },
      }
    : expect(value),
}))

describe('exerciseModelError', () => {
  beforeEach(() => {
    errorRows.count = 1
    errorRows.afterReload = 1
    errorRows.checks = []
    errorRows.events = []
  })

  function context(consumed: number[] = [0], queued: unknown[][] = []): NativeScenarioContext {
    const row = {
      errorRows: true,
      first: () => row,
    }
    return {
      page: {
        getByText: () => row,
        reload: async () => {
          errorRows.events.push('reload')
          errorRows.count = errorRows.afterReload
        },
      },
      modelScript: {
        queue: async (...steps: unknown[]) => {
          queued.push(steps)
          return 0
        },
        prompt: (text: string) => text,
        waitForSteps: async () => ({
          requests: consumed.map(stepIndex => record({ stepIndex })),
        }),
      },
    } as unknown as NativeScenarioContext
  }

  it('requires exactly one error live and after a reload', async () => {
    await exerciseModelError(context(), { queueAfterFailure: 'paused' })
    expect(errorRows.checks).toEqual([1, 1])
    expect(errorRows.events).toEqual(['idle', 'reload', 'idle', 'answer'])
  })

  it('scripts every ordinary failed attempt before checking the transcript', async () => {
    const queued: unknown[][] = []
    const error = { status: 400, message: 'refused' }
    const answered: unknown[] = []
    const failed = await exerciseModelError(context([2, 0, 1], queued), {
      queueAfterFailure: 'paused',
      attempts: 3,
      error,
      answerFailure: async (failure) => {
        answered.push(failure)
        errorRows.events.push('answer failure')
      },
    })
    expect(queued).toEqual([
      [
        { error },
        { error },
        { error },
      ],
    ])
    expect(failed.map(request => request.stepIndex)).toEqual([0, 1, 2])
    expect(answered).toEqual([error])
    expect(errorRows.events).toEqual([
      'answer failure',
      'idle',
      'reload',
      'idle',
      'answer',
    ])
  })

  it('rejects an ordinary turn whose failed attempts were not all consumed', async () => {
    await expect(exerciseModelError(context([0]), { queueAfterFailure: 'paused', attempts: 2 }))
      .rejects
      .toMatchObject({
        actual: [0],
        expected: [0, 1],
      })
    expect(errorRows.events).toEqual([])
    expect(errorRows.checks).toEqual([])
  })

  it.each([
    0,
    -1,
    1.5,
    Number.NaN,
    Number.POSITIVE_INFINITY,
  ])('refuses %s ordinary attempts before sending a prompt', async (attempts) => {
    const queued: unknown[][] = []
    await expect(exerciseModelError(context([], queued), { queueAfterFailure: 'paused', attempts }))
      .rejects
      .toThrow('positive whole number of attempts')
    expect(queued).toEqual([])
    expect(errorRows.events).toEqual([])
  })

  it.each(['live', 'stored'])('rejects duplicate errors in the %s transcript', async (phase) => {
    if (phase === 'live')
      errorRows.count = 2
    else
      errorRows.afterReload = 2
    await expect(exerciseModelError(context(), { queueAfterFailure: 'paused' })).rejects.toThrow('expected 2 to be 1')
  })
})

describe('exerciseTransientModelFailure', () => {
  beforeEach(() => {
    errorRows.events = []
  })

  function context(consumed: number[], queued: unknown[][]): NativeScenarioContext {
    return {
      page: {},
      modelScript: {
        queue: async (...steps: unknown[]) => {
          queued.push(steps)
          errorRows.events.push(`queue ${steps.length}`)
          return 4
        },
        prompt: (text: string) => text,
        waitForSteps: async (step: number) => {
          errorRows.events.push(`wait ${step}`)
          return {
            requests: consumed.map(stepIndex => record({ stepIndex })),
          }
        },
      },
    } as unknown as NativeScenarioContext
  }

  it('scripts every attempt, pauses before the checks, and deletes the retry before the recovery', async () => {
    const queued: unknown[][] = []
    const error = { status: 503, message: 'Service Unavailable' }
    const answered: unknown[] = []

    const failed = await exerciseTransientModelFailure(context([4, 5, 6], queued), {
      error,
      attempts: 3,
      answerFailure: async (failure) => {
        answered.push(failure)
        errorRows.events.push('answer failure')
      },
    })

    expect(queued).toEqual([
      [
        { error },
        { error },
        { error },
      ],
    ])
    expect(answered).toEqual([error])
    expect(failed.map(request => request.stepIndex)).toEqual([4, 5, 6])
    expect(errorRows.events).toEqual([
      'queue 3',
      'wait 7',
      'pause',
      'answer failure',
      'idle',
      'delete server error',
      'resume',
      'answer',
    ])
  })

  it('deletes a retry of the kind that the caller states', async () => {
    await exerciseTransientModelFailure(context([4], []), {
      error: { status: 429, message: 'limit', retryAfterSeconds: 18000 },
      attempts: 1,
      retryKind: 'usage limit',
    })
    expect(errorRows.events).toContain('delete usage limit')
  })

  it('fails when the native client consumed fewer steps than the stated attempts', async () => {
    // The step check compares the consumed steps with the stated attempts.
    await expect(exerciseTransientModelFailure(context([4], []), {
      error: { status: 503, message: 'x' },
      attempts: 2,
    }))
      .rejects
      .toThrow(/expected \[ 4 \] to deeply equal \[ 4, 5 \]/)
    expect(errorRows.events).not.toContain('delete server error')
  })

  it.each([0, -1, 1.5])('refuses %s attempts before it sends anything', async (attempts) => {
    await expect(exerciseTransientModelFailure(context([], []), {
      error: { status: 503, message: 'x' },
      attempts,
    }))
      .rejects
      .toThrow('positive whole number of attempts')
    expect(errorRows.events).toEqual([])
  })
})

function record(fields: Partial<MockModelRequestRecord>): MockModelRequestRecord {
  return {
    protocol: 'anthropic-messages',
    path: '/v1/messages',
    body: {},
    ...fields,
  }
}

describe('failedTurnRequests', () => {
  it('returns the requests that consumed the failed turn steps, in step order', () => {
    const retry = record({
      stepIndex: 4,
      body: { attempt: 'retry' },
    })
    const first = record({
      stepIndex: 3,
      body: { attempt: 'first' },
    })
    expect(failedTurnRequests([retry, first], 3, 2)).toEqual([first, retry])
  })

  it('leaves out the steps before and after the failed turn', () => {
    const earlier = record({ stepIndex: 2 })
    const failed = record({ stepIndex: 3 })
    const later = record({ stepIndex: 4 })
    expect(failedTurnRequests([earlier, failed, later], 3, 1)).toEqual([failed])
  })

  it('leaves out a request that a rule or the fallback answered', () => {
    const ruled = record({ rule: 'title' })
    const fallback = record({ fallback: true })
    const failed = record({ stepIndex: 0 })
    expect(failedTurnRequests([ruled, fallback, failed], 0, 1)).toEqual([failed])
  })

  it('returns fewer requests than attempts when a step was never consumed', () => {
    expect(failedTurnRequests([
      record({ stepIndex: 5 }),
    ], 5, 2)).toHaveLength(1)
  })

  it('returns no request for an empty record list', () => {
    expect(failedTurnRequests([], 0, 1)).toEqual([])
  })

  it.each([0, -1, 1.5, Number.NaN])('refuses %s attempts', (attempts) => {
    expect(() => failedTurnRequests([], 0, attempts)).toThrow('positive whole number of attempts')
  })

  it.each([-1, 0.5])('refuses a first step of %s', (firstStep) => {
    expect(() => failedTurnRequests([], firstStep, 1)).toThrow('nonnegative first step')
  })
})

/**
 * The patterns of `RETRYABLE_MESSAGE_PATTERNS` in OpenCode 1.18.34
 * (`packages/opencode/src/session/retry.ts`). The installed binary holds the same list.
 * The first pattern lacks the `i` flag of the original. The flag has no effect on digits.
 * OpenCode retries a failed model request when the message of the error, or the body of the
 * response, matches one of them, whatever the HTTP status is. A retry waits 2, 4, 8, 16 and
 * 32 seconds (plus jitter), and OpenCode 1.18.34 has no setting that turns it off.
 */
const OPENCODE_RETRYABLE_MESSAGE_PATTERNS: readonly RegExp[] = [
  /429|500|502|503|504|524/,
  /rate increased too quickly|rate limit|rate-limit|rate_limit|too many requests/i,
  /overloaded|service unavailable|service_unavailable|service-unavailable|internal error|internal_error|internal server error|server error|server_error|server-error|provider returned error|provider_returned_error|provider-returned-error/i,
  /terminated|fetch failed|failed to fetch|network[-_\s]error|upstream connect|connection error|connection refused|connection lost|socket connection was closed|socket hang up|reset before headers|getaddrinfo|enotfound|eai_again|econnrefused|econnreset|etimedout/i,
  /^timeout$|\b(?:request|response|connection|network|stream|read) (?:timeout|timed out|time out)\b/i,
  /try your request again|retry your request|resource exhausted|resource_exhausted/i,
  /\btry again (?:later|in\b)|\b(?:currently|temporarily) at capacity\b/i,
]

function matchingRetryPatterns(text: string): string[] {
  return OPENCODE_RETRYABLE_MESSAGE_PATTERNS.filter(pattern => pattern.test(text)).map(pattern => pattern.source)
}

describe('nativeErrorMarker', () => {
  // The failed run of opencode/model-error.spec.ts: this UUID holds `503`.
  const FAILED_RUN_UUID = 'dbd9879e-8554-4d06-ac6c-477b50353460'

  it('is not a message that OpenCode retries, for the UUID of the failed run', () => {
    expect(matchingRetryPatterns(nativeErrorMarker(FAILED_RUN_UUID))).toEqual([])
  })

  it.each([
    '429',
    '500',
    '502',
    '503',
    '504',
    '524',
  ])('is not a message that OpenCode retries, for a UUID that holds %s', (status) => {
    const uuid = `${status}00000-0000-4000-8000-0000000${status}00`
    expect(matchingRetryPatterns(nativeErrorMarker(uuid))).toEqual([])
  })

  it('holds no digit and no vowel after its prefix, so no status and no word can match', () => {
    for (let draw = 0; draw < 2000; draw++) {
      const marker = nativeErrorMarker()
      expect(marker).toMatch(/^NATIVEERROR[bcdf-hj-np-t]{32}$/)
      expect(matchingRetryPatterns(marker)).toEqual([])
    }
  })

  it('maps each hexadecimal digit to its own letter, so two UUIDs never share a marker', () => {
    const digits = [...'0123456789abcdef']
    const markers = new Set(digits.map(digit => nativeErrorMarker(digit.repeat(32))))
    expect(markers.size).toBe(digits.length)
  })

  it('maps an uppercase UUID and a lowercase UUID to the same marker', () => {
    expect(nativeErrorMarker(FAILED_RUN_UUID.toUpperCase())).toBe(nativeErrorMarker(FAILED_RUN_UUID))
  })

  it('ignores the hyphens of the UUID', () => {
    expect(nativeErrorMarker(FAILED_RUN_UUID)).toBe(nativeErrorMarker(FAILED_RUN_UUID.replaceAll('-', '')))
  })

  it('differs on each call', () => {
    expect(nativeErrorMarker()).not.toBe(nativeErrorMarker())
  })

  it('keeps the length of the marker that a UUID gives', () => {
    expect(nativeErrorMarker(randomUUID())).toHaveLength('NATIVEERROR'.length + 32)
  })

  it.each([
    ['an empty text', ''],
    ['a text that is not hexadecimal', 'not-a-uuid-not-a-uuid-not-a-uuid!!'],
    [
      'a UUID that is one digit short',
      FAILED_RUN_UUID.slice(0, -1),
    ],
    ['a UUID that is one digit long', `${FAILED_RUN_UUID}0`],
  ])('refuses %s', (_name, uuid) => {
    expect(() => nativeErrorMarker(uuid)).toThrow('32 hexadecimal digits')
  })
})
