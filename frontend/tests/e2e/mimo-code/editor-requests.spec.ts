import { mimoExtractControl } from '../../../src/components/chat/providers/mimo/extractControl'
import { MIMO_OPTION, MIMO_PERMISSION_POLICY } from '../../../src/generated/contracts/mimo-protocol'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { exerciseUnsupportedControlThroughPermission } from '../helpers/unsupportedNativeControl'
import { mimoTest } from '../mimo-fixtures'
import { createMiMoControlDeletion } from './controlScenarios'

// LeapMux exposes no native multiline editor route for this provider.
// MiMo asks for a deletion, and the deletion holds an output gate (see `createMiMoControlDeletion`).
mimoTest('classifies real native controls and proves the missing editor-requests route', async ({ native }) => {
  await chooseSettingsOption(native.page, `${MIMO_OPTION.PermissionPolicy}-${MIMO_PERMISSION_POLICY.Ask}`)
  await waitForSettingsIdle(native.page)
  await exerciseUnsupportedControlThroughPermission(native, {
    purpose: 'editor',
    classify: mimoExtractControl,
    operation: await createMiMoControlDeletion(native, 'editor'),
  })
})
