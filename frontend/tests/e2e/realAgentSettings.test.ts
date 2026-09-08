import { describe, expect, it } from 'vitest'
import { REAL_AGENT_E2E_SETTINGS, realAgentOpenOptions } from './realAgentSettings'

describe('real-agent end-to-end settings', () => {
  it('pins each provider to the selected model and effort', () => {
    expect(REAL_AGENT_E2E_SETTINGS).toEqual({
      claudeCode: { model: 'sonnet', effort: 'medium' },
      codex: { model: 'gpt-5.6-luna', effort: 'medium' },
      copilot: { model: 'gpt-5.6-luna', effort: 'medium' },
      cursor: { model: 'auto' },
      goose: { model: 'glm-5.3-flash', effort: 'high' },
      kilo: { model: 'zai-coding-plan/glm-5.3-flash', effort: 'high' },
      opencode: { model: 'zai-coding-plan/glm-5.3-flash', effort: 'high' },
      pi: { model: 'glm-5.3-flash', effort: 'high' },
      reasonix: { model: 'deepseek-flash' },
      zcode: { model: 'builtin:zai-coding-plan/GLM-5.3-Flash', effort: 'high' },
    })
  })

  it('maps an effort into the worker option vocabulary', () => {
    expect(realAgentOpenOptions(REAL_AGENT_E2E_SETTINGS.pi)).toEqual({
      model: 'glm-5.3-flash',
      optionValues: { effort: 'high' },
    })
  })

  it('omits the effort option for a model without that setting', () => {
    expect(realAgentOpenOptions(REAL_AGENT_E2E_SETTINGS.reasonix)).toEqual({
      model: 'deepseek-flash',
    })
  })
})
