import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { DIRAC_E2E_SKIP_REASON, diracTest } from '../dirac-fixtures'
import { expect, test } from '../fixtures'
import { createWorkspaceViaAPI, openAgentViaAPI } from '../helpers/api'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { nativeModelContextText } from '../helpers/nativeScenario'
import { diracRespondToolCall } from '../helpers/providerToolCalls'
import { listAgents } from '../helpers/subagentRegistry'
import { assistantBubbles, loginViaToken, openMenu, openWorkspace, sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { closeAgentViaAPI, createGitRepo, openNewAgentDialog, setWorkingDir, waitForWorker } from '../helpers/worktree'
import { nativeContext } from './scenarios'

test.describe('Dirac session resume', () => {
  const SESSION_MENU = 'session-select-menu'
  const provider: AgentProvider = AgentProvider.DIRAC
  const label = 'Dirac'
  const skip = DIRAC_E2E_SKIP_REASON
  test.skip(!!skip, skip ?? '')
  test('reattaches an incomplete task without its prior model context', async ({ page, leapmuxServer, modelScript }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const keeperDir = createGitRepo(dataDir, `resume-keeper-c-${crypto.randomUUID()}`)
    const subjectDir = createGitRepo(dataDir, `resume-subject-c-${crypto.randomUUID()}`)
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, `${label} resume ${crypto.randomUUID()}`)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, keeperDir, { title: 'Keeper' })
    const subjectId = await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, subjectDir, {
      agentProvider: provider,
      ...agentOpenOptions(agentSettings(provider)),
      title: 'Subject',
    })
    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)
    await page.locator('[data-testid="tab"][data-tab-type="agent"]').filter({ hasText: 'Subject' }).first().click()
    const firstAnswer = 'The first answer contains HALIBUT.'
    const diracGate = 'dirac-incomplete-resume'
    await modelScript.queue({ toolCalls: [diracRespondToolCall('dirac-resume-progress', 'progress', firstAnswer)] })
    await modelScript.queue({ gate: diracGate, text: 'The first task remains incomplete.' })
    let sessionId = ''
    let diracGateHeld = false
    try {
      await sendMessage(page, modelScript.prompt('Remember HALIBUT across a resumed session.'))
      const held = await modelScript.waitForGate(diracGate)
      diracGateHeld = true
      const continued = held.requests.find(request => request.stepIndex === 1)
      expect(JSON.stringify(continued?.body).includes(firstAnswer)).toBe(true)
      await expect.poll(async () => {
        const agents = await listAgents(hubUrl, adminToken, workerId, [subjectId])
        sessionId = agents?.find(agent => agent.id === subjectId)?.agentSessionId ?? ''
        return sessionId
      }).not.toBe('')
      await closeAgentViaAPI(hubUrl, adminToken, workerId, subjectId)
    }
    finally {
      if (diracGateHeld && (await modelScript.status()).pendingGates.includes(diracGate))
        await modelScript.releaseGate(diracGate)
    }
    await openNewAgentDialog(page)
    await waitForWorker(page)
    const dialog = page.getByRole('dialog')
    await dialog.getByTestId('agent-provider-selector-trigger').click()
    await page.getByTestId(`agent-provider-option-${provider}`).click()
    await setWorkingDir(page, subjectDir)
    await expect(dialog.getByTestId(`${SESSION_MENU}-trigger`)).toBeEnabled()
    await openMenu(dialog, SESSION_MENU)
    const session = dialog.getByTestId(SESSION_MENU).getByTestId(`loading-menu-option-${sessionId}`)
    await expect(session).toBeVisible()
    await session.click()
    await dialog.getByRole('button', { name: 'Create' }).click()
    await expect(userBubbles(page).filter({ hasText: 'Remember HALIBUT across a resumed session.' })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: firstAnswer })).toHaveCount(1)
    await modelScript.queue({ toolCalls: [diracRespondToolCall('dirac-resume-second', 'complete', 'I remember HALIBUT.')] })
    await sendMessage(page, modelScript.prompt('What was the word from the prior turn?'))
    const status = await modelScript.waitForSteps(3)
    await waitForAgentIdle(page, 180000)
    const resumed = status.requests.find(request => request.stepIndex === (2))
    const resumedBody = JSON.stringify(resumed?.body)
    expect(resumedBody.includes(firstAnswer)).toBe(false)
    expect(resumedBody.includes('What was the word from the prior turn?')).toBe(true)
  })
})

diracTest('reopens a completed native task with saved Worker rows and no prior model context', async ({ authenticatedDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDiracWorkspace.workspaceId })
  const start = (await modelScript.status()).stepCount
  const resumed = await exerciseSessionResume(context)
  const status = await modelScript.status()
  const initial = status.requests.find(record => record.stepIndex === start)
  if (!initial)
    throw new Error('The completed native task reached no first model request.')
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
