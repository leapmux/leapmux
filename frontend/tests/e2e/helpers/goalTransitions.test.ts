/**
 * Unit tests for the persisted session-goal transition counter.
 *
 * A `.test.ts` under `tests/e2e/` runs under vitest, not Playwright: no browser
 * and no hub, so it costs milliseconds and belongs to `task test-frontend`.
 *
 * Both defects pinned here cost a spec author a two-minute timeout that named
 * the assertion and not the cause, which is the reason this is worth testing at
 * all rather than left to the spec that uses it.
 */
import { zstdCompressSync } from 'node:zlib'
import { describe, expect, it } from 'vitest'
import { ContentCompression } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { countGoalTransitionsInMessages } from './goalTransitions'

function message(body: string, compress: boolean) {
  const raw = new TextEncoder().encode(body)
  return compress
    ? { content: new Uint8Array(zstdCompressSync(raw)), contentCompression: ContentCompression.ZSTD }
    : { content: raw, contentCompression: ContentCompression.NONE }
}

/** One persisted goal transition, as the worker writes it. */
const GOAL_SET = '{"type":"goal_updated","objective":"green build","goal_status":"active"}'

/**
 * Adjacent notifications fold into ONE notification_thread row that carries
 * each entry inside it -- which is why the counter counts type tokens rather
 * than messages.
 */
const THREADED = `{"type":"notification_thread","messages":[${GOAL_SET},`
  + '{"type":"goal_updated","objective":"green build","goal_status":"paused"},'
  + '{"type":"goal_cleared","objective":"green build"}]}'

describe('countGoalTransitionsInMessages', () => {
  it('counts one transition in one message', () => {
    expect(countGoalTransitionsInMessages([message(GOAL_SET, false)])).toBe(1)
  })

  // Counting ROWS would report these three as one, and the spec's ceiling --
  // its assertion that progress reports never reach the transcript -- would
  // then pass however noisy the transcript got.
  it('counts every entry folded into one notification thread', () => {
    expect(countGoalTransitionsInMessages([message(THREADED, false)])).toBe(3)
  })

  /**
   * The defect this counter was written wrong for the first time.
   *
   * Every persisted message is zstd-compressed UNCONDITIONALLY
   * (msgcodec.Compress has no size threshold), so reading the bytes as text
   * finds nothing and reports zero -- indistinguishable from the feature not
   * working, which is exactly how it was misread.
   */
  it('decompresses zstd content rather than reading the bytes as text', () => {
    const compressed = message(THREADED, true)
    expect(countGoalTransitionsInMessages([compressed])).toBe(3)
    // The wrong reading, pinned so the regression stays visible rather than
    // merely absent: those same bytes as text carry no match at all.
    expect(new TextDecoder().decode(compressed.content)).not.toContain('goal_updated')
  })

  it('counts across several messages and ignores unrelated ones', () => {
    expect(countGoalTransitionsInMessages([
      message(GOAL_SET, true),
      message('{"type":"assistant","content":"prose mentioning goal_updated"}', true),
      message('{"type":"goal_cleared","objective":"green build"}', true),
    ])).toBe(2)
  })

  it('reports nothing for an empty page', () => {
    expect(countGoalTransitionsInMessages([])).toBe(0)
  })

  // A message with no bytes decodes to the empty string, which must count zero
  // rather than reach the regex as null and throw.
  it('reports nothing for a message with empty content', () => {
    expect(countGoalTransitionsInMessages([
      { content: new Uint8Array(), contentCompression: ContentCompression.NONE },
    ])).toBe(0)
  })

  // An unreadable body counts zero rather than throwing: the caller polls, and
  // a throw there fails the spec with a stack trace instead of retrying.
  it('survives content it cannot decompress', () => {
    expect(countGoalTransitionsInMessages([
      { content: new Uint8Array([1, 2, 3]), contentCompression: ContentCompression.UNSPECIFIED },
    ])).toBe(0)
  })
})
