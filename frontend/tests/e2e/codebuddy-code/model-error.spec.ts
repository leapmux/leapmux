import { codebuddyTest } from '../codebuddy-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { exerciseModelError, exerciseTransientModelFailure } from '../helpers/nativeModelError'
import { currentNativeAgent } from '../helpers/nativeScenario'

codebuddyTest('shows the native model error and runs a later valid turn', async ({ native }, testInfo) => {
  await withCleanup(async () => {
    await exerciseModelError(native, { queueAfterFailure: 'running' })
  }, async () => {
    const agent = await currentNativeAgent(native)
    const snapshot = await readNativeMessageSnapshot(native, agent.id)
    await testInfo.attach('native-error-transcript', {
      contentType: 'application/json',
      body: JSON.stringify(snapshot.messages.map(message => ({
        seq: String(message.seq),
        source: message.source,
        body: nativeMessageBody(message),
      }))),
    })
  })
})

codebuddyTest('schedules LeapMux\'s own server-error retry after the native client gives up', async ({ native }) => {
  // CodeBuddy 2.175.0 retries a 5xx eight times. Without a `retry-after` header
  // its backoff starts at 1 s and doubles to 32 s, about 127 s in all, which
  // exceeds the test budget. It waits as long as the header states instead, so
  // each error states one second.
  await exerciseTransientModelFailure(native, {
    error: {
      status: 500,
      code: 'server_error',
      message: 'The server had an error while processing your request.',
      retryAfterSeconds: 1,
    },
    attempts: 9,
  })
})
