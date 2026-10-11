import { exerciseBasicChat } from '../helpers/nativeConversation'
import { qwenTest } from '../qwen-fixtures'

qwenTest.describe('Qwen Code Basic Chat', () => {
  qwenTest('send message and receive response', async ({ native }) => {
    await exerciseBasicChat(native)
  })
})
