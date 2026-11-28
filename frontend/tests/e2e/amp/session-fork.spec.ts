import { ampTest } from '../amp-fixtures'
import { expectUnsupportedFork } from '../helpers/nativeFork'

ampTest('reports supportsFork false and refuses native session forking', async ({ native }) => {
  await expectUnsupportedFork(native)
})
