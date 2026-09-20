import type { ProseResult } from '../toolCall'

/** A call that manages agents rather than launches one: list, search, team facts. */
export interface AgentsRequest {
  channel?: string
  query?: string
  team?: { name: string, description?: string }
}
export type AgentsResult = ProseResult
