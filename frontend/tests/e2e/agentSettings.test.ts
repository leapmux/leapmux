import { describe, expect, it } from 'vitest'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { AGENT_E2E_SETTINGS, agentDefaultsEnv, agentOpenOptions, agentSettings } from './agentSettings'

// The cases below assert the catalog's SHAPE and the two mappings that read it.
// They never restate a model id: a second copy of the table would fail on every
// model bump and catch nothing, because the copy and the source are one file
// apart and change together.
describe('real-agent end-to-end settings', () => {
  it('pins a usable model for every provider', () => {
    for (const [provider, settings] of Object.entries(AGENT_E2E_SETTINGS)) {
      expect(settings.model, `AgentProvider ${provider}`).not.toBe('')
      expect(settings.model.trim(), `AgentProvider ${provider}`).toBe(settings.model)
    }
  })

  // An empty string reaches the worker as a request to CLEAR the option, so it
  // opens the agent at the account default -- the state this catalog exists to
  // exclude. Omit the field instead.
  it('never pins an empty effort', () => {
    for (const [provider, settings] of Object.entries(AGENT_E2E_SETTINGS))
      expect('effort' in settings ? settings.effort : 'high', `AgentProvider ${provider}`).not.toBe('')
  })

  it('covers every provider the enum offers', () => {
    const missing = Object.values(AgentProvider)
      .filter((value): value is AgentProvider => typeof value === 'number')
      .filter(provider => provider !== AgentProvider.UNSPECIFIED)
      .filter(provider => !(provider in AGENT_E2E_SETTINGS))
    expect(missing).toEqual([])
  })

  it('refuses a provider it does not pin', () => {
    expect(() => agentSettings(AgentProvider.UNSPECIFIED)).toThrow(/no pinned model/)
  })

  it('builds the pinned model and maps the pinned effort into the worker option vocabulary', () => {
    const pinned = AGENT_E2E_SETTINGS[AgentProvider.PI]
    expect(agentOpenOptions(AgentProvider.PI)).toEqual({
      agentProvider: AgentProvider.PI,
      model: pinned.model,
      optionValues: { effort: pinned.effort },
    })
  })

  it('states no effort option for a provider whose pinned model has no effort', () => {
    expect(agentOpenOptions(AgentProvider.GOOSE)).toEqual({
      agentProvider: AgentProvider.GOOSE,
      model: AGENT_E2E_SETTINGS[AgentProvider.GOOSE].model,
      optionValues: {},
    })
  })

  // A spread of pinned settings and a second `optionValues` dropped the pinned effort. The merge keeps it.
  it('keeps the pinned effort under the option values of the test', () => {
    expect(agentOpenOptions(AgentProvider.OH_MY_PI, { optionValues: { permissionMode: 'yolo' } }).optionValues).toEqual({
      effort: AGENT_E2E_SETTINGS[AgentProvider.OH_MY_PI].effort,
      permissionMode: 'yolo',
    })
  })

  it('lets an option value of the test replace the pinned value of the same ID', () => {
    expect(agentOpenOptions(AgentProvider.DROID, { optionValues: { effort: 'high' } }).optionValues).toEqual({ effort: 'high' })
  })

  it('takes the model of the test and keeps the pinned effort', () => {
    expect(agentOpenOptions(AgentProvider.QWEN_CODE, { model: 'another-model' })).toEqual({
      agentProvider: AgentProvider.QWEN_CODE,
      model: 'another-model',
      optionValues: { effort: AGENT_E2E_SETTINGS[AgentProvider.QWEN_CODE].effort },
    })
  })

  // The open request writes the model last, so a model given as an option value lost to the pinned model with no message.
  it('refuses a model given as an option value', () => {
    expect(() => agentOpenOptions(AgentProvider.FAST_AGENT, { optionValues: { model: 'another-model' } })).toThrow('give the model as `model`')
  })

  it.each(['', ' '])('refuses an empty model override: %j', (model) => {
    expect(() => agentOpenOptions(AgentProvider.FAST_AGENT, { model })).toThrow('needs a model ID')
  })

  it('does not change the option values that the test passes', () => {
    const optionValues = { permissionMode: 'plan' }
    agentOpenOptions(AgentProvider.PI, { optionValues })
    expect(optionValues).toEqual({ permissionMode: 'plan' })
  })

  it('builds the spawn environment from the catalog', () => {
    const env = agentDefaultsEnv()
    expect(env.LEAPMUX_CLAUDE_DEFAULT_MODEL).toBe(AGENT_E2E_SETTINGS[AgentProvider.CLAUDE_CODE].model)
    expect(env.LEAPMUX_CODEX_DEFAULT_EFFORT).toBe(AGENT_E2E_SETTINGS[AgentProvider.CODEX].effort)
    expect(env.LEAPMUX_COPILOT_DEFAULT_MODEL).toBe(AGENT_E2E_SETTINGS[AgentProvider.GITHUB_COPILOT].model)
    expect(env.LEAPMUX_COPILOT_DEFAULT_EFFORT).toBe(AGENT_E2E_SETTINGS[AgentProvider.GITHUB_COPILOT].effort)
  })
})
