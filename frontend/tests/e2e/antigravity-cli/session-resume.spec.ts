import { existsSync, mkdtempSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { antigravityTest } from '../antigravity-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { createNativePermissionFileWrite, exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { currentNativeAgent, nativeAgentById } from '../helpers/nativeScenario'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { exerciseWorkerRestart, waitForWorkerConnection } from '../helpers/nativeWorkerRestart'
import { readToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { getGlobalState } from '../helpers/server'
import { uniqueMarker } from '../helpers/shellArguments'
import { chooseSettingsOption, expectNoControlBanner, resumePausedQueue, sendMessage, tabById, waitForControlBanner, waitForSettingsIdle } from '../helpers/ui'
import { ensureWorkerOnline, restartWorker, stopWorker, waitForWorkerOffline } from '../process-control-fixtures'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { exerciseAntigravityResumeWithEvidence } from './resumeEvidence'
import { ANTIGRAVITY_AGENT, nativeContext } from './scenarios'
import { antigravityNativeToolResult } from './toolResult'

antigravityTest('reopens the native session and restores the saved transcript', async ({ native }, testInfo) => {
  await exerciseAntigravityResumeWithEvidence(native, testInfo)
})

for (const session of ['fresh', 'reopened'] as const) {
  for (const mode of ['default', 'bypass'] as const) {
    antigravityTest(`reads an external skill in ${mode} mode in a ${session} session`, async ({ native }, testInfo) => {
      if (session === 'reopened')
        await exerciseAntigravityResumeWithEvidence(native, testInfo)
      const skillDir = mkdtempSync(join(getGlobalState().tmpDir, 'antigravity-external-skill-'))
      const path = join(skillDir, 'SKILL.md')
      const content = `Antigravity external skill ${uniqueMarker()}.`
      writeFileSync(path, `${content}\n`)
      await chooseSettingsOption(native.page, `permissionMode-${mode}`)
      await waitForSettingsIdle(native.page)
      const callId = 'antigravity-external-skill'
      const toolCall = readToolCall(native.provider, callId, path)

      if (mode === 'default') {
        await exerciseNativePermissionDecision(native, {
          toolCall,
          decision: 'allow',
          beforeDecision: async (banner) => {
            await expect(banner).toContainText(path)
          },
          nativeProof: (request) => {
            expect(antigravityNativeToolResult(request, callId)).toContain(content)
          },
        })
      }
      else {
        await expectNoNativeControl(native, {
          testId: 'control-banner',
          relatedProof: async () => {
            const { resultRequest } = await runNativeToolTurn(native, {
              prompt: 'Read the scripted external skill in bypass mode.',
              toolCalls: [toolCall],
              answer: 'The external skill read finished.',
              permissions: 'none',
            })
            expect(antigravityNativeToolResult(resultRequest, callId)).toContain(content)
          },
        })
      }
    })
  }
}

const restartTest = createProviderProcessTest({ agent: ANTIGRAVITY_AGENT, nativeContext })

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
