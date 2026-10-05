import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CURSOR_E2E_SKIP_REASON } from '../cursor-fixtures'
import { test } from '../fixtures'
import { resumePickerScenario } from '../helpers/nativeResumePicker'
import { cursorModelTurns } from './modelTurns'

function cursorConversationId(body: unknown): string | undefined {
  if (body === null || typeof body !== 'object' || !('conversationId' in body))
    return undefined
  return typeof body.conversationId === 'string' ? body.conversationId : undefined
}

const provider = AgentProvider.CURSOR

const label = 'Cursor'

test.skip(!!CURSOR_E2E_SKIP_REASON, CURSOR_E2E_SKIP_REASON || '')

test('restores old Worker rows and native model context after reopening', async ({ page, leapmuxServer, modelScript }) => {
  let firstConversationId: string | undefined
  await resumePickerScenario({ page, leapmuxServer, modelScript }, {
    provider,
    label,
    assertConversationBubbles: true,
    // Cursor sends no history. The service holds the conversation that the reopened agent continues.
    conversationTurns: cursorModelTurns,
    resumedBodyHoldsOriginalAnswer: false,
    onFirstTurn: (status) => {
      firstConversationId = cursorConversationId(status.requests.find(request => request.stepIndex === 0)?.body)
      expect(firstConversationId).toBeTruthy()
    },
    onResumedRequest: resumed => expect(cursorConversationId(resumed.body)).toBe(firstConversationId),
  })
})
