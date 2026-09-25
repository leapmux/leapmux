import type { ProviderConfigurationCapability } from '../capabilities'
import { MIMO_DEFAULT_MODE, MIMO_MODE } from '~/generated/contracts/mimo-protocol'

/** The complete MiMo configuration channel, separate from registration. */
export const mimoConfiguration: ProviderConfigurationCapability = {
  // Text is inlined into the prompt, and an image or a PDF travels as a file part
  // that MiMo hands to the model. A file of another type would reach the model's API
  // unconverted, so LeapMux refuses it, as the worker's ValidateAttachment does.
  attachments: { text: true, image: true, pdf: true, binary: false },
  // MiMo's primary agents -- build, plan and any the configuration adds -- ride the
  // permission-mode axis, so plan mode and its toggle work as for every provider.
  triggerModeGroupKey: 'permissionMode',
  planMode: {
    groupKey: 'permissionMode',
    currentMode: agent => agent.optionValues?.permissionMode ?? MIMO_DEFAULT_MODE,
    planValue: MIMO_MODE.Plan,
    defaultValue: MIMO_DEFAULT_MODE,
  },
  // A subagent is an actor inside the parent's session. A message to a RUNNING
  // subagent reaches it through the same prompt route and joins its turn. MiMo
  // reports no turn that a message starts on an idle subagent, so the worker
  // refuses that message and states why (mimo/subagent.go, sendChildInput).
  supportsSubagentSend: true,
}
