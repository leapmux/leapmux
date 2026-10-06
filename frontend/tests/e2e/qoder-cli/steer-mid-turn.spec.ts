import { exerciseSteerBeforeTool } from '../helpers/nativeToolSteering'
import { qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI live child transcript', () => {
  qoderTest('sends a queued message into the active turn', async ({ native }) => {
    await exerciseSteerBeforeTool(native)
  })
})
