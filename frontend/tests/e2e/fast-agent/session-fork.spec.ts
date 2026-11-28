import { fastAgentTest } from '../fastagent-fixtures'
import { expectUnsupportedFork } from '../helpers/nativeFork'

fastAgentTest('reports supportsFork false and refuses native session forking', async ({ native }) => {
  await expectUnsupportedFork(native)
})
