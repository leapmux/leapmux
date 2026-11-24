import { expect } from '@playwright/test'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { userBubbles, waitForControlBanner } from '../helpers/ui'
import { lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

function sendAndWait(agentId: string, prompt: string, requestId: string): string[] {
  return [
    'agent',
    'send',
    agentId,
    prompt,
    '--request-id',
    requestId,
    '--wait',
    '--all',
    '--timeout',
    '25s',
  ]
}

lettaTest('retries the exact reply while a later native permission is open', async ({ askingLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const native = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: askingLettaWorkspace.workspaceId,
  })
  const { id } = await currentNativeAgent(native)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const prompt = 'Check the release checklist.'
  const answer = 'The release checklist passed.'
  await modelScript.queue({ text: answer })
  const first = sendAndWait(id, modelScript.prompt(prompt), 'letta-release-check')
  const result = await runCLI(cli, first) as { input_id: string }
  expect(result).toMatchObject({
    turn_outcome: 'completed',
    replies: [answer],
  })

  const nextReply = 'I wrote the requested file.'
  await modelScript.queue({
    toolCalls: [
      bashToolCall(native.provider, 'letta-cli-permission', 'printf cli-approved | tee cli-approved.txt'),
    ],
  }, { text: nextReply })
  const second = sendAndWait(id, modelScript.prompt('Try writing the scripted file.'), 'letta-file-permission')
  const waiting = await runCLI(cli, second) as { input_id: string }
  expect(waiting).toMatchObject({ state: 'waiting_for_user', replies: [] })
  await expect(await waitForControlBanner(page)).toContainText('Bash')
  expect(await runCLI(cli, first)).toMatchObject({
    input_id: result.input_id,
    turn_outcome: 'completed',
    replies: [answer],
  })

  const { requests } = await runCLI(cli, ['agent', 'requests', id]) as { requests: Array<{ id: string }> }
  expect(requests).toHaveLength(1)
  await runCLI(cli, [
    'agent',
    'approve',
    id,
    requests[0]!.id,
  ])
  expect(await runCLI(cli, second)).toMatchObject({
    input_id: waiting.input_id,
    turn_outcome: 'completed',
    replies: [nextReply],
  })
  await expect(userBubbles(page).filter({ hasText: prompt })).toHaveCount(1)
})
