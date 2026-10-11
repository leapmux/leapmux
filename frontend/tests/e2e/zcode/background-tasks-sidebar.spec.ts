import { zcodeTest } from '../zcode-fixtures'
import { exerciseZCodeChildToolTranscript } from './childScenario'

zcodeTest('background-tasks-sidebar: routes the prompt, tools, and final report into the child tab', async ({ native }) => {
  await exerciseZCodeChildToolTranscript(native)
})
