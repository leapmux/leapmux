import { clineExtractControl } from '../../../src/components/chat/providers/cline/extractControl'
import { clineTest } from '../cline-fixtures'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { exerciseUnsupportedControlThroughPermission } from '../helpers/unsupportedNativeControl'

// LeapMux exposes no native multiline editor route for this provider.
clineTest('classifies real native controls and proves the missing editor-requests route', async ({ native }) => {
  await chooseSettingsOption(native.page, 'permissionMode-act')
  await waitForSettingsIdle(native.page)
  await exerciseUnsupportedControlThroughPermission(native, { purpose: 'editor', classify: clineExtractControl })
})
