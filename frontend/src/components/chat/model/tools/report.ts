import type { ProseResult } from '../toolCall'

/**
 * A call whose answer is a report. `StructuredOutput` sends a free-form payload,
 * so it rides raw until a schema types it.
 *
 * `proposal` is the one field of that payload a renderer draws on its own, so it is
 * typed here rather than read back out of the untyped bag. Cursor sends its plan
 * under it; a provider that sends none leaves it absent.
 */
export interface ReportRequest { payload?: Record<string, unknown>, proposal?: string }
export type ReportResult = ProseResult
