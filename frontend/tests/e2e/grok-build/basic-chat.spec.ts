import { expect } from '@playwright/test'
import { grokTest } from '../grok-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'
import { nativeModelLastUserText } from '../helpers/nativeScenario'

grokTest.describe('Grok Build Basic Chat', () => {
  grokTest('send message and receive response', async ({ native }) => {
    const { prompt, request } = await exerciseBasicChat(native)
    // The prompt is the last user text of the request, not text in an earlier row or an instruction.
    expect(nativeModelLastUserText(request)).toContain(prompt)
  })
})
