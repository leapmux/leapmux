import { expectUnsupportedFork } from '../helpers/nativeFork'
import { qoderTest } from '../qoder-fixtures'

qoderTest('reports supportsFork false and refuses native session forking', async ({ native }) => {
  await expectUnsupportedFork(native)
})
