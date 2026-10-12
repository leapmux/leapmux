import { expect } from '@playwright/test'
import { fastAgentTest } from '../fastagent-fixtures'
import { goalsAndTodosSection } from '../helpers/goalsAndTodos'
import { messageBubbles, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { FAST_AGENT_AGENT } from './scenarios'

fastAgentTest.describe('Fast Agent to-do support', () => {
  fastAgentTest('offers no native to-do command or tool in the launched coding agent', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, FAST_AGENT_AGENT)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    // Fast Agent's /commands route reads the same available-command catalogue
    // that its ACP session publishes. An unknown /todo route must refuse.
    await sendMessage(page, '/commands --json')
    await waitForAgentIdle(page)
    const commands = messageBubbles(page).filter({ hasText: 'command_index' }).first()
    await expect(commands).toBeVisible()
    expect(await commands.textContent()).not.toMatch(/"name"\s*:\s*"(?:todo|todowrite|update_plan|plan_update|task_list)"/i)
    await sendMessage(page, '/todo')
    await waitForAgentIdle(page)
    await expect(messageBubbles(page).filter({ hasText: 'Unknown command: /todo' }).first()).toBeVisible()

    const start = await modelScript.queue({ text: 'The coding turn answered without a plan update.' })
    await sendMessage(page, modelScript.prompt('Create a native to-do list if a tool supports it.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const tools = JSON.stringify((await modelScript.requestAt(start)).body)
    expect(tools).toContain('"tools"')
    expect(tools).not.toMatch(/"name"\s*:\s*"(?:todo|todowrite|update_plan|plan_update|task_list)"/i)
    await expect(goalsAndTodosSection(page)).toHaveCount(0)
  })
})
