import type { MockModelRequestRecord } from './mockModelScript'
import { randomUUID } from 'node:crypto'
import { describe, expect, it } from 'vitest'
import { failedTurnRequests, nativeErrorMarker } from './nativeModelError'

function record(fields: Partial<MockModelRequestRecord>): MockModelRequestRecord {
  return { protocol: 'anthropic-messages', path: '/v1/messages', body: {}, ...fields }
}

describe('failedTurnRequests', () => {
  it('returns the requests that consumed the failed turn steps, in step order', () => {
    const retry = record({ stepIndex: 4, body: { attempt: 'retry' } })
    const first = record({ stepIndex: 3, body: { attempt: 'first' } })
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
    expect(failedTurnRequests([record({ stepIndex: 5 })], 5, 2)).toHaveLength(1)
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

  it.each(['429', '500', '502', '503', '504', '524'])('is not a message that OpenCode retries, for a UUID that holds %s', (status) => {
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
    ['a UUID that is one digit short', FAILED_RUN_UUID.slice(0, -1)],
    ['a UUID that is one digit long', `${FAILED_RUN_UUID}0`],
  ])('refuses %s', (_name, uuid) => {
    expect(() => nativeErrorMarker(uuid)).toThrow('32 hexadecimal digits')
  })
})
