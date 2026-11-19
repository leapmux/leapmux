// The client half of `leapmux.v1.TabStatus`: the short status that a process
// reports for its tab through `leapmux control tab status set`, which the tab
// bar and the sidebar draw beside the tab's title.
//
// The wire form carries an open proto enum and a progress whose presence is the
// whole signal. This module is the one place that reads it, so each surface
// draws the same flat, closed shape.

import type { TabStatus } from '~/generated/proto/leapmux/v1/worker_private_pb'
import { TabStatusTone } from '~/generated/proto/leapmux/v1/worker_private_pb'

/** The color of a status. The proto states the neutral tone as unspecified. */
export type TabStatusToneName = 'neutral' | 'success' | 'warning' | 'error'

/** A tab's status as the tab bar and the sidebar draw it. */
export interface TabStatusView {
  /** A short text, such as "Running the tests". Never empty. */
  text: string
  /**
   * How far the work is, from 0 to 100, or undefined when the reporter gave
   * none. Undefined draws no progress at all; 0 draws an empty ring.
   */
  progressPercent: number | undefined
  tone: TabStatusToneName
}

/** The largest progress: TabStatus states its progress from 0 to 100. */
const MAX_PROGRESS_PERCENT = 100

/**
 * Read a tone from the wire. A tone that a newer Worker added and this build
 * does not know draws as neutral: a status in the wrong color is still the
 * right status, and dropping the whole status would hide it.
 */
function toneName(tone: TabStatusTone): TabStatusToneName {
  switch (tone) {
    case TabStatusTone.SUCCESS:
      return 'success'
    case TabStatusTone.WARNING:
      return 'warning'
    case TabStatusTone.ERROR:
      return 'error'
    default:
      return 'neutral'
  }
}

/**
 * Decode a wire status. Returns undefined for an absent status, which is how
 * the Worker reports a cleared one, and for a status with no text.
 *
 * The Worker refuses a status without a text and a progress over 100, so the
 * checks here defend the drawing against a Worker that does not, rather than
 * state the rules: an empty text would draw a bare ring with nothing to say,
 * and a progress over 100 would overfill it.
 */
export function tabStatusView(status: TabStatus | undefined): TabStatusView | undefined {
  const text = status?.text.trim() ?? ''
  if (!status || text === '')
    return undefined
  return {
    text,
    progressPercent: status.progressPercent === undefined
      ? undefined
      : Math.min(status.progressPercent, MAX_PROGRESS_PERCENT),
    tone: toneName(status.tone),
  }
}

/**
 * The full text of a status, for its tooltip and its accessible name: the
 * text that the badge may cut, and the progress, which the ring alone shows.
 */
export function tabStatusDescription(status: TabStatusView): string {
  return status.progressPercent === undefined
    ? status.text
    : `${status.text} (${status.progressPercent}%)`
}
