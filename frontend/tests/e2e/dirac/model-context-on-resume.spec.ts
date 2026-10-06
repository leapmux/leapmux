import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { diracTest } from '../dirac-fixtures'
import { createWorkspaceViaAPI, openAgentViaAPI } from '../helpers/api'
import { stepRequest } from '../helpers/mockModelScript'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { reopenFromSessionPicker } from '../helpers/nativeResume'
import { nativeAgentById, nativeModelContextText } from '../helpers/nativeScenario'
import { diracRespondToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { agentTabs, assistantBubbles, loginViaToken, openWorkspace, sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { closeAgentViaAPI } from '../helpers/workerTabs'
import { createGitRepo } from '../helpers/worktree'

diracTest.describe('Dirac session resume', () => {
  const provider: AgentProvider = AgentProvider.DIRAC
  const label = 'Dirac'
  diracTest('reattaches an incomplete task without its prior model context', async ({ page, leapmuxServer, modelScript }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const keeperDir = createGitRepo(dataDir, `resume-keeper-c-${crypto.randomUUID()}`)
    const subjectDir = createGitRepo(dataDir, `resume-subject-c-${crypto.randomUUID()}`)
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, `${label} resume ${crypto.randomUUID()}`)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, keeperDir, { title: 'Keeper' })
    const subjectId = await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, subjectDir, {
      ...agentOpenOptions(provider),
      title: 'Subject',
    })
    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)
    await agentTabs(page).filter({ hasText: 'Subject' }).first().click()
    const firstAnswer = 'The first answer contains HALIBUT.'
    const diracGate = 'dirac-incomplete-resume'
    const progressStep = await modelScript.queue(
      { toolCalls: [diracRespondToolCall('dirac-resume-progress', 'progress', firstAnswer)] },
      { gate: diracGate, text: 'The first task remains incomplete.' },
    )
    let sessionId = ''
    let diracGateHeld = false
    try {
      await sendMessage(page, modelScript.prompt('Remember HALIBUT across a resumed session.'))
      const held = await modelScript.waitForGate(diracGate)
      diracGateHeld = true
      expect(JSON.stringify(stepRequest(held, progressStep + 1).body)).toContain(firstAnswer)
      sessionId = await retryUntilPass(async () => {
        const stored = (await nativeAgentById({ leapmuxServer: { hubUrl, adminToken, workerId } }, subjectId))?.agentSessionId ?? ''
        expect(stored, 'the Worker stores the native session of the held turn').not.toBe('')
        return stored
      })
      await closeAgentViaAPI(hubUrl, adminToken, workerId, subjectId)
    }
    finally {
      if (diracGateHeld && (await modelScript.status()).pendingGates.includes(diracGate))
        await modelScript.releaseGate(diracGate)
    }
    await reopenFromSessionPicker(page, { provider, workingDir: subjectDir, sessionId })
    await expect(userBubbles(page).filter({ hasText: 'Remember HALIBUT across a resumed session.' })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: firstAnswer })).toHaveCount(1)
    const resumedStep = await modelScript.queue({ toolCalls: [diracRespondToolCall('dirac-resume-second', 'complete', 'I remember HALIBUT.')] })
    await sendMessage(page, modelScript.prompt('What was the word from the prior turn?'))
    await modelScript.waitForSteps(resumedStep + 1)
    await waitForAgentIdle(page)
    const resumedBody = JSON.stringify((await modelScript.requestAt(resumedStep)).body)
    expect(resumedBody).not.toContain(firstAnswer)
    expect(resumedBody).toContain('What was the word from the prior turn?')
  })
})

diracTest('reopens a completed native task with saved Worker rows and no prior model context', async ({ native, page, modelScript }) => {
  // The scenario queues its first step at the next index of the script.
  const start = (await modelScript.status()).stepCount
  const resumed = await exerciseSessionResume(native)
  const initial = await modelScript.requestAt(start)
  const prompt = nativeModelContextText(initial).match(/\bRESUMEPROMPT[a-f0-9]{32}\b/)?.[0]
  const savedAnswers = await assistantBubbles(page).filter({ hasText: 'RESUMEANSWER' }).allTextContents()
  const answer = savedAnswers.join('\n').match(/\bRESUMEANSWER[a-f0-9]{32}\b/)?.[0]
  if (!prompt || !answer)
    throw new Error('The native task or saved Worker rows omitted their unique resume markers.')
  // The markers belong to this scenario. They are not text from another task.
  expect(prompt).toBe(`RESUMEPROMPT${resumed.marker}`)
  expect(answer).toBe(resumed.originalAnswer)
  const nextContext = nativeModelContextText(resumed.request)
  expect(nextContext).not.toContain(prompt)
  expect(nextContext).not.toContain(answer)
})
