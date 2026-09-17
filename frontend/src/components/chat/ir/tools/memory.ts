import type { ProseResult } from '../toolCall'

/**
 * A call that reads or writes a long-lived memory. No schema states its fields
 * yet, so the payload stays raw; the result is the words the tool answered with.
 */
export interface MemoryRequest { payload?: Record<string, unknown> }
export type MemoryResult = ProseResult
