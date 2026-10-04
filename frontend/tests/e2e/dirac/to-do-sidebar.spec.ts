import { expect } from '@playwright/test'
import { diracTest } from '../dirac-fixtures'
import { diracRespondToolCall } from '../helpers/providerToolCalls'
import { expandGoalsAndTodosSection } from '../helpers/subagentRegistry'
import { chooseSettingsOption, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

diracTest('shows completed native plan entries in the sidebar and keeps their status after reload', async ({ authenticatedDiracWorkspace, page, modelScript, leapmuxServer }) => {
  await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDiracWorkspace.workspaceId })
  await chooseSettingsOption(page, 'permissionMode-plan')
  const start = (await modelScript.status()).stepCount
  await modelScript.queue(
    { toolCalls: [diracRespondToolCall('dirac-native-todo-plan', 'plan', '- DIRACPLANONE inspect the file.\n- DIRACPLANTWO report the result.')] },
    { toolCalls: [diracRespondToolCall('dirac-native-todo-complete', 'complete', 'The native plan turn completed.')] },
  )
  await sendMessage(page, modelScript.prompt('Return the scripted proposal and complete after its native acceptance.'))
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  await expandGoalsAndTodosSection(page)
  const list = page.locator('[data-testid="goals-and-todos"]:visible').first()
  await expect(list).toContainText('DIRACPLANONE')
  await expect(list).toContainText('DIRACPLANTWO')
  await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(2)
  await page.reload()
  await expandGoalsAndTodosSection(page)
  await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(2)
})
