import { expect } from '@playwright/test'
import { exerciseBasicChat } from '../helpers/nativeConversation'
import { nativeModelLastUserText } from '../helpers/nativeScenario'
import { qwenTest } from '../qwen-fixtures'

qwenTest.describe('Qwen Code Basic Chat', () => {
  qwenTest('send message and receive response', async ({ native }) => {
    const { prompt, request } = await exerciseBasicChat(native)
    // The prompt is the last user text of the request, not text in an earlier row or an instruction.
    expect(nativeModelLastUserText(request)).toContain(prompt)
  })
})
