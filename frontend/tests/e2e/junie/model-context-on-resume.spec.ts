import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { expect, test } from '../fixtures'
import { createWorkspaceViaAPI, openAgentViaAPI } from '../helpers/api'
import { junieAnswerToolCall } from '../helpers/providerToolCalls'
import { listAgents } from '../helpers/subagentRegistry'
import { assistantBubbles, loginViaToken, openMenu, openWorkspace, sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { closeAgentViaAPI, createGitRepo, openNewAgentDialog, setWorkingDir, waitForWorker } from '../helpers/worktree'
import { JUNIE_E2E_SKIP_REASON } from '../junie-fixtures'

test.describe('Junie session resume', () => {
  const SESSION_MENU = 'session-select-menu'
  const provider: AgentProvider = AgentProvider.JUNIE
  const label = 'Junie'
  const skip = JUNIE_E2E_SKIP_REASON
  test.skip(!!skip, skip ?? '')
  test('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
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
    await modelScript.rule({ name: 'junie-capability-filter', when: { system: 'capability filter agent' }, respond: { text: '' } }, { name: 'junie-task-name', when: { system: 'task description summarizer' }, respond: { text: 'Resume task' } })
    const firstAnswer = 'The first answer contains HALIBUT.'
    await modelScript.queue({ toolCalls: [junieAnswerToolCall('junie-resume-first', firstAnswer)] })
    let sessionId = ''
    await sendMessage(page, modelScript.prompt('Remember HALIBUT across a resumed session.'))
    await modelScript.waitForSteps(1)
    await waitForAgentIdle(page, 180000)
    await expect.poll(async () => {
      const agents = await listAgents(hubUrl, adminToken, workerId, [subjectId])
      sessionId = agents?.find(agent => agent.id === subjectId)?.agentSessionId ?? ''
      return sessionId
    }).not.toBe('')
    await closeAgentViaAPI(hubUrl, adminToken, workerId, subjectId)
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
    await modelScript.queue({ toolCalls: [junieAnswerToolCall('junie-resume-second', 'I remember HALIBUT.')] })
    await sendMessage(page, modelScript.prompt('What was the word from the prior turn?'))
    const status = await modelScript.waitForSteps(2)
    await waitForAgentIdle(page, 180000)
    const resumed = status.requests.find(request => request.stepIndex === (1))
    const resumedBody = JSON.stringify(resumed?.body)
    expect(resumedBody.includes(firstAnswer)).toBe(true)
  })
})
