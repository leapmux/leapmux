import { withCleanup } from '../helpers/cleanup'
import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptedPartialAnswer, exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { qoderTest } from '../qoder-fixtures'
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
  await withCleanup(() => exerciseInterruptedPartialAnswer(native), () => attachQoderWorkerFrames(testInfo, native.leapmuxServer, agent.id))
})
