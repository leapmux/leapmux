import { expect } from '@playwright/test'
import { cursorTest } from '../cursor-fixtures'
import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { nativeContext } from './scenarios'

function cursorConversationId(body: unknown): string | undefined {
  if (body === null || typeof body !== 'object' || !('conversationId' in body))
    return undefined
  return typeof body.conversationId === 'string' ? body.conversationId : undefined
}

const label = 'Cursor'

cursorTest('restores old Worker rows and native model context after reopening', async ({ page, leapmuxServer, modelScript }) => {
  let firstConversationId: string | undefined
  await resumePickerScenario({ page, leapmuxServer, modelScript }, nativeContext, {
    label,
    resumedBodyHoldsOriginalAnswer: false,
    onFirstTurn: (request) => {
      firstConversationId = cursorConversationId(request.body)
      expect(firstConversationId).toBeTruthy()
    },
    onResumedRequest: resumed => expect(cursorConversationId(resumed.body)).toBe(firstConversationId),
  })
})
