import { unlinkSync } from 'node:fs'
import { join } from 'node:path'
import { geminiTest } from '../gemini-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { nativeAgentById } from '../helpers/nativeScenario'
import { expectGeminiLiveChild, finishGeminiChildWithReload, openGeminiRunningChild } from './childScenarios'
import { geminiNativeProject } from './nativeStore'

geminiTest('stores the original native child prompt and results in its distinct tab after reload', async ({ native }) => {
  const child = await openGeminiRunningChild(native)
  await withCleanup(async () => {
    await expectGeminiLiveChild(native, child)
    await finishGeminiChildWithReload(native, child, { beforeReload: async () => {
      const parent = await nativeAgentById(native, child.parentId)
      if (!parent)
        throw new Error('The stored native child lost its parent identity.')
      unlinkSync(join(geminiNativeProject(native, parent), 'chats', parent.agentSessionId, `${child.nativeChildId}.jsonl`))
    } })
  }, child.finish)
})
