import { gooseTest } from '../goose-fixtures'
import { exerciseGooseDelegateTranscript } from './childScenario'

gooseTest('background-tasks-sidebar: delegate spawn creates a clickable row with a tool-request transcript', async ({ native }) => {
  await exerciseGooseDelegateTranscript(native)
})
