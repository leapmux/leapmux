import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ANTIGRAVITY_E2E_SKIP_REASON, antigravityTest } from '../antigravity-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { createNativePermissionFileWrite } from '../helpers/nativePermission'
import { currentNativeAgent, nativeAgentById } from '../helpers/nativeScenario'
import { exerciseWorkerRestart, waitForWorkerConnection } from '../helpers/nativeWorkerRestart'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { expectNoControlBanner, resumePausedQueue, sendMessage, tabById, waitForControlBanner } from '../helpers/ui'
import { ensureWorkerOnline, restartWorker, stopWorker, waitForWorkerOffline } from '../process-control-fixtures'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { exerciseAntigravityResumeWithEvidence } from './resumeEvidence'
import { ANTIGRAVITY_AGENT, nativeContext } from './scenarios'

antigravityTest('reopens the native session and restores the saved transcript', async ({ native }, testInfo) => {
  await exerciseAntigravityResumeWithEvidence(native, testInfo)
})

const restartTest = createProviderProcessTest({ agent: ANTIGRAVITY_AGENT, nativeContext, skipReason: ANTIGRAVITY_E2E_SKIP_REASON })

for (const restart of ['worker', 'hub-and-worker'] as const) {
  restartTest(`retains native context and transcript after a ${restart} restart`, async ({ native, separateHubWorker }) => {
    await exerciseWorkerRestart(native, separateHubWorker, restart)
  })
}

restartTest('drops a stale permission after a worker restart and continues the session', async ({ native, separateHubWorker }) => {
  const agent = await currentNativeAgent(native)
  const operation = await createNativePermissionFileWrite(native, {
    fileName: 'antigravity-restart-pending.txt',
    callId: 'antigravity-restart-pending',
    outputPrefix: 'RESTART_PENDING',
  })
  await native.modelScript.queue({
    toolCalls: [operation.toolCall],
  })
  await sendMessage(native.page, native.modelScript.prompt('Hold the native permission before the restart.'))
  await waitForControlBanner(native.page)
  await operation.beforeDecision()
  await withCleanup(async () => {
    await stopWorker(separateHubWorker)
    await waitForWorkerOffline(separateHubWorker)
    await waitForWorkerConnection(native.page, false)
    await restartWorker(separateHubWorker)
    await waitForWorkerConnection(native.page, true)
    await retryUntilPass(async () => {
      expect((await nativeAgentById(native, agent.id))?.status).toBe(AgentStatus.ACTIVE)
    })
    await tabById(native.page, agent.id).click()
    await currentNativeAgent(native)
    await resumePausedQueue(native.page)
    await expectNoControlBanner(native.page)
    await sendNativeAnswer(native, 'Continue after the pending permission was canceled.', 'The resumed session accepted the next prompt.')
    expect(existsSync(join(agent.workingDir, 'antigravity-restart-pending.txt'))).toBe(false)
  }, async () => {
    await ensureWorkerOnline(separateHubWorker)
  })
})
