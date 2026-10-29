import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeContext } from './scenarios'

codebuddyTest('stops a native model turn and resumes its paused queue', async ({ native }, testInfo) => {
  await exerciseInterruptTurn(native)
  const agent = await currentNativeAgent(native)
  const snapshot = await readNativeMessageSnapshot(native, agent.id)
  await testInfo.attach('native-interrupt-transcript', {
    contentType: 'application/json',
    body: JSON.stringify(snapshot.messages.map(message => ({
      seq: String(message.seq),
      source: message.source,
      body: nativeMessageBody(message),
    }))),
  })
})

codebuddyTest('stops an actual native tool and accepts the next queued turn', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})

// Default mode raises a permission request so this test can interrupt an ordinary tool before it runs.
codebuddyTest('withdraws a waiting permission and accepts the next queued turn', async ({ askingCodebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: askingCodebuddyWorkspace.workspaceId,
  })
  await exerciseControlInterrupt(context, { control: 'permission' })
})
