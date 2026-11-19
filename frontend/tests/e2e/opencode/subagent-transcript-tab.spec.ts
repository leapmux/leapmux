import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent, nativeAgentById } from '../helpers/nativeScenario'
import { opencodeTest } from '../opencode-fixtures'
import { exerciseOpencodeSpawnTranscript } from './childScenario'

opencodeTest('subagent spawn creates a prompt and report transcript', async ({ native }, testInfo) => {
  const parent = await currentNativeAgent(native)
  try {
    await exerciseOpencodeSpawnTranscript(native)
  }
  finally {
    const snapshot = await readNativeMessageSnapshot(native, parent.id)
    await testInfo.attach('native-child-parent-state', {
      contentType: 'application/json',
      body: JSON.stringify({
        agent: await nativeAgentById(native, parent.id),
        messages: snapshot.messages.map(message => ({
          id: message.id,
          spanId: message.spanId,
          completion: message.completion,
          frame: nativeMessageBody(message),
        })),
      }, (_key, value: unknown) => typeof value === 'bigint'
        ? value.toString()
        : value, 2),
    })
  }
})
