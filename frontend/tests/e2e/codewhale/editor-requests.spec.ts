import { codewhaleExtractControl } from '../../../src/components/chat/providers/codewhale/extractControl'
import { codewhaleTest } from '../codewhale-fixtures'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { exerciseUnsupportedControlThroughPermission } from '../helpers/unsupportedNativeControl'

// LeapMux exposes no native multiline editor route for this provider.
codewhaleTest('classifies real native controls and proves the missing editor-requests route', async ({ native }) => {
  await chooseSettingsOption(native.page, 'permissionMode-ask')
  await waitForSettingsIdle(native.page)
  await exerciseUnsupportedControlThroughPermission(native, { purpose: 'editor', classify: codewhaleExtractControl })
})
