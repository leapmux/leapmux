import { copilotTest } from '../copilot-fixtures'
import { expectUnsupportedFork } from '../helpers/nativeFork'

copilotTest('reports supportsFork false and refuses native session forking', async ({ native }) => {
  await expectUnsupportedFork(native)
})
