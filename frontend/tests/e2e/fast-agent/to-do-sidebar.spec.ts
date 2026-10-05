import { fastAgentTest, expect as fastExpect, openFastAgentAgent } from '../fastagent-fixtures'
import { goalsAndTodosSection } from '../helpers/subagentRegistry'
import { messageBubbles, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'

fastAgentTest.describe('Fast Agent to-do support', () => {
  fastAgentTest('offers no native to-do command or tool in the launched coding agent', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openFastAgentAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    // Fast Agent's /commands route reads the same available-command catalogue
    // that its ACP session publishes. An unknown /todo route must refuse.
    await sendMessage(page, '/commands --json')
    await waitForAgentIdle(page)
    const commands = messageBubbles(page).filter({ hasText: 'command_index' }).first()
    await fastExpect(commands).toBeVisible()
    fastExpect(await commands.textContent()).not.toMatch(/"name"\s*:\s*"(?:todo|todowrite|update_plan|plan_update|task_list)"/i)
    await sendMessage(page, '/todo')
    await waitForAgentIdle(page)
    await fastExpect(messageBubbles(page).filter({ hasText: 'Unknown command: /todo' }).first()).toBeVisible()

    await modelScript.queue({ text: 'The coding turn answered without a plan update.' })
    await sendMessage(page, modelScript.prompt('Create a native to-do list if a tool supports it.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const request = status.requests.find(record => record.stepIndex === 0)
    fastExpect(request).toBeDefined()
    const tools = JSON.stringify(request?.body)
    fastExpect(tools).toContain('"tools"')
    fastExpect(tools).not.toMatch(/"name"\s*:\s*"(?:todo|todowrite|update_plan|plan_update|task_list)"/i)
    await fastExpect(goalsAndTodosSection(page)).toHaveCount(0)
  })
})
