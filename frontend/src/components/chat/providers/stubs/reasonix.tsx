import type { ACPSettingsPanelConfig } from '../acp/registerACPProvider'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { registerACPProvider } from '../acp/registerACPProvider'

// Reasonix (DeepSeek) is a model-only ACP provider: the model is fixed at
// startup (the worker passes `--model`), and there is no runtime permission
// mode or config-option channel — so the settings panel shows just a model
// selector. Reasonix is also text-only (it advertises image:false), so override
// the default full-attachment capability.
const settingsConfig: ACPSettingsPanelConfig = {
  kind: 'modelOnly',
}

registerACPProvider({
  provider: AgentProvider.REASONIX,
  settingsConfig,
  attachments: { text: true, image: false, pdf: false, binary: false },
})
