import { expect } from '@playwright/test'
import { diracTest } from '../dirac-fixtures'
import { expandGoalsAndTodosSection, goalsAndTodosList } from '../helpers/goalsAndTodos'
import { diracRespondToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, sendMessage, waitForAgentIdle } from '../helpers/ui'

diracTest('shows completed native plan entries in the sidebar and keeps their status after reload', async ({ native }) => {
  const { page, modelScript } = native
  await chooseSettingsOption(page, 'permissionMode-plan')
  const start = await modelScript.queue(
    { toolCalls: [diracRespondToolCall('dirac-native-todo-plan', 'plan', '- DIRACPLANONE inspect the file.\n- DIRACPLANTWO report the result.')] },
    { toolCalls: [diracRespondToolCall('dirac-native-todo-complete', 'complete', 'The native plan turn completed.')] },
  )
  await sendMessage(page, modelScript.prompt('Return the scripted proposal and complete after its native acceptance.'))
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  await expandGoalsAndTodosSection(page)
  const list = goalsAndTodosList(page)
  await expect(list).toContainText('DIRACPLANONE')
  await expect(list).toContainText('DIRACPLANTWO')
  await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(2)
  await page.reload()
  await expandGoalsAndTodosSection(page)
  await expect(list.locator('[data-task-checkbox="completed"]')).toHaveCount(2)
})
