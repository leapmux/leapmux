import { gooseTest } from '../goose-fixtures'
import { exerciseGooseModelAndEffort } from './settingsScenario'

gooseTest('keeps the high effort after a turn and reload', async ({ native }) => {
  await exerciseGooseModelAndEffort(native, 'effort')
})
