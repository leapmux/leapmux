import { describe, expect, it } from 'vitest'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { REAL_AGENT_E2E_SETTINGS, realAgentEnv, realAgentOpenOptions, realAgentSettings } from './realAgentSettings'

// The cases below assert the catalog's SHAPE and the two mappings that read it.
// They never restate a model id: a second copy of the table would fail on every
// model bump and catch nothing, because the copy and the source are one file
// apart and change together.
describe('real-agent end-to-end settings', () => {
  it('pins a usable model for every provider', () => {
    for (const [provider, settings] of Object.entries(REAL_AGENT_E2E_SETTINGS)) {
      expect(settings.model, `AgentProvider ${provider}`).not.toBe('')
      expect(settings.model.trim(), `AgentProvider ${provider}`).toBe(settings.model)
    }
  })

  // An empty string reaches the worker as a request to CLEAR the option, so it
  // opens the agent at the account default -- the state this catalog exists to
  // exclude. Omit the field instead.
  it('never pins an empty effort', () => {
    for (const [provider, settings] of Object.entries(REAL_AGENT_E2E_SETTINGS))
      expect('effort' in settings ? settings.effort : 'high', `AgentProvider ${provider}`).not.toBe('')
  })

  it('covers every provider the enum offers', () => {
    const missing = Object.values(AgentProvider)
      .filter((value): value is AgentProvider => typeof value === 'number')
      .filter(provider => provider !== AgentProvider.UNSPECIFIED)
      .filter(provider => !(provider in REAL_AGENT_E2E_SETTINGS))
    expect(missing).toEqual([])
  })

  it('refuses a provider it does not pin', () => {
    expect(() => realAgentSettings(AgentProvider.UNSPECIFIED)).toThrow(/no pinned model/)
  })

  it('maps an effort into the worker option vocabulary', () => {
    expect(realAgentOpenOptions({ model: 'a-model', effort: 'high' })).toEqual({
      model: 'a-model',
      optionValues: { effort: 'high' },
    })
  })

  it('omits the effort option for a model without that setting', () => {
    expect(realAgentOpenOptions({ model: 'a-model' })).toEqual({ model: 'a-model' })
  })

  it('builds the spawn environment from the catalog', () => {
    const env = realAgentEnv()
    expect(env.LEAPMUX_CLAUDE_DEFAULT_MODEL).toBe(REAL_AGENT_E2E_SETTINGS[AgentProvider.CLAUDE_CODE].model)
    expect(env.LEAPMUX_CODEX_DEFAULT_EFFORT).toBe(REAL_AGENT_E2E_SETTINGS[AgentProvider.CODEX].effort)
    expect(env.LEAPMUX_COPILOT_DEFAULT_MODEL).toBe(REAL_AGENT_E2E_SETTINGS[AgentProvider.GITHUB_COPILOT].model)
    // Copilot registers no env effort key: its reasoning axis is the daemon's
    // `reasoning_effort` config option, so the worker reads no such variable.
    expect(env).not.toHaveProperty('LEAPMUX_COPILOT_DEFAULT_EFFORT')
  })
})
