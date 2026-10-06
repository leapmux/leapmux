import type { NativeCompactionOptions } from '../helpers/manualCompaction'

/** The native compaction of ZCode. Its summarizer takes the next ordered step. */
export const ZCODE_COMPACTION: NativeCompactionOptions = { summary: { route: 'queued' } }
