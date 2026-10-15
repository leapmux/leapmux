import { expect } from '@playwright/test'
import { withCleanup } from '../helpers/cleanup'
import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptedPartialAnswer, exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { qoderTest } from '../qoder-fixtures'
import { hasQoderPartialTextReceipt } from './partialTextReceipt'
import { attachQoderWorkerFrames } from './workerFrames'

qoderTest('stops a native model turn and resumes its paused queue', async ({ native }) => {
  await exerciseInterruptTurn(native)
})

qoderTest('stops an actual native tool and accepts the next queued turn', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})

qoderTest('withdraws a waiting question and accepts the next queued turn', async ({ native }) => {
  await exerciseControlInterrupt(native, { control: 'question' })
})

qoderTest('retains interrupted native text after a verified stream receipt and reload', async ({ native }, testInfo) => {
  const agent = await currentNativeAgent(native)
  await withCleanup(() => exerciseInterruptedPartialAnswer(native, {
    waitForPartialReceipt: async (text) => {
      await retryUntilPass(async () => {
        const snapshot = await readNativeMessageSnapshot(native, agent.id)
        expect(hasQoderPartialTextReceipt(snapshot.messages.map(nativeMessageBody), text), 'the Worker stores the exact root text delta before interruption').toBe(true)
      })
    },
  }), () => attachQoderWorkerFrames(testInfo, native.leapmuxServer, agent.id))
})
