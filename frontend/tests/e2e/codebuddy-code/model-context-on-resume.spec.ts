import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { CODEBUDDY_E2E_SKIP_REASON } from '../codebuddy-fixtures'
import { expect, test } from '../fixtures'
import { createWorkspaceViaAPI, openAgentViaAPI } from '../helpers/api'
import { listAgents } from '../helpers/subagentRegistry'
import { ARITHMETIC_ANSWER_TEXT, assistantBubbles, loginViaToken, openMenu, openWorkspace, sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { closeAgentViaAPI, createGitRepo, openNewAgentDialog, setWorkingDir, waitForWorker } from '../helpers/worktree'

test.describe('CodeBuddy Code session resume', () => {
  const SESSION_MENU = 'session-select-menu'

  const provider: AgentProvider = AgentProvider.CODEBUDDY

  const label = 'CodeBuddy Code'

  const skip = CODEBUDDY_E2E_SKIP_REASON

  test.skip(!!skip, skip ?? '')

  test('continues a closed session chosen from the native picker', async ({ page, leapmuxServer, modelScript }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const keeperDir = createGitRepo(dataDir, `resume-keeper-${crypto.randomUUID()}`)
    const subjectDir = createGitRepo(dataDir, `resume-subject-${crypto.randomUUID()}`)
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

    await modelScript.queue({ text: 'The first answer contains HALIBUT.' })
    await sendMessage(page, modelScript.prompt('Remember HALIBUT across a resumed session.'))
    await modelScript.waitForSteps(1)
    await waitForAgentIdle(page)
    let sessionId = ''
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
    const options = dialog.getByTestId(SESSION_MENU).getByRole('menuitemradio')
    await expect(options).toHaveCount(3)
    const session = options.nth(2)
    await expect(session).toHaveAttribute('data-testid', `loading-menu-option-${sessionId}`)
    await session.click()
    await dialog.getByRole('button', { name: 'Create' }).click()

    await expect(userBubbles(page).filter({ hasText: 'Remember HALIBUT across a resumed session.' })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: 'The first answer contains HALIBUT.' })).toHaveCount(1)

    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt('What was the word from the prior turn?'))
    const status = await modelScript.waitForSteps(2)
    await waitForAgentIdle(page)
    const resumed = status.requests.find(request => request.stepIndex === 1)
    expect(JSON.stringify(resumed?.body).includes('The first answer contains HALIBUT.')).toBe(true)
  })
})
