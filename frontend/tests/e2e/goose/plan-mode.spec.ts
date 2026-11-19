import { GOOSE_MODE } from '../../../src/generated/contracts/goose-protocol'
import { gooseTest } from '../goose-fixtures'
import { expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { expectNoPlanOption } from '../helpers/unsupportedPlanMode'

gooseTest('offers the native execution modes without a Plan mode', async ({ native }) => {
  await expectNoPlanOption(native)
  await expectSettingsOptionsOffered(native.page, 'permissionMode', Object.values(GOOSE_MODE))
})
