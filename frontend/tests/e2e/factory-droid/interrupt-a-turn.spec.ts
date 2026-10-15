import { GENERATION_TRACE_ENV } from '../../../src/generated/contracts/generation-trace'
import { droidTest } from '../droid-fixtures'
import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { waitForNativeGenerationReceipt } from '../helpers/nativeGenerationReceipt'
import { exerciseInterruptedPartialAnswer, exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { withTimingWorker } from '../helpers/timingFixture'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { DROID_AGENT, nativeContext } from './scenarios'

droidTest('stops a native model turn and resumes its paused queue', async ({ native }) => {
  await exerciseInterruptTurn(native)
})

droidTest('stops an actual native tool and accepts the next queued turn', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})

droidTest('withdraws a waiting question and accepts the next queued turn', async ({ native }) => {
  await exerciseControlInterrupt(native, { control: 'question' })
})

droidTest('retains interrupted native text after a verified buffer receipt and reload', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
  await withTimingWorker(leapmuxServer, { dataDirPrefix: 'droid-partial-text', env: { [GENERATION_TRACE_ENV]: '1' } }, async (worker) => {
    const workspaceId = authenticatedEmptyWorkspace.workspaceId
    await openProviderAgent(worker.server, workspaceId, DROID_AGENT)
    await openWorkspace(page, workspaceId)
    const native = await nativeContext({ page, modelScript, leapmuxServer: worker.server, workspaceId })
    await exerciseInterruptedPartialAnswer(native, {
      waitForPartialReceipt: text => waitForNativeGenerationReceipt(worker.logLines, text),
    })
  })
})
