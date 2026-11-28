import { expectUnsupportedFork } from '../helpers/nativeFork'
import { kimiTest } from '../kimi-fixtures'

kimiTest('reports supportsFork false and refuses native session forking', async ({ native }) => {
  await expectUnsupportedFork(native)
})
