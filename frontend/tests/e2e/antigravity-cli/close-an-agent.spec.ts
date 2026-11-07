import { antigravityTest } from '../antigravity-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'

antigravityTest('closes the agent tab and waits for its owned native process', async ({ native }) => {
  await exerciseCloseAgent(native)
})
