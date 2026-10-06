/** Test the held child prompt and file result in its own tab. */
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { codexTest } from '../codex-fixtures'
import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { tabById } from '../helpers/ui'

/** Require the actual Codex child completion to retain its native thread and turn IDs. */
async function expectNativeCodexCompletion(context: Pick<ManagedNativeScenarioContext, 'page' | 'leapmuxServer'>): Promise<void> {
  const child = await currentNativeAgent(context)
  const endings = async () => {
    const snapshot = await readNativeMessageSnapshot(context, child.id)
    const results = snapshot.messages.flatMap((message) => {
      const body = nativeMessageBody(message)
      return message.source === MessageSource.AGENT && isObject(body) && isObject(body.turn) && body.turn.status === 'completed'
        ? [{ message, body, turn: body.turn }]
        : []
    })
    return { snapshot, results }
  }
  await expect.poll(async () => (await endings()).results.length).toBe(1)
  const { snapshot, results } = await endings()
  const result = results[0]
  if (!result)
    throw new Error('The native Codex child has no stored turn completion.')
  expect(result.body.threadId).toBe(child.providerChildKey)
  expect(result.turn.id).toEqual(expect.any(String))
  expect(result.turn.id).not.toBe('')
  expect(snapshot.messages.map(nativeMessageBody).filter(body => isObject(body) && body.type === 'subagent_ended')).toHaveLength(0)
  await expect(context.page.locator('[data-testid="result-divider"]:visible')).toHaveCount(1)
  await expect(context.page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCount(0)
  await context.page.reload()
  await tabById(context.page, child.id).click()
  await expect(context.page.locator('[data-testid="result-divider"]:visible')).toHaveCount(1)
  const reopened = await endings()
  expect(reopened.results).toHaveLength(1)
  expect(reopened.snapshot.messages).toEqual(snapshot.messages)
}

codexTest.describe('codex subagent lifecycle', () => {
  codexTest('shows a child prompt while its own model request waits', async ({ native }) => {
    await exerciseLiveChildTranscript(native, {
      childWhen: { body: ['NEW_TASK', 'answer_the_live_child_task'] },
      childTask: 'Report the live child marker.',
      parentTask: 'Spawn one child to report the live child marker.',
      afterComplete: () => expectNativeCodexCompletion(native),
    })
  })

  codexTest('shows a child file result only in the running child tab', async ({ native, authenticatedCodexWorkspace }) => {
    const workingDir = authenticatedCodexWorkspace.workingDir
    if (!workingDir)
      throw new Error('The live child file proof requires a working directory.')
    await exerciseLiveChildTranscript(native, {
      childWhen: { body: ['NEW_TASK', 'answer_the_live_child_task'] },
      childTask: 'Read the assigned file in the live child task.',
      parentTask: 'Start one child to read the assigned file.',
      toolProof: { read: { workingDir } },
      afterComplete: () => expectNativeCodexCompletion(native),
    })
  })
})
