import type { ContentMovedInfo } from './chatLayoutPivot'
import type { MeasurementCommitInfo } from './useChatVirtualizer'
import { createLogger } from '~/lib/logger'
import { monotonicNow } from '~/lib/monotonicNow'

// ---------------------------------------------------------------------------
// Scroll-anomaly diagnostics (always-on WARN logging)
//
// The chat list keeps the content under the reader in place across a geometry change
// with a layout shift (see chatLayoutShift.ts). A correct shift is invisible: the element
// at the viewport top keeps its place on screen. The layout pivot (chatLayoutPivot.ts)
// records that element in each frame and reports when it moved on screen with no scroll
// and no move of its row -- a jump that the reader sees. This module turns the report into
// a rate-limited WARN on the `chatScroll` channel, so a jump can be copied from the
// console. It fires without the debug-logging preference: a jump is worth surfacing
// regardless. It stays a pure unit with no DOM, so it runs in the node test project.
// ---------------------------------------------------------------------------

/**
 * The re-warn window (ms), fixed from the last emitted WARN. A row that keeps moving, such
 * as one that collapses under the reader in steps, reports once per window; the next WARN
 * carries the count of the reports that the window suppressed.
 */
export const CONTENT_MOVED_REWARN_MS = 1000

const scrollLog = createLogger('chatScroll')

export interface ContentMovedReporterDeps {
  /** The layout shift at the report. */
  layoutShift: () => number
  /** The last height commit that moved the offset map: the likely cause of the move. */
  lastMeasurement: () => MeasurementCommitInfo | undefined
  /** Clock, injectable for tests. */
  now?: () => number
}

/**
 * Create the reporter that the layout pivot calls with each move of the content at the
 * viewport top. It logs one WARN `content moved without a scroll` at most once per
 * CONTENT_MOVED_REWARN_MS, with the row, the distance, the layout shift, and the last
 * height commit.
 */
export function createContentMovedReporter(deps: ContentMovedReporterDeps): (info: ContentMovedInfo) => void {
  const now = deps.now ?? monotonicNow
  // NEGATIVE_INFINITY so the first report can never fall inside a window measured from a
  // zero epoch (a monotonic clock starts near 0 at page load).
  let lastWarnAt = Number.NEGATIVE_INFINITY
  let suppressed = 0
  return (info) => {
    const at = now()
    if (at - lastWarnAt < CONTENT_MOVED_REWARN_MS) {
      suppressed++
      return
    }
    scrollLog.warn('content moved without a scroll', {
      ...info,
      layoutShift: deps.layoutShift(),
      measurement: deps.lastMeasurement(),
      suppressedSinceLastWarn: suppressed,
    })
    lastWarnAt = at
    suppressed = 0
  }
}
