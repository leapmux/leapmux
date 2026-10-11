import { zcodeTest } from '../zcode-fixtures'
import { exerciseZCodeChildToolTranscript } from './childScenario'

zcodeTest('routes the prompt, tools, and final report into the child tab', async ({ native }) => {
  await exerciseZCodeChildToolTranscript(native)
})
