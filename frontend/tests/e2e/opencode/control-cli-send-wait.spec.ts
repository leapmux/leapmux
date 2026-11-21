import { existsSync } from 'node:fs'
import { expect } from '@playwright/test'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, userBubbles, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { opencodeTest } from '../opencode-fixtures'
import { outsideRemovalCommand, projectBesideProbe, withOutsideProjectAgent } from './outsideProject'

function sendAndWait(agentId: string, prompt: string, requestId: string): string[] {
  return [
    'agent',
    'send',
    agentId,
    prompt,
    '--request-id',
    requestId,
    '--wait',
    '--timeout',
    '20s',
  ]
}

opencodeTest('waits for the input that owns a permission request and replays its exact reply', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
  const { workingDir, file } = projectBesideProbe('opencode-cli-reply-', 'remove this test file')
  await withOutsideProjectAgent({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedEmptyWorkspace.workspaceId,
  }, workingDir, async (native) => {
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const { id: agentId } = await currentNativeAgent(native)
    const earlierAnswer = 'Two checks passed before approval.'
    const earlierStep = await modelScript.queue({ text: earlierAnswer })
    const earlier = sendAndWait(agentId, modelScript.prompt('Summarize the checks before approval.'), 'opencode-earlier-reply')
    const first = await runCLI(cli, earlier) as { input_id: string }
    expect(first).toMatchObject({
      turn_outcome: 'completed',
      replies: [earlierAnswer],
    })
    await modelScript.waitForSteps(earlierStep + 1)
    await waitForAgentIdle(page)

    const question = 'Run the scripted check outside the project directory.'
    const answer = 'The approved check completed.'
    const step = await modelScript.queue({
      toolCalls: [
        bashToolCall(native.provider, 'opencode-cli-permission', outsideRemovalCommand(file, 'OPENCODE_CLI_PERMISSION_')),
      ],
    }, { text: answer })
    const send = sendAndWait(agentId, modelScript.prompt(question), 'opencode-permission-reply')
    const blocked = await runCLI(cli, send) as { input_id: string }
    expect(blocked).toMatchObject({
      message: 'delivered',
      state: 'waiting_for_user',
    })
    await waitForControlBanner(page)
    expect(existsSync(file)).toBe(true)
    expect(await runCLI(cli, earlier)).toMatchObject({
      input_id: first.input_id,
      turn_outcome: 'completed',
      replies: [earlierAnswer],
    })

    const { requests } = await runCLI(cli, ['agent', 'requests', agentId]) as { requests: Array<{ id: string }> }
    expect(requests).toHaveLength(1)
    const [permission] = requests
    if (!permission)
      throw new Error('The Worker returned no OpenCode permission request.')
    await runCLI(cli, [
      'agent',
      'approve',
      agentId,
      permission.id,
    ])
    await modelScript.waitForSteps(step + 2)
    expect(await runCLI(cli, send)).toMatchObject({
      input_id: blocked.input_id,
      turn_outcome: 'completed',
      replies: [answer],
    })
    expect(existsSync(file)).toBe(false)
    await expect(userBubbles(page).filter({ hasText: question })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: answer })).toHaveCount(1)
  })
})
