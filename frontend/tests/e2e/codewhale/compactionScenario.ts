import type { NativeCompactionOptions } from '../helpers/manualCompaction'

/**
 * The native compaction of Codewhale. Its summarizer request falls through to the fallback, which answers the summary.
 *
 * Codewhale 0.10.0 ends a compaction with `item.completed` for a `context_compaction` item. The item states
 * "Compaction complete: ..." as its summary, and the event states `auto: false` for a manual compaction. The
 * transcript draws that item as the completed notice, with the trigger that the event states.
 *
 * The summarizer request opens with the 0.10 handoff prompt; 0.9 opened with a
 * context-checkpoint sentence instead.
 */
export const CODEWHALE_COMPACTION: NativeCompactionOptions = {
  summary: { route: 'fallback', requestMarker: 'Write a handoff note so this session' },
}
