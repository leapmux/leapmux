import { geminiTest } from '../gemini-fixtures'
import { exerciseUngroupedNativeChildren } from '../helpers/workflowGrouping'
import { openGeminiRunningChild } from './childScenarios'

geminiTest('keeps two exact native child UUIDs distinct without an invented workflow group', async ({ native }) => {
  await exerciseUngroupedNativeChildren(native, { openChild: slot => openGeminiRunningChild(native, slot.index) })
})
