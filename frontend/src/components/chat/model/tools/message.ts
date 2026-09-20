import type { ProseResult } from '../toolCall'

export interface MessageRequest { to?: string, text: string, summary?: string }
export type MessageResult = ProseResult

/** The longest message preview the summary line shows before it clips. */
export const MESSAGE_PREVIEW_LIMIT = 120
