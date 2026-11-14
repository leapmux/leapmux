import type { ContentMovedInfo } from './chatLayoutPivot'
import type { MeasurementCommitInfo } from './useChatVirtualizer'
import type { LoggerCapture } from '~/test-support/loggerCapture'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { captureLoggerOutput } from '~/test-support/loggerCapture'
import { CONTENT_MOVED_REWARN_MS, createContentMovedReporter } from './chatScrollDiagnostics'

const MEASUREMENT: MeasurementCommitInfo = {
  id: 'r7',
  kind: 'agent_text',
  source: 'visible',
  firstMeasure: false,
  assumedHeight: 400,
  newHeight: 300,
  delta: -100,
  commitSeq: 12,
}

const MOVE: ContentMovedInfo = { rowId: 'r7', movedPx: -6, scrollTop: 1200 }

describe('createContentMovedReporter', () => {
  let capture: LoggerCapture

  beforeEach(() => {
    capture = captureLoggerOutput()
  })

  afterEach(() => {
    capture.restore()
  })

  /** A reporter over a clock that the test sets. */
  function reporter() {
    let now = 0
    const report = createContentMovedReporter({
      layoutShift: () => -240,
      lastMeasurement: () => MEASUREMENT,
      now: () => now,
    })
    return {
      report,
      at: (ms: number) => {
        now = ms
      },
    }
  }

  /** The payload object of each WARN on the `chatScroll` channel. */
  const payloads = () => vi.mocked(console.warn).mock.calls.filter(args => args[0] === '[chatScroll]').map(args => args[2] as Record<string, unknown>)

  it('warns with the row, the distance, the shift, and the last measurement', () => {
    const { report } = reporter()
    report(MOVE)
    expect(capture.messages('warn', 'chatScroll')).toEqual(['content moved without a scroll'])
    expect(payloads()).toEqual([
      expect.objectContaining({
        rowId: 'r7',
        movedPx: -6,
        scrollTop: 1200,
        layoutShift: -240,
        measurement: MEASUREMENT,
        suppressedSinceLastWarn: 0,
      }),
    ])
  })

  it('warns once per second', () => {
    const { report, at } = reporter()
    report(MOVE)
    at(400)
    report(MOVE)
    at(CONTENT_MOVED_REWARN_MS - 1)
    report(MOVE)
    expect(capture.messages('warn', 'chatScroll')).toHaveLength(1)
  })

  it('reports the suppressed count on the next warn', () => {
    const { report, at } = reporter()
    report(MOVE)
    at(400)
    report(MOVE)
    at(800)
    report(MOVE)
    at(CONTENT_MOVED_REWARN_MS + 1)
    report(MOVE)
    expect(capture.messages('warn', 'chatScroll')).toHaveLength(2)
    expect(payloads()[1]).toMatchObject({ suppressedSinceLastWarn: 2 })
  })

  it('measures the window from the last warn, not from the last report', () => {
    const { report, at } = reporter()
    report(MOVE)
    at(900)
    report(MOVE)
    at(CONTENT_MOVED_REWARN_MS)
    report(MOVE)
    expect(capture.messages('warn', 'chatScroll')).toHaveLength(2)
  })
})
