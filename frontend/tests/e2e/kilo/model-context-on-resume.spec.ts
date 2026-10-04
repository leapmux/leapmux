import { expect } from '@playwright/test'
import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { COPILOT_PERMISSION_MODE } from '../../../src/generated/contracts/copilot-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { test } from '../fixtures'
import { createWorkspaceViaAPI, openAgentViaAPI } from '../helpers/api'
import { listAgents } from '../helpers/subagentRegistry'
import { assistantBubbles, loginViaToken, openMenu, openWorkspace, sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { closeAgentViaAPI, createGitRepo, openNewAgentDialog, setWorkingDir, waitForWorker } from '../helpers/worktree'
import { KILO_E2E_SKIP_REASON } from '../kilo-fixtures'

function cursorConversationId(body: unknown): string | undefined {
  if (body === null || typeof body !== 'object' || !('conversationId' in body))
    return undefined
  return typeof body.conversationId === 'string' ? body.conversationId : undefined
}

for (const { provider, label, skip } of [{ provider: AgentProvider.KILO, label: 'Kilo', skip: KILO_E2E_SKIP_REASON }]) {
  test.skip(!!skip, skip || '')
  test('restores old Worker rows and native model context after reopening', async ({ page, leapmuxServer, modelScript }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const keeperDir = createGitRepo(dataDir, `resume-keeper-a-${crypto.randomUUID()}`)
    const subjectDir = createGitRepo(dataDir, `resume-subject-a-${crypto.randomUUID()}`)
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, `${label} resume ${crypto.randomUUID()}`)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, keeperDir, { title: 'Keeper' })
    const initialSettings = agentOpenOptions(agentSettings(provider))
    const subjectSettings = provider === AgentProvider.GITHUB_COPILOT
      ? {
          ...initialSettings,
          optionValues: { ...initialSettings.optionValues, [OPTION_ID_PERMISSION_MODE]: COPILOT_PERMISSION_MODE.Manual },
        }
      : initialSettings
    const subjectId = await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, subjectDir, {
      agentProvider: provider,
      ...subjectSettings,
      title: 'Subject',
    })
    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)
    await page.locator('[data-testid="tab"][data-tab-type="agent"]').filter({ hasText: 'Subject' }).first().click()

    const firstPrompt = 'Remember HALIBUT across a resumed session.'
    const firstAnswer = 'The first answer contains HALIBUT.'
    await modelScript.queue({ text: firstAnswer })
    await sendMessage(page, modelScript.prompt(firstPrompt))
    const firstStatus = await modelScript.waitForSteps(1)
    const firstConversationId = provider === AgentProvider.CURSOR
      ? cursorConversationId(firstStatus.requests.find(request => request.stepIndex === 0)?.body)
      : undefined
    if (provider === AgentProvider.CURSOR)
      expect(firstConversationId).toBeTruthy()
    await waitForAgentIdle(page)
    await expect(userBubbles(page).filter({ hasText: firstPrompt })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: firstAnswer })).toHaveCount(1)

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
    await expect(dialog.getByTestId('session-select-menu-trigger')).toBeEnabled()
    await openMenu(dialog, 'session-select-menu')
    const stored = dialog.getByTestId('session-select-menu').getByTestId(`loading-menu-option-${sessionId}`)
    await expect(stored).toBeVisible()
    await stored.click()
    await dialog.getByRole('button', { name: 'Create' }).click()

    await expect(userBubbles(page).filter({ hasText: firstPrompt })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: firstAnswer })).toHaveCount(1)
    await modelScript.queue({ text: 'I remember HALIBUT.' })
    await sendMessage(page, modelScript.prompt('What was the word from the prior turn?'))
    const status = await modelScript.waitForSteps(2)
    await waitForAgentIdle(page)
    const resumed = status.requests.find(request => request.stepIndex === 1)
    if (provider === AgentProvider.CURSOR)
      expect(cursorConversationId(resumed?.body)).toBe(firstConversationId)
    else
      expect(JSON.stringify(resumed?.body)).toContain(firstAnswer)
    await expect(assistantBubbles(page).filter({ hasText: 'I remember HALIBUT.' })).toHaveCount(1)
  })
}
