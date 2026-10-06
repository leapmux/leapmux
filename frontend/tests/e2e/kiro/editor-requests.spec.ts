import { kiroExtractControl } from '../../../src/components/chat/providers/kiro/extractControl'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { exerciseUnsupportedControlThroughPermission } from '../helpers/unsupportedNativeControl'
import { kiroTest } from '../kiro-fixtures'

// LeapMux exposes no native multiline editor route for this provider.
kiroTest('classifies real native controls and proves the missing editor-requests route', async ({ native }) => {
  await chooseSettingsOption(native.page, 'policyPreset-ask')
  await waitForSettingsIdle(native.page)
  await exerciseUnsupportedControlThroughPermission(native, { purpose: 'editor', classify: kiroExtractControl })
})
