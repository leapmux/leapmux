import { expectUnsupportedFork } from '../helpers/nativeFork'
import { museTest } from '../muse-fixtures'

museTest('reports supportsFork false and refuses native session forking', async ({ native }) => {
  await expectUnsupportedFork(native)
})
