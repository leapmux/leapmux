import { describe, expect, it } from 'vitest'
import { antigravityAgentRequest, antigravityAgentResult } from './agent'
import { antigravityStep } from './toolCommon'

function invokeStep(args: Record<string, unknown>, invokeSubagent?: unknown) {
  return antigravityStep({
    type: 'CORTEX_STEP_TYPE_INVOKE_SUBAGENT',
    status: 'CORTEX_STEP_STATUS_DONE',
    metadata: {
      toolCall: {
        id: 'call-invoke-subagent',
        name: 'invoke_subagent',
        argumentsJson: JSON.stringify(args),
      },
    },
    ...(invokeSubagent !== undefined
      ? { invokeSubagent }
      : {}),
  })!
}

describe('antigravityAgentRequest', () => {
  it('extracts role, type, prompt, and optional conversation id', () => {
    const step = invokeStep({
      Role: 'Plan Verify Round 1 Verifier A',
      TypeName: 'research',
      Prompt: 'Verify the plan.',
      conversationId: 'child-1',
    })
    expect(antigravityAgentRequest(step)).toEqual({
      description: 'Plan Verify Round 1 Verifier A',
      agentType: 'research',
      prompt: 'Verify the plan.',
      promptFormat: 'markdown',
      registryKey: 'child-1',
    })
  })

  it('defaults the description to Subagent and omits registryKey when conversationId is absent', () => {
    const step = invokeStep({
      TypeName: 'self',
      Prompt: 'Inspect the build.',
    })
    expect(antigravityAgentRequest(step)).toEqual({
      description: 'Subagent',
      agentType: 'self',
      prompt: 'Inspect the build.',
      promptFormat: 'markdown',
    })
  })
})

describe('antigravityAgentResult', () => {
  it('reports a launched subagent as running and launched asynchronously', () => {
    const step = invokeStep(
      {
        Role: 'Plan Verify Round 1 Verifier A',
        TypeName: 'research',
        Prompt: 'Verify the plan.',
      },
      {
        subagents: [
          { typeName: 'research', role: 'Plan Verify Round 1 Verifier A' },
        ],
        results: [
          { conversationId: 'child-cascade-1' },
        ],
      },
    )
    const request = antigravityAgentRequest(step)
    expect(antigravityAgentResult(step, request)).toEqual({
      agents: [
        {
          description: 'Plan Verify Round 1 Verifier A',
          registryKey: 'child-cascade-1',
          agentId: 'child-cascade-1',
          statusLabel: 'launched asynchronously',
          outcome: 'running',
          metadata: [],
          body: '',
        },
      ],
    })
  })

  it('uses each subagent role by position when one step launches multiple subagents', () => {
    const step = invokeStep(
      {
        Role: 'Fallback Role',
        Prompt: 'Run verifiers.',
      },
      {
        subagents: [
          { typeName: 'research', role: 'Verifier A' },
          { typeName: 'research', role: '' },
          { typeName: 'research', role: 'Verifier C' },
        ],
        results: [
          { conversationId: 'child-a' },
          { conversationId: '' },
          { conversationId: 'child-c' },
        ],
      },
    )
    const request = antigravityAgentRequest(step)
    expect(antigravityAgentResult(step, request)).toEqual({
      agents: [
        {
          description: 'Verifier A',
          registryKey: 'child-a',
          agentId: 'child-a',
          statusLabel: 'launched asynchronously',
          outcome: 'running',
          metadata: [],
          body: '',
        },
        {
          description: 'Fallback Role',
          agentId: '',
          statusLabel: 'launched asynchronously',
          outcome: 'running',
          metadata: [],
          body: '',
        },
        {
          description: 'Verifier C',
          registryKey: 'child-c',
          agentId: 'child-c',
          statusLabel: 'launched asynchronously',
          outcome: 'running',
          metadata: [],
          body: '',
        },
      ],
    })
  })

  it('ignores non-object results while preserving position alignment with subagents', () => {
    const step = invokeStep(
      {
        Role: 'Fallback Role',
        Prompt: 'Run verifiers.',
      },
      {
        subagents: [
          { typeName: 'research', role: 'Ignored Entry' },
          { typeName: 'research', role: 'Verifier B' },
        ],
        results: [
          null,
          { conversationId: 'child-b' },
        ],
      },
    )
    const request = antigravityAgentRequest(step)
    expect(antigravityAgentResult(step, request)).toEqual({
      agents: [
        {
          description: 'Verifier B',
          registryKey: 'child-b',
          agentId: 'child-b',
          statusLabel: 'launched asynchronously',
          outcome: 'running',
          metadata: [],
          body: '',
        },
      ],
    })
  })

  it.each([undefined, null, 'invalid', {}])('returns an empty agent list when invokeSubagent.results is %j', (results) => {
    const step = invokeStep(
      { Role: 'Researcher', Prompt: 'Inspect.' },
      { results },
    )
    const request = antigravityAgentRequest(step)
    expect(antigravityAgentResult(step, request)).toEqual({ agents: [] })
  })
})
