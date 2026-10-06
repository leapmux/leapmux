import { ampExtractControl } from '../../../src/components/chat/providers/amp/extractControl'
import { ampTest } from '../amp-fixtures'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { exerciseUnsupportedControlThroughPermission } from '../helpers/unsupportedNativeControl'

// LeapMux exposes no native multiline editor route for this provider.
ampTest('classifies real native controls and proves the missing editor-requests route', async ({ native }) => {
  await chooseSettingsOption(native.page, 'permissionMode-ask')
  await waitForSettingsIdle(native.page)
  await exerciseUnsupportedControlThroughPermission(native, { purpose: 'editor', classify: ampExtractControl })
})
