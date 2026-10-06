import { zcodeTest } from '../zcode-fixtures'
import { exerciseZCodeRemovalPermission } from './permissionScenario'

zcodeTest('a risky command produces a permission banner that can be denied', async ({ native }) => {
  await exerciseZCodeRemovalPermission(native, { bypass: false })
})

zcodeTest('the permission banner applies the selected bypass pill on allow', async ({ native }) => {
  await exerciseZCodeRemovalPermission(native, { bypass: true })
})
