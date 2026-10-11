import type { ProviderConfigurationCapability } from '../capabilities'
import { KIMI_DEFAULT_MODE, KIMI_MODE } from '~/generated/contracts/kimi-protocol'
import { buildPlanMode } from '../../settingsGroups'

/** The complete Kimi Code configuration channel, separate from registration. */
export const kimiConfiguration: ProviderConfigurationCapability = {
  // Text joins the prompt inline and an image travels as an image part. The server's
  // file part hands the model a path, which states nothing about a file LeapMux holds
  // only as bytes, so a PDF and a binary file are refused. The worker's
  // `ValidateAttachment` states the same rule, and it also refuses an image for a model
  // that takes none.
  attachments: { text: true, image: true, pdf: false, binary: false },
  // Plan mode is one value of the permission-mode axis, as it is for Claude and ZCode.
  triggerModeGroupKey: 'permissionMode',
  planMode: buildPlanMode('permissionMode', KIMI_MODE.Plan, KIMI_DEFAULT_MODE),
  // A subagent's tab sends it messages, which the server runs as its next turn.
  supportsSubagentSend: true,
}
