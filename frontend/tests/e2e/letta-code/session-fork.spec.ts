import { expectUnsupportedFork } from '../helpers/nativeFork'
import { lettaTest } from '../letta-fixtures'

lettaTest('reports supportsFork false and refuses native session forking', async ({ native }) => {
  await expectUnsupportedFork(native)
})
