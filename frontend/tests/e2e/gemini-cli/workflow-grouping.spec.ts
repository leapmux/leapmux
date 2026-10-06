import { geminiTest } from '../gemini-fixtures'
import { exerciseUngroupedNativeChildren } from '../helpers/ungroupedNativeChildren'
import { openGeminiRunningChild } from './childScenarios'
import { nativeContext } from './scenarios'

geminiTest('keeps two exact native child UUIDs distinct without an invented workflow group', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  await exerciseUngroupedNativeChildren(context, { openChild: index => openGeminiRunningChild(context, index) })
})
