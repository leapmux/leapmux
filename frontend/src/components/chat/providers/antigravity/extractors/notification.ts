import type { NotificationEntry } from '../../../model/notification'
import { ANTIGRAVITY_FRAME, ANTIGRAVITY_STEP_TYPE } from '~/generated/contracts/antigravity-protocol'
import { pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { retryEntry } from '../../../model/notification'

export function antigravityNotificationEntry(msg: Record<string, unknown>): NotificationEntry[] {
  if (msg.name === ANTIGRAVITY_FRAME.Interaction && msg.modelRequest) {
    const request = msg.modelRequest as Record<string, unknown>
    return [
      retryEntry('api', {
        attempt: pickNumber(request, 'attempt', 0),
        maxAttempts: pickNumber(request, 'maxAttempts', 0),
      }),
    ]
  }
  if (msg.type === ANTIGRAVITY_STEP_TYPE.Checkpoint) {
    return [
      {
        kind: 'compaction',
        phase: 'end',
        detail: { trigger: 'auto' },
      },
    ]
  }
  if (msg.type === ANTIGRAVITY_STEP_TYPE.ErrorMessage) {
    const error = pickObject(pickObject(msg, 'errorMessage'), 'error')
    const user = pickString(error, 'userErrorMessage')
    const short = pickString(error, 'shortError')
    const text = [
      user,
      short !== user
        ? short
        : '',
    ].filter(Boolean).join('\n\n') || pickString(error, 'fullError')
    return text
      ? [
          { kind: 'text', text: `Antigravity CLI reported an error: ${text}` },
        ]
      : []
  }
  return []
}
