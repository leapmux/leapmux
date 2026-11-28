import { clineTest } from '../cline-fixtures'
import { expectUnsupportedFork } from '../helpers/nativeFork'

clineTest('reports supportsFork false and refuses native session forking', async ({ native }) => {
  await expectUnsupportedFork(native)
})
