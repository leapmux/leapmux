import type { ProseResult } from '../toolCall'

export interface TriggerRequest {
  action: 'create' | 'delete' | 'list' | 'get' | 'update' | 'run' | 'other'
  triggerId?: string
  name?: string
  schedule?: string
}
export type TriggerResult = ProseResult
