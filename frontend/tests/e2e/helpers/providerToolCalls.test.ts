import { describe, expect, it } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import {
  askUserQuestionToolCall,
  backgroundBashToolCall,
  bashToolCall,
  editToolCall,
  enterPlanModeToolCall,
  exitPlanModeToolCall,
  hasToolFor,
  readToolCall,
  spawnSubagentToolCall,
  updateTodosToolCall,
  writeToolCall,
} from './providerToolCalls'

/**
 * Every provider this project supports. Read off the proto enum rather than
 * written out, so a provider added there fails this file until the vocabulary
 * table answers for it -- the runtime half of the `satisfies` clause, which
 * only covers the keys the table already spells.
 */
const PROVIDERS = (Object.values(AgentProvider) as unknown[])
  .filter((value): value is AgentProvider => typeof value === 'number' && value !== AgentProvider.UNSPECIFIED)

/**
 * Each operation, paired with a call that exercises it. The pair is what lets
 * one table drive both directions of the `hasToolFor` contract below.
 */
const OPERATIONS = [
  { operation: 'bash', call: (p: AgentProvider) => bashToolCall(p, 'call-1', 'echo hi') },
  { operation: 'edit', call: (p: AgentProvider) => editToolCall(p, 'call-1', { path: '/tmp/a.txt', before: 'a', after: 'b' }) },
  { operation: 'write', call: (p: AgentProvider) => writeToolCall(p, 'call-1', { path: '/tmp/a.txt', content: 'x\ny' }) },
  { operation: 'read', call: (p: AgentProvider) => readToolCall(p, 'call-1', '/tmp/a.txt') },
  { operation: 'enterPlanMode', call: (p: AgentProvider) => enterPlanModeToolCall(p, 'call-1') },
  { operation: 'exitPlanMode', call: (p: AgentProvider) => exitPlanModeToolCall(p, 'call-1', 'The plan.') },
  { operation: 'askUserQuestion', call: (p: AgentProvider) => askUserQuestionToolCall(p, 'call-1', [{ question: 'Which?', header: 'Choice', options: [{ label: 'A', description: 'The first' }, { label: 'B', description: 'The second' }] }]) },
  { operation: 'spawnSubagent', call: (p: AgentProvider) => spawnSubagentToolCall(p, 'call-1', { description: 'Probe the subagent path', prompt: 'Reply with PONG.' }) },
  { operation: 'backgroundBash', call: (p: AgentProvider) => backgroundBashToolCall(p, 'call-1', 'sleep 60') },
  { operation: 'updateTodos', call: (p: AgentProvider) => updateTodosToolCall(p, 'call-1', [{ step: 'First', status: 'pending' }]) },
] as const

describe('TOOL_VOCABULARY', () => {
  it('answers for every provider the proto enum declares', () => {
    expect(PROVIDERS).toHaveLength(10)
    for (const provider of PROVIDERS)
      expect(() => hasToolFor(provider, 'bash'), `provider ${provider}`).not.toThrow()
  })

  it('rejects a provider outside the enum by name', () => {
    expect(() => bashToolCall(9999 as AgentProvider, 'call-1', 'echo hi'))
      .toThrow('No tool vocabulary for AgentProvider 9999')
  })

  it('offers a shell tool for every provider', () => {
    // The one operation every agent has. A null here would strand the specs
    // that script a command, which is most of them.
    for (const provider of PROVIDERS.filter(p => p !== AgentProvider.CURSOR))
      expect(hasToolFor(provider, 'bash'), `provider ${provider}`).toBe(true)
  })
})

describe('hasToolFor', () => {
  // The two exported surfaces must agree: a caller that asks first must never
  // be surprised by the builder, and a caller that does not ask must get a
  // named error rather than a call the agent ignores.
  for (const { operation, call } of OPERATIONS) {
    it(`agrees with the builder for ${operation}`, () => {
      for (const provider of PROVIDERS) {
        const offered = hasToolFor(provider, operation)
        if (offered) {
          expect(() => call(provider), `provider ${provider} offers ${operation}`).not.toThrow()
          continue
        }
        expect(() => call(provider), `provider ${provider} lacks ${operation}`)
          .toThrow(`AgentProvider ${provider} offers no`)
      }
    })
  }
})

