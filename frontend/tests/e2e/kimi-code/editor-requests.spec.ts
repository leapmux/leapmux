import { kimiExtractControl } from '../../../src/components/chat/providers/kimi/extractControl'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { exerciseUnsupportedControlThroughPermission } from '../helpers/unsupportedNativeControl'
import { kimiTest } from '../kimi-fixtures'

// LeapMux exposes no native multiline editor route for this provider.
kimiTest('classifies real native controls and proves the missing editor-requests route', async ({ native }) => {
  await chooseSettingsOption(native.page, 'permissionMode-manual')
  await waitForSettingsIdle(native.page)
  await exerciseUnsupportedControlThroughPermission(native, { purpose: 'editor', classify: kimiExtractControl })
})
