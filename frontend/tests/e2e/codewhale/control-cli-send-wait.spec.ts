import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { codewhaleTest } from '../codewhale-fixtures'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, userBubbles, waitForControlBanner } from '../helpers/ui'

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

codewhaleTest('keeps an exact native reply readable while a later input waits for permission', async ({ native, leapmuxServer, modelScript, page }) => {
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { id: agentId, workingDir } = await currentNativeAgent(native)
  const firstPrompt = 'Summarize the CodeWhale readiness check.'
  const firstReply = 'The CodeWhale readiness check completed.'
  await modelScript.queue({ text: firstReply })
  const first = sendAndWait(agentId, modelScript.prompt(firstPrompt), 'codewhale-readiness')
  const completed = await runCLI(cli, first) as { input_id: string }
  expect(completed).toMatchObject({
    message: 'delivered',
    turn_outcome: 'completed',
    replies: [firstReply],
  })

  const secondReply = 'The approved file now exists.'
  const file = join(workingDir, 'cli-permission.txt')
  await modelScript.queue({
    toolCalls: [
      bashToolCall(native.provider, 'cli-permission-write', 'touch cli-permission.txt'),
    ],
  }, { text: secondReply })
  const second = sendAndWait(agentId, modelScript.prompt('Run the requested file creation.'), 'codewhale-permission')
  const waiting = await runCLI(cli, second) as { input_id: string }
  expect(waiting).toMatchObject({ message: 'delivered', state: 'waiting_for_user', replies: [] })
  await waitForControlBanner(page)
  expect(existsSync(file)).toBe(false)
  expect(await runCLI(cli, first)).toMatchObject({
    input_id: completed.input_id,
    turn_outcome: 'completed',
    replies: [firstReply],
  })
  await expect(userBubbles(page).filter({ hasText: firstPrompt })).toHaveCount(1)

  const { requests } = await runCLI(cli, ['agent', 'requests', agentId]) as { requests: Array<{ id: string }> }
  expect(requests).toHaveLength(1)
  const [permission] = requests
  if (!permission)
    throw new Error('The Worker returned no CodeWhale permission request.')
  await runCLI(cli, [
    'agent',
    'approve',
    agentId,
    permission.id,
  ])
  expect(await runCLI(cli, second)).toMatchObject({
    input_id: waiting.input_id,
    turn_outcome: 'completed',
    replies: [secondReply],
  })
  expect(existsSync(file)).toBe(true)
  await expect(assistantBubbles(page).filter({ hasText: secondReply })).toHaveCount(1)
})