describe('bashToolCall', () => {
  it('carries the given id and a non-empty tool name for every provider', () => {
    for (const provider of PROVIDERS.filter(p => hasToolFor(p, 'bash'))) {
      const call = bashToolCall(provider, 'call-42', 'echo hi')
      expect(call.id, `provider ${provider}`).toBe('call-42')
      expect(call.name.length, `provider ${provider}`).toBeGreaterThan(0)
      // Exactly one payload form. A call with neither says nothing, and a call
      // with both leaves the protocol to pick, which differs per protocol.
      expect(call.arguments === undefined, `provider ${provider}`).toBe(call.input !== undefined)
    }
  })

  it('puts the command in the arguments where the provider takes JSON', () => {
    expect(bashToolCall(AgentProvider.CLAUDE_CODE, 'call-1', 'echo hi').arguments)
      .toMatchObject({ command: 'echo hi' })
  })

  it('puts JavaScript source in the input for Codex, not JSON arguments', () => {
    // Codex's `exec` is an OpenAI CUSTOM tool, so its payload is source text.
    // A builder that returned `arguments` would reach the CLI as an empty call.
    const call = bashToolCall(AgentProvider.CODEX, 'call-1', 'echo hi')
    expect(call.name).toBe('exec')
    expect(call.arguments).toBeUndefined()
    expect(call.input).toContain('exec_command')
    expect(call.input).toContain(JSON.stringify('echo hi'))
  })
})

describe('backgroundBashToolCall', () => {
  it('sets the flag that detaches the command, which the foreground call omits', () => {
    // Claude's background shell is the SAME tool plus one flag, so the two
    // builders differ by exactly that key. A copy that lost it would open a
    // blocking row and the registry would never gain a shell entry.
    const background = backgroundBashToolCall(AgentProvider.CLAUDE_CODE, 'call-1', 'sleep 60')
    const foreground = bashToolCall(AgentProvider.CLAUDE_CODE, 'call-1', 'sleep 60')
    expect(background.name).toBe(foreground.name)
    expect(background.arguments).toMatchObject({ run_in_background: true })
    expect(foreground.arguments).not.toHaveProperty('run_in_background')
  })
})

describe('spawnSubagentToolCall', () => {
  it('names the collaboration namespace for Codex', () => {
    // A call that omits it comes back as `unsupported call: spawn_agent` in the
    // tool OUTPUT, so the turn continues and the registry simply stays empty.
    const call = spawnSubagentToolCall(AgentProvider.CODEX, 'call-1', { description: 'Probe it', prompt: 'Go.' })
    expect(call.name).toBe('spawn_agent')
    expect(call.namespace).toBe('collaboration')
  })

  it('reduces a description to an identifier for the two tools that take a name', () => {
    const request = { description: 'Probe the Subagent Path!', prompt: 'Go.' }
    expect(spawnSubagentToolCall(AgentProvider.CODEX, 'call-1', request).arguments)
      .toMatchObject({ task_name: 'probe_the_subagent_path' })
    // Copilot takes the identifier AND the description, which its schema
    // requires separately.
    expect(spawnSubagentToolCall(AgentProvider.GITHUB_COPILOT, 'call-1', request).arguments)
      .toMatchObject({ name: 'probe_the_subagent_path', description: 'Probe the Subagent Path!' })
  })

  it('falls back to a usable name when the description reduces to nothing', () => {
    // Empty and punctuation-only both strip to an empty string. A provider that
    // refuses an empty name refuses the spawn, and the symptom appears far from
    // here as a registry row that never opened.
    for (const description of ['', '!!!', '  ', '---']) {
      expect(spawnSubagentToolCall(AgentProvider.CODEX, 'call-1', { description, prompt: 'Go.' }).arguments)
        .toMatchObject({ task_name: 'scripted_subagent' })
    }
  })

  it('keeps no leading or trailing underscore on the identifier', () => {
    expect(spawnSubagentToolCall(AgentProvider.CODEX, 'call-1', { description: '  Probe it.  ', prompt: 'Go.' }).arguments)
      .toMatchObject({ task_name: 'probe_it' })
  })
})

describe('askUserQuestionToolCall', () => {
  it('states multiSelect rather than omitting it, which Claude\'s schema requires', () => {
    const questions = [{ question: 'Which?', header: 'Choice', options: [{ label: 'A', description: 'The first' }] }]
    const call = askUserQuestionToolCall(AgentProvider.CLAUDE_CODE, 'call-1', questions)
    expect(call.arguments?.questions).toEqual([{ ...questions[0], multiSelect: false }])
  })

  it('keeps an explicit multiSelect', () => {
    const questions = [{ question: 'Which?', header: 'Choice', multiSelect: true, options: [{ label: 'A', description: 'The first' }] }]
    const call = askUserQuestionToolCall(AgentProvider.CLAUDE_CODE, 'call-1', questions)
    expect(call.arguments?.questions).toEqual([questions[0]])
  })
})

describe('editToolCall', () => {
  it('carries both sides of the hunk for every provider that offers an edit tool', () => {
    for (const provider of PROVIDERS.filter(p => hasToolFor(p, 'edit'))) {
      const call = editToolCall(provider, 'call-1', { path: '/tmp/a.txt', before: 'ALPHA', after: 'BETA' })
      const payload = call.input ?? JSON.stringify(call.arguments)
      expect(payload, `provider ${provider} keeps the old text`).toContain('ALPHA')
      expect(payload, `provider ${provider} keeps the new text`).toContain('BETA')
      expect(payload, `provider ${provider} keeps the path`).toContain('/tmp/a.txt')
    }
  })
})
