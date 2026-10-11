import { kiloTest } from '../kilo-fixtures'
import { exerciseKiloSpawnTranscript } from './childScenario'

kiloTest('subagent spawn creates a prompt and report transcript', async ({ native }) => {
  await exerciseKiloSpawnTranscript(native)
})
