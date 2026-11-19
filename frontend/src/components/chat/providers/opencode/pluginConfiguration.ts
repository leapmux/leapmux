import type { ProviderConfigurationCapability } from '../capabilities'

export const openCodeConfiguration: ProviderConfigurationCapability = {
  attachments: {
    text: true,
    image: true,
    pdf: true,
    audio: false,
    binary: false,
  },
  triggerModeGroupKey: 'primaryAgent',
  planMode: {
    groupKey: 'primaryAgent',
    currentMode: agent => agent.optionValues?.primaryAgent ?? 'build',
    planValue: 'plan',
    defaultValue: 'build',
  },
  supportsSubagentSend: true,
}
