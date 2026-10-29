import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { diracTest } from '../dirac-fixtures'
import { stepRequest } from '../helpers/mockModelScript'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { expectReopenedNativeAgent, nativeResumeTexts, reopenFromSessionPicker } from '../helpers/nativeResume'
import { openResumeSubject } from '../helpers/nativeResumePicker'
import { nativeAgentById, nativeModelContextText } from '../helpers/nativeScenario'
import { diracRespondToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { assistantBubbles, sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { closeNativeAgentAndWait } from '../helpers/workerTabs'
import { waitForDiracCheckpoint } from './resumeCheckpoint'

diracTest.describe('native session resume', () => {
  const provider: AgentProvider = AgentProvider.DIRAC
  const label = 'Dirac'
  diracTest('reattaches an incomplete task with its stored model context', async ({ page, leapmuxServer, modelScript }) => {
    const { subjectId, subjectDir, keeperId } = await openResumeSubject({ page, modelScript, leapmuxServer }, {
      label,
      subjectOptions: () => agentOpenOptions(provider),
    })
    const texts = nativeResumeTexts()
    const firstAnswer = texts.originalAnswer
    const diracGate = 'dirac-incomplete-resume'
    const progressStep = await modelScript.queue(
      {
        toolCalls: [
          diracRespondToolCall('dirac-resume-progress', 'progress', firstAnswer),
        ],
      },
      { gate: diracGate, text: 'The first task remains incomplete.' },
    )
    let sessionId = ''
    let diracGateHeld = false
    try {
      await sendMessage(page, modelScript.prompt(texts.originalPrompt))
      const held = await modelScript.waitForGate(diracGate)
      diracGateHeld = true
      expect(JSON.stringify(stepRequest(held, progressStep + 1).body)).toContain(firstAnswer)
      sessionId = await retryUntilPass(async () => {
        const stored = (await nativeAgentById({ leapmuxServer }, subjectId))?.agentSessionId ?? ''
        expect(stored, 'the Worker stores the native session of the held turn').not.toBe('')
        return stored
      })
      await waitForDiracCheckpoint(leapmuxServer.agentEnv?.DIRAC_DIR ?? '', sessionId)
      await closeNativeAgentAndWait({ leapmuxServer }, subjectId)
    }
    finally {
      if (diracGateHeld && (await modelScript.status()).pendingGates.includes(diracGate))
        await modelScript.releaseGate(diracGate)
    }
    await reopenFromSessionPicker(page, { provider, workingDir: subjectDir, sessionId })
    await expectReopenedNativeAgent({ page, leapmuxServer }, { agentProvider: provider, agentSessionId: sessionId }, [subjectId, keeperId])
    await expect(userBubbles(page).filter({ hasText: texts.originalPrompt })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: firstAnswer })).toHaveCount(1)
    const resumedStep = await modelScript.queue({
      toolCalls: [
        diracRespondToolCall('dirac-resume-second', 'complete', texts.resumedAnswer),
      ],
    })
    await sendMessage(page, modelScript.prompt(texts.resumedPrompt))
    await modelScript.waitForSteps(resumedStep + 1)
    await waitForAgentIdle(page)
    const resumedBody = JSON.stringify((await modelScript.requestAt(resumedStep)).body)
    expect(resumedBody).toContain(firstAnswer)
    expect(resumedBody).toContain(texts.originalPrompt)
    expect(resumedBody).toContain(texts.resumedPrompt)
    await expect(assistantBubbles(page).filter({ hasText: texts.resumedAnswer }).first()).toBeVisible()
  })
})

diracTest('reopens a completed native task with saved Worker rows and prior model context', async ({ native, page }) => {
  const resumed = await exerciseSessionResume(native)
  const prompt = nativeModelContextText(resumed.originalRequest).match(/\bRESUMEPROMPT[a-f0-9]{32}\b/)?.[0]
  const savedAnswers = await assistantBubbles(page).filter({ hasText: 'RESUMEANSWER' }).allTextContents()
  const answer = savedAnswers.join('\n').match(/\bRESUMEANSWER[a-f0-9]{32}\b/)?.[0]
  if (!prompt || !answer)
    throw new Error('The native task or saved Worker rows omitted their unique resume markers.')
  // The markers belong to this scenario. They are not text from another task.
  expect(prompt).toBe(`RESUMEPROMPT${resumed.marker}`)
  expect(answer).toBe(resumed.originalAnswer)
  const nextContext = nativeModelContextText(resumed.request)
  expect(nextContext).toContain(prompt)
  expect(nextContext).toContain(answer)
})
