import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from './agentSettings'
import { DIRAC_E2E_SKIP_REASON } from './dirac-fixtures'
import { DROID_E2E_SKIP_REASON, DROID_TITLE_RULE } from './droid-fixtures'
import { FAST_AGENT_E2E_SKIP_REASON } from './fastagent-fixtures'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { diracRespondToolCall, junieAnswerToolCall } from './helpers/providerToolCalls'
import { listAgents } from './helpers/subagentRegistry'
import { assistantBubbles, loginViaToken, openMenu, openWorkspace, sendMessage, userBubbles, waitForAgentIdle } from './helpers/ui'
import { closeAgentViaAPI, createGitRepo, openNewAgentDialog, setWorkingDir, waitForWorker } from './helpers/worktree'
import { JUNIE_E2E_SKIP_REASON } from './junie-fixtures'
import { LETTA_E2E_SKIP_REASON, LETTA_TITLE_RULE } from './letta-fixtures'
import { QODER_E2E_SKIP_REASON } from './qoder-fixtures'

const SESSION_MENU = 'session-select-menu'

const PROVIDERS = [
  { provider: AgentProvider.JUNIE, label: 'Junie', skip: JUNIE_E2E_SKIP_REASON },
  { provider: AgentProvider.LETTA, label: 'Letta Code', skip: LETTA_E2E_SKIP_REASON },
  { provider: AgentProvider.DIRAC, label: 'Dirac', skip: DIRAC_E2E_SKIP_REASON },
  { provider: AgentProvider.QODER, label: 'Qoder CLI', skip: QODER_E2E_SKIP_REASON },
  { provider: AgentProvider.DROID, label: 'Factory Droid', skip: DROID_E2E_SKIP_REASON },
  { provider: AgentProvider.FAST_AGENT, label: 'Fast Agent', skip: FAST_AGENT_E2E_SKIP_REASON },
] as const

for (const { provider, label, skip } of PROVIDERS) {
  test.describe(`${label} session resume`, () => {
    test.skip(!!skip, skip ?? '')

    test(provider === AgentProvider.DIRAC
      ? 'reattaches an incomplete task without its prior model context'
      : 'continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
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

      if (provider === AgentProvider.JUNIE) {
        await modelScript.rule(
          { name: 'junie-capability-filter', when: { system: 'capability filter agent' }, respond: { text: '' } },
          { name: 'junie-task-name', when: { system: 'task description summarizer' }, respond: { text: 'Resume task' } },
        )
      }
      if (provider === AgentProvider.LETTA)
        await modelScript.rule(LETTA_TITLE_RULE)
      if (provider === AgentProvider.DROID)
        await modelScript.rule(DROID_TITLE_RULE)

      const firstAnswer = 'The first answer contains HALIBUT.'
      const diracIncomplete = provider === AgentProvider.DIRAC
      const diracGate = 'dirac-incomplete-resume'
      await modelScript.queue(provider === AgentProvider.JUNIE
        ? { toolCalls: [junieAnswerToolCall('junie-resume-first', firstAnswer)] }
        : diracIncomplete
          ? { toolCalls: [diracRespondToolCall('dirac-resume-progress', 'progress', firstAnswer)] }
          : { text: firstAnswer })
      if (diracIncomplete)
        await modelScript.queue({ gate: diracGate, text: 'The first task remains incomplete.' })
      let sessionId = ''
      let diracGateHeld = false
      try {
        await sendMessage(page, modelScript.prompt('Remember HALIBUT across a resumed session.'))
        if (diracIncomplete) {
          const held = await modelScript.waitForGate(diracGate)
          diracGateHeld = true
          const continued = held.requests.find(request => request.stepIndex === 1)
          expect(JSON.stringify(continued?.body).includes(firstAnswer)).toBe(true)
        }
        else {
          await modelScript.waitForSteps(1)
          await waitForAgentIdle(page, 180_000)
        }
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

      await modelScript.queue(provider === AgentProvider.JUNIE
        ? { toolCalls: [junieAnswerToolCall('junie-resume-second', 'I remember HALIBUT.')] }
        : provider === AgentProvider.DIRAC
          ? { toolCalls: [diracRespondToolCall('dirac-resume-second', 'complete', 'I remember HALIBUT.')] }
          : { text: 'I remember HALIBUT.' })
      await sendMessage(page, modelScript.prompt('What was the word from the prior turn?'))
      const status = await modelScript.waitForSteps(diracIncomplete ? 3 : 2)
      await waitForAgentIdle(page, 180_000)
      const resumed = status.requests.find(request => request.stepIndex === (diracIncomplete ? 2 : 1))
      const resumedBody = JSON.stringify(resumed?.body)
      if (diracIncomplete) {
        expect(resumedBody.includes(firstAnswer)).toBe(false)
        expect(resumedBody.includes('What was the word from the prior turn?')).toBe(true)
      }
      else {
        expect(resumedBody.includes(firstAnswer)).toBe(true)
      }
    })
  })
}
