import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest, expect } from '../codebuddy-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

codebuddyTest.describe('CodeBuddy Code tool execution', () => {
  codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

  codebuddyTest('runs a Bash tool and draws its span', async ({ codebuddyWorkspace, page, modelScript }) => {
    void codebuddyWorkspace
    const call = bashToolCall(AgentProvider.CODEBUDDY, 'call-1', 'echo hi')
    await modelScript.queue({
      toolCalls: [call],
    })
    await modelScript.queue({ text: 'The command ran.' })
    await sendMessage(page, modelScript.prompt('Run echo hi.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expect(assistantBubbles(page).filter({ hasText: 'The command ran.' }).first()).toBeVisible()
    await expect(page.getByTestId('thinking-indicator')).not.toBeVisible()
  })
})

codebuddyTest('runs successful and failed native commands with their actual output', async ({ codebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: codebuddyWorkspace.workspaceId })
  await exerciseShellToolExecution(context)
})
