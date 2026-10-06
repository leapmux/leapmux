import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, chatText, sendMessage, waitForAgentIdle } from '../helpers/ui'

import { ohMyPiTest } from '../ohmypi-fixtures'

/**
 * The installed agent executes the scripted shell command. Calculated output proves that the executor ran the command.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 */
ohMyPiTest.describe('Oh My Pi tool execution', () => {
  ohMyPiTest('draws the output of a command', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
    void authenticatedOhMyPiWorkspace
    // The command text states no `omp-42`, so only the command's own output
    // can put it on the page.
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.OH_MY_PI, 'echo-call', 'echo "omp-$((40 + 2))"')] },
      { text: 'The command printed its number.' },
    )
    await sendMessage(page, modelScript.prompt('Run the arithmetic command.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expect.poll(() => chatText(page)).toContain('omp-42')
    // omp appends a `Wall time: <n> seconds` notice to every output. The
    // extractor drops it, because the row already states how the call ended.
    expect(await chatText(page)).not.toContain('Wall time:')
  })
})

ohMyPiTest('preserves a literal private shell path with spaces and metacharacters', async ({ authenticatedOhMyPiWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOhMyPiWorkspace.workspaceId, provider: AgentProvider.OH_MY_PI }
  await exerciseShellToolExecution(context, { includeFailure: false, prepare: () => applyPermissionPreset(page, 'bypass') })
})
