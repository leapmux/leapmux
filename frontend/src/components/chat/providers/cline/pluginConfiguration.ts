import type { ProviderConfigurationCapability } from '../capabilities'
import { CLINE_PERMISSION_MODE } from '~/generated/contracts/cline-protocol'
import { buildPlanMode } from '../../settingsGroups'

/** The complete Cline configuration channel, separate from registration. */
export const clineConfiguration: ProviderConfigurationCapability = {
  // The same policy as the worker's ValidateAttachment: Cline takes an image as a data
  // URL and a text file as a path it reads itself, and it has no input for a PDF or
  // another binary file.
  attachments: { text: true, image: true, pdf: false, binary: false },
  // Plan, Act and Auto-approve share the permission-mode axis, and Plan is the plan
  // toggle's value, as it is for Claude and Kimi Code.
  triggerModeGroupKey: 'permissionMode',
  planMode: buildPlanMode('permissionMode', CLINE_PERMISSION_MODE.Plan, CLINE_PERMISSION_MODE.Act),
}
