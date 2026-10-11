import { grokExtractControl } from '../../../src/components/chat/providers/grok/extractControl'
import { grokTest } from '../grok-fixtures'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { exerciseUnsupportedControlThroughPermission } from '../helpers/unsupportedNativeControl'

// LeapMux exposes no native multiline editor route for this provider.
grokTest('classifies real native controls and proves the missing editor-requests route', async ({ native }) => {
  await chooseSettingsOption(native.page, 'approvalMode-ask')
  await waitForSettingsIdle(native.page)
  await exerciseUnsupportedControlThroughPermission(native, { purpose: 'editor', classify: grokExtractControl })
})
