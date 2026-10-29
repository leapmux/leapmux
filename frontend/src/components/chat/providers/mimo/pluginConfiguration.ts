import type { ProviderConfigurationCapability } from '../capabilities'
import { MIMO_AUDIO_MIME, MIMO_DEFAULT_MODE, MIMO_MODE } from '~/generated/contracts/mimo-protocol'

/** The complete MiMo configuration channel, separate from registration. */
export const mimoConfiguration: ProviderConfigurationCapability = {
  // Media travels as file parts. Audio also requires the native model's audio
  // capability, which the worker checks before it writes the prompt.
  attachments: {
    text: true,
    image: true,
    pdf: true,
    audio: [...MIMO_AUDIO_MIME],
    binary: false,
  },
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
