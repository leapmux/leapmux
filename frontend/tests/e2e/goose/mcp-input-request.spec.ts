import { gooseTest } from '../goose-fixtures'
import { exerciseGooseMcpForm } from './mcpScenario'

gooseTest('roundtrips zero, false, and blue through native form elicitation', async ({ native }) => {
  await exerciseGooseMcpForm(native)
})
