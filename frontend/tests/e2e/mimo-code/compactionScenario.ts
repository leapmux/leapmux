import type { NativeCompactionOptions } from '../helpers/manualCompaction'

/**
 * MiMo Code requests its summary through the fallback route.
 * The native part reports a start and an end. The worker publishes the start as live state and stores only the end.
 */
export const MIMO_COMPACTION: NativeCompactionOptions = {
  summary: { route: 'fallback', requestMarker: 'Write a continuation summary that will allow you' },
}
