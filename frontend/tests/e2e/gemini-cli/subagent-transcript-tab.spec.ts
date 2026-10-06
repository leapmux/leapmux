import { unlinkSync } from 'node:fs'
import { join } from 'node:path'
import { geminiTest } from '../gemini-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { nativeAgentById } from '../helpers/nativeScenario'
import { expectGeminiLiveChild, finishGeminiChildWithReload, openGeminiRunningChild } from './childScenarios'
import { geminiNativeProject } from './nativeStore'
import { nativeContext } from './scenarios'

geminiTest('stores the original native child prompt and results in its distinct tab after reload', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  const child = await openGeminiRunningChild(context)
  await withCleanup(async () => {
    await expectGeminiLiveChild(context, child)
    await finishGeminiChildWithReload(context, child, { beforeReload: async () => {
      const parent = await nativeAgentById(context, child.parentId)
      if (!parent)
        throw new Error('The stored native child lost its parent identity.')
      unlinkSync(join(geminiNativeProject(context, parent), 'chats', parent.agentSessionId, `${child.nativeChildId}.jsonl`))
    } })
  }, child.finish)
})
