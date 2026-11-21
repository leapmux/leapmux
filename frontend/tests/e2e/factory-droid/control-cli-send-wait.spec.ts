import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { droidTest } from '../droid-fixtures'
import { mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { askUserQuestionToolCall, editToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, savedControlAnswer, userBubbles, waitForControlBanner } from '../helpers/ui'
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
    '--timeout',
    '20s',
  ]
}

droidTest('returns each Droid reply to its own keyed input', async ({ askingDroidWorkspace, leapmuxServer, modelScript, page }) => {
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: askingDroidWorkspace.workspaceId,
  })
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { id: agentId, workingDir } = await currentNativeAgent(context)
  const firstPrompt = 'Summarize the Droid readiness check.'
  const firstReply = 'Droid completed the readiness check.'
  await modelScript.queue({ text: firstReply })
  const first = sendAndWait(agentId, modelScript.prompt(firstPrompt), 'droid-readiness')
  const completed = await runCLI(cli, first) as { input_id: string }
  expect(completed).toMatchObject({
    message: 'delivered',
    turn_outcome: 'completed',
    replies: [firstReply],
  })

  const note = join(workingDir, 'cli-permission-note.txt')
  writeFileSync(note, 'old text\n')
  const secondReply = 'The approved edit applied.'
  await modelScript.queue({
    toolCalls: [
      editToolCall(context.provider, 'cli-permission-edit', { path: 'cli-permission-note.txt', before: 'old text', after: 'new text' }),
    ],
  }, { text: secondReply })
  const second = sendAndWait(agentId, modelScript.prompt('Apply the requested edit.'), 'droid-permission')
  const waiting = await runCLI(cli, second) as { input_id: string }
  expect(waiting).toMatchObject({ message: 'delivered', state: 'waiting_for_user', replies: [] })
  await waitForControlBanner(page)
  expect(readFileSync(note, 'utf8')).toBe('old text\n')
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
    throw new Error('The Worker returned no Droid permission request.')
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
  expect(readFileSync(note, 'utf8')).toBe('new text\n')
  await expect(assistantBubbles(page).filter({ hasText: secondReply })).toHaveCount(1)
})

droidTest('returns the same input reply after answering its native question', async ({ askingDroidWorkspace, leapmuxServer, modelScript, page }) => {
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: askingDroidWorkspace.workspaceId,
  })
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const { id: agentId } = await currentNativeAgent(context)
  const question = 'Which color do you prefer?'
  const reply = 'The selected color was recorded.'
  const toolCall = askUserQuestionToolCall(context.provider, 'cli-color-question', [
    {
      question,
      header: 'Color',
      options: [
        { label: 'Blue', description: 'The color blue' },
        { label: 'Red', description: 'The color red' },
      ],
    },
  ])
  await modelScript.queue(
    {
      toolCalls: [toolCall],
    },
    { text: reply },
  )
  const send = sendAndWait(agentId, modelScript.prompt('Ask me to select a color.'), 'droid-color-question')
  const waiting = await runCLI(cli, send) as { input_id: string }
  expect(waiting).toMatchObject({ message: 'delivered', state: 'waiting_for_user', replies: [] })
  await expect(await waitForControlBanner(page)).toContainText(question)

  const { requests } = await runCLI(cli, ['agent', 'requests', agentId]) as { requests: Array<{ id: string }> }
  expect(requests).toHaveLength(1)
  const [request] = requests
  if (!request)
    throw new Error('The Worker returned no Droid question.')
  await runCLI(cli, [
    'agent',
    'answer',
    agentId,
    request.id,
    '--option',
    '2',
  ])
  expect(await runCLI(cli, send)).toMatchObject({
    input_id: waiting.input_id,
    turn_outcome: 'completed',
    replies: [reply],
  })
  await expect(savedControlAnswer(page)).toHaveText(`${question}: Red`)
  await expect(assistantBubbles(page).filter({ hasText: reply })).toHaveCount(1)
})
