import { codebuddyTest } from '../codebuddy-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { exerciseModelError } from '../helpers/nativeModelError'
import { currentNativeAgent } from '../helpers/nativeScenario'

codebuddyTest('shows the native model error and runs a later valid turn', async ({ native }, testInfo) => {
  await withCleanup(async () => {
    await exerciseModelError(native, { queueAfterFailure: 'running' })
  }, async () => {
    const agent = await currentNativeAgent(native)
    const snapshot = await readNativeMessageSnapshot(native, agent.id)
    await testInfo.attach('native-error-transcript', {
      contentType: 'application/json',
      body: JSON.stringify(snapshot.messages.map(message => ({ seq: String(message.seq), source: message.source, body: nativeMessageBody(message) }))),
    })
  })
})
