import { expectUnsupportedFork } from '../helpers/nativeFork'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('reports supportsFork false and refuses native session forking', async ({ native }) => {
  await expectUnsupportedFork(native)
})
