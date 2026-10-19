import { droidTest } from '../droid-fixtures'
import { nativeMessageBody, nativeMessageSupplement, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { exerciseModelRetry } from '../helpers/nativeModelRetry'
import { currentNativeAgent } from '../helpers/nativeScenario'

droidTest('discards a failed stream attempt and resolves the retry row', async ({ native }, testInfo) => {
  await exerciseModelRetry(native, { errors: [{ status: 500, code: 'server_error', message: 'Service unavailable', midStream: true }] })
  const agent = await currentNativeAgent(native)
  const snapshot = await readNativeMessageSnapshot(native, agent.id)
  await testInfo.attach('native-retry-transcript', {
    contentType: 'application/json',
    body: JSON.stringify(snapshot.messages.map(message => ({ seq: String(message.seq), source: message.source, body: nativeMessageBody(message), supplement: nativeMessageSupplement(message) })), null, 2),
  })
})
