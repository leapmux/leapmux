import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { piTest } from '../pi-fixtures'

piTest('bash command execution renders output in chat', async ({ authenticatedPiWorkspace, page, modelScript }) => {
  void authenticatedPiWorkspace // fixture trigger
  // The command text, the prompt and the reply state no `pi-42`, so only the
  // command's own output can put it in a tool row.
  await modelScript.queue(
    { toolCalls: [bashToolCall(AgentProvider.PI, 'echo-call', 'echo "pi-$((40 + 2))"')] },
    { text: 'The command printed its number.' },
  )
  await sendMessage(page, modelScript.prompt('Run the arithmetic command and show me the output.'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)

  await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'pi-42' }).first()).toBeVisible()
})

piTest('keeps actual native shell output and a failed command result', async ({ authenticatedPiWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedPiWorkspace.workspaceId, provider: AgentProvider.PI }
  await exerciseShellToolExecution(context)
})
