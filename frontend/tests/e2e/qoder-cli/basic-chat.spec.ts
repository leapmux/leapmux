import { exerciseBasicChat } from '../helpers/nativeConversation'
import { nativeMessageBody, readAllAgentMessages } from '../helpers/nativeMessages'
import { selectedAgentTabId } from '../helpers/nativeScenario'
import { qoderTest } from '../qoder-fixtures'

qoderTest('ends the actual native turn and keeps its answer after reload', async ({ native }, testInfo) => {
  // The native `result` row states the duration of the turn.
  try {
    await exerciseBasicChat(native, { timedDivider: true })
  }
  catch (error) {
    const messages = await readAllAgentMessages(native, await selectedAgentTabId(native.page))
    await testInfo.attach('qoder-worker-frames', {
      body: JSON.stringify(messages.map(message => ({
        content: nativeMessageBody(message),
        source: message.source,
      }))),
      contentType: 'application/json',
    })
    throw error
  }
})
