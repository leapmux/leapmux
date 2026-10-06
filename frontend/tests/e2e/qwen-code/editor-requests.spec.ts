import { qwenExtractControl } from '../../../src/components/chat/providers/qwen/extractControl'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { exerciseUnsupportedControlThroughPermission } from '../helpers/unsupportedNativeControl'
import { qwenTest } from '../qwen-fixtures'

// LeapMux exposes no native multiline editor route for this provider.
qwenTest('classifies real native controls and proves the missing editor-requests route', async ({ native }) => {
  await chooseSettingsOption(native.page, 'permissionMode-default')
  await waitForSettingsIdle(native.page)
  await exerciseUnsupportedControlThroughPermission(native, { purpose: 'editor', classify: qwenExtractControl })
})
