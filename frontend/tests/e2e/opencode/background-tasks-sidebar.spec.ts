import { opencodeTest } from '../opencode-fixtures'
import { exerciseOpencodeSpawnTranscript } from './childScenario'

opencodeTest('background-tasks-sidebar: subagent spawn creates a prompt and report transcript', async ({ native }) => {
  await exerciseOpencodeSpawnTranscript(native)
})
