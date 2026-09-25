import type { MockModelToolCall } from './mockModelScript'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import {
  askUserQuestionToolCall,
  backgroundBashToolCall,
  bashToolCall,
  blockGoalToolCall,
  completeGoalToolCall,
  createGoalToolCall,
  editToolCall,
  enterPlanModeToolCall,
  exitPlanModeFromFileToolCall,
  exitPlanModeToolCall,
  hasToolFor,
  kiroCompleteTodosToolCall,
  kiroSwitchToExecutionToolCall,
  mcpToolCall,
  mimoInteractiveBashToolCall,
  mimoTaskToolCall,
  mimoWorkflowToolCall,
  ohMyPiYieldToolCall,
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
  { operation: 'exitPlanModeFromFile', call: (p: AgentProvider) => exitPlanModeFromFileToolCall(p, 'call-1', [{ label: 'A', description: 'The first' }, { label: 'B', description: 'The second' }]) },
  { operation: 'askUserQuestion', call: (p: AgentProvider) => askUserQuestionToolCall(p, 'call-1', [{ question: 'Which?', header: 'Choice', options: [{ label: 'A', description: 'The first' }, { label: 'B', description: 'The second' }] }]) },
  { operation: 'spawnSubagent', call: (p: AgentProvider) => spawnSubagentToolCall(p, 'call-1', { description: 'Probe the subagent path', prompt: 'Reply with PONG.' }) },
  { operation: 'backgroundBash', call: (p: AgentProvider) => backgroundBashToolCall(p, 'call-1', 'sleep 60') },
  { operation: 'updateTodos', call: (p: AgentProvider) => updateTodosToolCall(p, 'call-1', [{ step: 'First', status: 'pending' }]) },
  { operation: 'createGoal', call: (p: AgentProvider) => createGoalToolCall(p, 'call-1', 'Ship the feature.') },
  { operation: 'completeGoal', call: (p: AgentProvider) => completeGoalToolCall(p, 'call-1') },
  { operation: 'blockGoal', call: (p: AgentProvider) => blockGoalToolCall(p, 'call-1', 'Blocked here.') },
  { operation: 'mcpTool', call: (p: AgentProvider) => mcpToolCall(p, 'call-1', { server: 'form_probe', tool: 'echo', input: { text: 'hi' } }) },
] as const

describe('TOOL_VOCABULARY', () => {
  it('answers for every provider the proto enum declares', () => {
    expect(PROVIDERS).toHaveLength(19)
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

describe('backgroundBashToolCall for Codewhale', () => {
  it('starts the job through task_shell_start, which is the one tool that takes it', () => {
    // `bash` refuses a `background` argument, so the flag form Claude uses would
    // fail the call and open no shell row.
    expect(backgroundBashToolCall(AgentProvider.CODEWHALE, 'call-1', 'sleep 60')).toEqual({ id: 'call-1', name: 'task_shell_start', arguments: { command: 'sleep 60' } })
  })
})

describe('spawnSubagentToolCall', () => {
  // `run` blocks until the child reports, so the parent's next scripted step reads
  // the report rather than a later notification turn.
  it('runs a MiMo Code subagent and waits for its report', () => {
    const call = spawnSubagentToolCall(AgentProvider.MIMO_CODE, 'call-1', { description: 'Probe it', prompt: 'Go.' })
    expect(call).toEqual({
      id: 'call-1',
      name: 'actor',
      arguments: { operation: { action: 'run', subagent_type: 'general', description: 'Probe it', prompt: 'Go.' } },
    })
  })

  it('names the collaboration namespace for Codex', () => {
    // A call that omits it comes back as `unsupported call: spawn_agent` in the
    // tool OUTPUT, so the turn continues and the registry simply stays empty.
    const call = spawnSubagentToolCall(AgentProvider.CODEX, 'call-1', { description: 'Probe it', prompt: 'Go.' })
    expect(call.name).toBe('spawn_agent')
    expect(call.namespace).toBe('collaboration')
  })

  it('reduces a description to an identifier for the three tools that take a name', () => {
    const request = { description: 'Probe the Subagent Path!', prompt: 'Go.' }
    expect(spawnSubagentToolCall(AgentProvider.CODEX, 'call-1', request).arguments)
      .toMatchObject({ task_name: 'probe_the_subagent_path' })
    // Copilot takes the identifier AND the description, which its schema
    // requires separately.
    expect(spawnSubagentToolCall(AgentProvider.GITHUB_COPILOT, 'call-1', request).arguments)
      .toMatchObject({ name: 'probe_the_subagent_path', description: 'Probe the Subagent Path!' })
    expect(spawnSubagentToolCall(AgentProvider.CODEWHALE, 'call-1', request).arguments)
      .toMatchObject({ name: 'probe_the_subagent_path' })
  })

  it('starts a Codewhale child through the one agent tool', () => {
    // `agent` holds every subagent action. A call without `action: start` asks
    // for a roster or a status, and no child runs.
    const call = spawnSubagentToolCall(AgentProvider.CODEWHALE, 'call-1', { description: 'Probe it', prompt: 'Go.' })
    expect(call.name).toBe('agent')
    expect(call.arguments).toEqual({ action: 'start', name: 'probe_it', type: 'explore', prompt: 'Go.' })
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

  // MiMo's schema spells a multi-select question `multiple` and takes no option
  // field beyond the label and the description.
  it('spells the MiMo Code question in MiMo\'s own fields', () => {
    const questions = [{ question: 'Which?', header: 'Choice', multiSelect: true, options: [{ label: 'A', description: 'The first', preview: '```\nA\n```' }] }]
    const call = askUserQuestionToolCall(AgentProvider.MIMO_CODE, 'call-1', questions)
    expect(call.name).toBe('question')
    expect(call.arguments?.questions).toEqual([{ question: 'Which?', header: 'Choice', options: [{ label: 'A', description: 'The first' }], multiple: true }])
    const single = [{ question: 'Which?', header: 'Choice', options: [{ label: 'A', description: 'The first' }] }]
    expect(askUserQuestionToolCall(AgentProvider.MIMO_CODE, 'call-1', single).arguments?.questions)
      .toMatchObject([{ multiple: false }])
  })
  it('gives each Codewhale question its own id and the runtime\'s field names', () => {
    // The answer repeats the question's id, so two questions must never share
    // one. An option's preview is not in the runtime's schema, so it stays out.
    const questions = [
      { question: 'Which?', header: 'Choice', options: [{ label: 'A', description: 'The first', preview: 'x' }] },
      { question: 'Which size?', header: 'Choice', multiSelect: true, options: [{ label: 'S', description: 'Small' }] },
    ]
    const call = askUserQuestionToolCall(AgentProvider.CODEWHALE, 'call-1', questions)
    expect(call.name).toBe('request_user_input')
    expect(call.arguments?.questions).toEqual([
      { id: 'question_1', header: 'Choice', question: 'Which?', options: [{ label: 'A', description: 'The first' }], allow_free_text: false, multi_select: false },
      { id: 'question_2', header: 'Choice', question: 'Which size?', options: [{ label: 'S', description: 'Small' }], allow_free_text: false, multi_select: true },
    ])
  })
})

describe('mimoInteractiveBashToolCall', () => {
  it('is the shell call with the interactive flag set', () => {
    const interactive = mimoInteractiveBashToolCall('call-1', 'read -p "Name? " n')
    const plain = bashToolCall(AgentProvider.MIMO_CODE, 'call-1', 'read -p "Name? " n')
    expect(interactive.name).toBe(plain.name)
    expect(interactive.arguments).toMatchObject({ command: 'read -p "Name? " n', interactive: true })
    expect(plain.arguments).not.toHaveProperty('interactive')
  })
})

describe('mimoTaskToolCall', () => {
  it('carries one operation of the to-do tool', () => {
    expect(mimoTaskToolCall('call-1', { action: 'create', summary: 'Write the parser' })).toEqual({
      id: 'call-1',
      name: 'task',
      arguments: { operation: { action: 'create', summary: 'Write the parser' } },
    })
    expect(mimoTaskToolCall('call-2', { action: 'done', id: 'T1' }).arguments).toEqual({ operation: { action: 'done', id: 'T1' } })
  })
})

describe('mcpToolCall', () => {
  // Each agent gives a Model Context Protocol tool a name of its own, built from the
  // server and the tool.
  it('calls a Model Context Protocol tool in the agent\'s own shape', () => {
    const request = { server: 'form_probe', tool: 'echo', input: { text: 'hi' } }
    expect(mcpToolCall(AgentProvider.GROK_BUILD, 'call-1', request)).toEqual({
      id: 'call-1',
      name: 'use_tool',
      arguments: { tool_name: 'form_probe__echo', tool_input: { text: 'hi' } },
    })
    expect(mcpToolCall(AgentProvider.PI, 'call-2', request)).toEqual({
      id: 'call-2',
      name: 'mcp',
      arguments: { tool: 'form_probe_echo', args: { text: 'hi' } },
    })
  })
})

describe('blockGoalToolCall', () => {
  it('ends the Codewhale goal loop through the runtime\'s own goal tool', () => {
    expect(blockGoalToolCall(AgentProvider.CODEWHALE, 'call-1', 'Stops here.')).toEqual({
      id: 'call-1',
      name: 'update_goal',
      arguments: { status: 'blocked', blocker: 'Stops here.' },
    })
  })
})

describe('updateTodosToolCall', () => {
  it('writes the whole Codewhale list with the runtime\'s field names', () => {
    const call = updateTodosToolCall(AgentProvider.CODEWHALE, 'call-1', [
      { step: 'First', status: 'completed' },
      { step: 'Second', status: 'in_progress' },
    ])
    expect(call.name).toBe('todo_write')
    expect(call.arguments).toEqual({ todos: [{ content: 'First', status: 'completed' }, { content: 'Second', status: 'in_progress' }] })
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

// Kimi Code's tool schemas set `additionalProperties: false`, so an extra field
// fails the call and a renamed one fails it too. These cases pin the shapes
// that the schemas in its model requests state.
describe('kimi code vocabulary', () => {
  const kimi = AgentProvider.KIMI_CODE

  it('offers no plan-carrying exit, because its exit call raises the plan file', () => {
    expect(hasToolFor(kimi, 'exitPlanMode')).toBe(false)
    expect(exitPlanModeFromFileToolCall(kimi, 'call-1', [
      { label: 'Option A', description: 'Simple' },
      { label: 'Option B', description: 'Robust' },
    ])).toEqual({
      id: 'call-1',
      name: 'ExitPlanMode',
      arguments: { options: [{ label: 'Option A', description: 'Simple' }, { label: 'Option B', description: 'Robust' }] },
    })
  })

  it('offers the plan-file exit for no other provider', () => {
    expect(PROVIDERS.filter(p => hasToolFor(p, 'exitPlanModeFromFile'))).toEqual([kimi])
  })

  it('writes the question in snake case and drops the preview the schema refuses', () => {
    const call = askUserQuestionToolCall(kimi, 'call-1', [{
      question: 'Which?',
      header: 'Choice',
      options: [{ label: 'A', description: 'The first', preview: '```\nA\n```' }, { label: 'B', description: 'The second' }],
    }])
    expect(call.arguments).toEqual({
      questions: [{
        question: 'Which?',
        header: 'Choice',
        options: [{ label: 'A', description: 'The first' }, { label: 'B', description: 'The second' }],
        multi_select: false,
      }],
    })
  })

  it('maps a completed step onto its done status', () => {
    expect(updateTodosToolCall(kimi, 'call-1', [
      { step: 'First', status: 'completed' },
      { step: 'Second', status: 'in_progress' },
      { step: 'Third', status: 'pending' },
    ]).arguments).toEqual({
      todos: [
        { title: 'First', status: 'done' },
        { title: 'Second', status: 'in_progress' },
        { title: 'Third', status: 'pending' },
      ],
    })
  })

  it('states a description for a background command, which the schema then requires', () => {
    expect(backgroundBashToolCall(kimi, 'call-1', 'sleep 60').arguments)
      .toMatchObject({ command: 'sleep 60', run_in_background: true, description: expect.any(String) })
    expect(bashToolCall(kimi, 'call-1', 'echo hi').arguments).toEqual({ command: 'echo hi' })
  })

  it('creates a goal and marks it complete through the goal tools', () => {
    expect(createGoalToolCall(kimi, 'call-1', 'Ship the feature.')).toEqual({ id: 'call-1', name: 'CreateGoal', arguments: { objective: 'Ship the feature.' } })
    expect(completeGoalToolCall(kimi, 'call-2')).toEqual({ id: 'call-2', name: 'UpdateGoal', arguments: { status: 'complete' } })
  })

  it('addresses a file by path in the edit, write, and read tools', () => {
    expect(editToolCall(kimi, 'call-1', { path: '/tmp/a.txt', before: 'a', after: 'b' }).arguments)
      .toEqual({ path: '/tmp/a.txt', old_string: 'a', new_string: 'b' })
    expect(writeToolCall(kimi, 'call-1', { path: '/tmp/a.txt', content: 'x' }).arguments).toEqual({ path: '/tmp/a.txt', content: 'x' })
    expect(readToolCall(kimi, 'call-1', '/tmp/a.txt').arguments).toEqual({ path: '/tmp/a.txt' })
  })
})

// Grok Build and Qwen Code state a spawn's foreground or background ALWAYS: Qwen
// 0.24 backgrounds an agent unless the call says otherwise, so a builder that left
// the flag out would let a CLI release pick the path a test drives.
describe('spawnSubagentToolCall background flag', () => {
  it.each([
    [AgentProvider.GROK_BUILD, 'background'],
    [AgentProvider.QWEN_CODE, 'run_in_background'],
  ])('states the flag both ways for provider %s', (provider, flag) => {
    const request = { description: 'Probe the subagent path', prompt: 'Reply with PONG.' }
    expect(spawnSubagentToolCall(provider, 'call-1', request).arguments).toMatchObject({ [flag]: false })
    expect(spawnSubagentToolCall(provider, 'call-1', { ...request, background: true }).arguments).toMatchObject({ [flag]: true })
  })

  it('leaves the flag to a provider that takes none', () => {
    const call = spawnSubagentToolCall(AgentProvider.CLAUDE_CODE, 'call-1', { description: 'd', prompt: 'p', background: true })
    expect(call.arguments).not.toHaveProperty('background')
    expect(call.arguments).not.toHaveProperty('run_in_background')
  })
})

describe('the Kiro tool vocabulary', () => {
  const kiro = AgentProvider.KIRO

  it('uses Kiro\'s own file tools and their argument names', () => {
    expect(editToolCall(kiro, 'c', { path: '/w/a', before: 'A', after: 'B' })).toEqual({ id: 'c', name: 'str_replace', arguments: { path: '/w/a', oldStr: 'A', newStr: 'B' } })
    expect(writeToolCall(kiro, 'c', { path: '/w/a', content: 'x' })).toEqual({ id: 'c', name: 'fs_write', arguments: { path: '/w/a', text: 'x' } })
    expect(readToolCall(kiro, 'c', '/w/a')).toEqual({ id: 'c', name: 'read_file', arguments: { path: '/w/a' } })
  })

  it('asks exactly one question, with titled options', () => {
    const question = { question: 'Which DB?', header: 'DB', options: [{ label: 'Postgres', description: 'pg' }] }
    expect(askUserQuestionToolCall(kiro, 'c', [question]).arguments).toEqual({ question: 'Which DB?', options: [{ title: 'Postgres', description: 'pg' }], reason: 'general-question' })
    expect(() => askUserQuestionToolCall(kiro, 'c', [question, question])).toThrow('exactly one question')
    expect(() => askUserQuestionToolCall(kiro, 'c', [])).toThrow('exactly one question')
  })

  it('refuses a multi-select question, which Kiro\'s user_input cannot ask', () => {
    const question = { question: 'Which DBs?', header: 'DB', options: [{ label: 'Postgres', description: 'pg' }], multiSelect: true }
    expect(() => askUserQuestionToolCall(kiro, 'c', [question])).toThrow('multi-select')
    expect(askUserQuestionToolCall(kiro, 'c', [{ ...question, multiSelect: false }]).name).toBe('user_input')
  })

  it('calls a Model Context Protocol tool by the name that Kiro gives it', () => {
    expect(mcpToolCall(kiro, 'kiro-mcp', { server: 'probe', tool: 'ask', input: {} })).toEqual({ id: 'kiro-mcp', name: 'mcp_probe_ask', arguments: {} })
    expect(mcpToolCall(kiro, 'c', { server: 'form_probe', tool: 'echo', input: { text: 'hi' } }).arguments).toEqual({ text: 'hi' })
  })

  it('offers no background command, which no Kiro spec runs', () => {
    expect(hasToolFor(kiro, 'backgroundBash')).toBe(false)
  })

  it('invokes a bundled agent with the description as its reason', () => {
    expect(spawnSubagentToolCall(kiro, 'c', { description: 'Find the file', prompt: 'Look.' }).arguments).toEqual({ name: 'context-gatherer', prompt: 'Look.', explanation: 'Find the file' })
  })

  it('creates the to-do list from the steps, and completes tasks by their ids', () => {
    expect(updateTodosToolCall(kiro, 'c', [{ step: 'One', status: 'pending' }]).arguments).toMatchObject({ command: 'create', tasks: [{ task_description: 'One' }] })
    expect(kiroCompleteTodosToolCall('c', ['1', '2']).arguments).toMatchObject({ command: 'complete', completed_task_ids: ['1', '2'] })
  })

  it('leaves the plan mode through its own switch, which is no plan approval', () => {
    expect(hasToolFor(kiro, 'exitPlanMode')).toBe(false)
    expect(kiroSwitchToExecutionToolCall('c', '1. Do it')).toEqual({ id: 'c', name: 'switch_to_execution', arguments: { plan: '1. Do it' } })
  })

  it('completes a goal step through send_message', () => {
    expect(completeGoalToolCall(kiro, 'c')).toEqual({ id: 'c', name: 'send_message', arguments: { message: 'The goal is verified.', severity: 'success' } })
  })

  it('blocks a goal through an error that a step sends', () => {
    expect(blockGoalToolCall(kiro, 'c', 'The repository is read-only.')).toEqual({ id: 'c', name: 'send_message', arguments: { message: 'The repository is read-only.', severity: 'error' } })
  })
})

describe('the Grok Build tool vocabulary', () => {
  // Grok reads its plan from the file the model wrote in plan mode, and its
  // `exit_plan_mode` schema declares no argument at all.
  it('sends no plan text with exit_plan_mode', () => {
    expect(exitPlanModeToolCall(AgentProvider.GROK_BUILD, 'call-1', 'The plan.').arguments).toEqual({})
  })

  it('spells the multi-select flag the way its schema does', () => {
    const call = askUserQuestionToolCall(AgentProvider.GROK_BUILD, 'call-1', [{ question: 'Which?', header: 'Choice', options: [{ label: 'A', description: 'a' }], multiSelect: true }])
    expect(call.arguments).toEqual({ questions: [{ question: 'Which?', options: [{ label: 'A', description: 'a' }], multi_select: true }] })
  })

  it('replaces the whole to-do list, so the scripted steps are the list', () => {
    const call = updateTodosToolCall(AgentProvider.GROK_BUILD, 'call-1', [{ step: 'First', status: 'pending' }, { step: 'Second', status: 'completed' }])
    expect(call.arguments).toEqual({ merge: false, todos: [{ id: '1', content: 'First', status: 'pending' }, { id: '2', content: 'Second', status: 'completed' }] })
  })

  it('states the description its shell tool requires', () => {
    expect(bashToolCall(AgentProvider.GROK_BUILD, 'call-1', 'ls').arguments).toEqual({ command: 'ls', description: 'Run the scripted command' })
    expect(backgroundBashToolCall(AgentProvider.GROK_BUILD, 'call-1', 'sleep 9').arguments).toMatchObject({ background: true })
  })
})

describe('the Qwen Code tool vocabulary', () => {
  it('uses its own file tools with an absolute path argument', () => {
    expect(readToolCall(AgentProvider.QWEN_CODE, 'call-1', '/p/a.txt')).toMatchObject({ name: 'read_file', arguments: { file_path: '/p/a.txt' } })
    expect(editToolCall(AgentProvider.QWEN_CODE, 'call-1', { path: '/p/a.txt', before: 'a', after: 'b' })).toMatchObject({ name: 'edit', arguments: { file_path: '/p/a.txt', old_string: 'a', new_string: 'b' } })
    expect(writeToolCall(AgentProvider.QWEN_CODE, 'call-1', { path: '/p/a.txt', content: 'x' })).toMatchObject({ name: 'write_file' })
  })

  it('carries the plan in exit_plan_mode and backgrounds a command with its own flag', () => {
    expect(exitPlanModeToolCall(AgentProvider.QWEN_CODE, 'call-1', 'The plan.').arguments).toEqual({ plan: 'The plan.' })
    expect(backgroundBashToolCall(AgentProvider.QWEN_CODE, 'call-1', 'sleep 9').arguments).toMatchObject({ is_background: true })
  })
})

// omp 18.2.11's own tool schemas. Only the specs 133 to 139 script these shapes, and
// CI runs no E2E, so these cases are the check that runs on every change.
describe('the Oh My Pi tool vocabulary', () => {
  it('yields a subagent\'s report as `data`', () => {
    expect(ohMyPiYieldToolCall('call-1', 'Done.')).toEqual({ id: 'call-1', name: 'yield', arguments: { data: 'Done.' } })
  })

  const omp = AgentProvider.OH_MY_PI

  it('addresses a file by path in the read, write and replace-mode edit tools', () => {
    expect(bashToolCall(omp, 'call-1', 'ls')).toEqual({ id: 'call-1', name: 'bash', arguments: { command: 'ls' } })
    expect(readToolCall(omp, 'call-1', 'notes.txt')).toEqual({ id: 'call-1', name: 'read', arguments: { path: 'notes.txt' } })
    expect(writeToolCall(omp, 'call-1', { path: 'a.txt', content: 'x\n' })).toEqual({ id: 'call-1', name: 'write', arguments: { path: 'a.txt', content: 'x\n' } })
    // The E2E profile sets `edit.mode: replace`. The default hashline edit addresses
    // lines by a hash of the file, which a scripted turn cannot compute.
    expect(editToolCall(omp, 'call-1', { path: 'a.ts', before: 'a', after: 'b' })).toEqual({ id: 'call-1', name: 'edit', arguments: { path: 'a.ts', old_string: 'a', new_string: 'b' } })
  })

  it('gives each question of an ask call its own id, and states multi for a multi-select alone', () => {
    // The question bridge matches each answer to its question by this id.
    const call = askUserQuestionToolCall(omp, 'call-1', [
      { question: 'Which?', header: 'Choice', options: [{ label: 'A', description: 'The first' }] },
      { question: 'Which sizes?', header: 'Sizes', multiSelect: true, options: [{ label: 'S', description: 'Small', preview: '```\nS\n```' }] },
    ])
    expect(call).toEqual({
      id: 'call-1',
      name: 'ask',
      arguments: {
        questions: [
          { id: 'q1', question: 'Which?', header: 'Choice', options: [{ label: 'A', description: 'The first' }] },
          { id: 'q2', question: 'Which sizes?', header: 'Sizes', options: [{ label: 'S', description: 'Small', preview: '```\nS\n```' }], multi: true },
        ],
      },
    })
  })

  it('spawns a subagent through a task batch, whose task name becomes the subagent\'s id', () => {
    // Spec 136 finds the registry row by this id. omp takes no background flag in
    // the call: its `async.enabled` setting decides, and the E2E profile turns it off.
    expect(spawnSubagentToolCall(omp, 'call-1', { description: 'Run the fruit task', prompt: 'List three fruits.', background: true })).toEqual({
      id: 'call-1',
      name: 'task',
      arguments: { context: 'Run the fruit task', tasks: [{ name: 'run_the_fruit_task', agent: 'task', task: 'List three fruits.' }] },
    })
  })

  it('opens a to-do list with init, in one phase, and leaves each status to omp', () => {
    expect(updateTodosToolCall(omp, 'call-1', [{ step: 'First', status: 'completed' }, { step: 'Second', status: 'pending' }])).toEqual({
      id: 'call-1',
      name: 'todo',
      arguments: { op: 'init', list: [{ phase: 'Plan', items: ['First', 'Second'] }] },
    })
  })

  it('offers no plan-mode, goal or background shell tool, which omp\'s RPC mode does not reach', () => {
    for (const operation of ['enterPlanMode', 'exitPlanMode', 'exitPlanModeFromFile', 'backgroundBash', 'createGoal', 'completeGoal', 'blockGoal'] as const)
      expect(hasToolFor(omp, operation), operation).toBe(false)
  })
})

describe('the Cline tool vocabulary', () => {
  const cline = AgentProvider.CLINE

  it('uses Cline\'s own tools and their argument names', () => {
    expect(bashToolCall(cline, 'c', 'echo hi')).toEqual({ id: 'c', name: 'run_commands', arguments: { commands: ['echo hi'] } })
    expect(editToolCall(cline, 'c', { path: '/w/a', before: 'A', after: 'B' })).toEqual({ id: 'c', name: 'editor', arguments: { path: '/w/a', old_text: 'A', new_text: 'B' } })
    // A write is the same editor call with no old text, which creates the file.
    expect(writeToolCall(cline, 'c', { path: '/w/a', content: 'x' })).toEqual({ id: 'c', name: 'editor', arguments: { path: '/w/a', new_text: 'x' } })
    expect(readToolCall(cline, 'c', '/w/a')).toEqual({ id: 'c', name: 'read_files', arguments: { files: [{ path: '/w/a' }] } })
  })

  it('leaves plan mode through the plan tool, which carries no plan', () => {
    expect(hasToolFor(cline, 'enterPlanMode')).toBe(false)
    expect(exitPlanModeToolCall(cline, 'c', 'The plan.')).toEqual({ id: 'c', name: 'switch_to_act_mode', arguments: {} })
  })

  it('asks exactly one question with 2 to 5 bare options', () => {
    const option = (label: string) => ({ label, description: `The ${label}` })
    const question = { question: 'Which DB?', header: 'DB', options: [option('Postgres'), option('SQLite')] }
    expect(askUserQuestionToolCall(cline, 'c', [question]).arguments).toEqual({ question: 'Which DB?', options: ['Postgres', 'SQLite'] })
    expect(() => askUserQuestionToolCall(cline, 'c', [question, question])).toThrow('exactly one question')
    expect(() => askUserQuestionToolCall(cline, 'c', [])).toThrow('exactly one question')
    expect(() => askUserQuestionToolCall(cline, 'c', [{ ...question, multiSelect: true }])).toThrow('no multi-select')
    expect(() => askUserQuestionToolCall(cline, 'c', [{ ...question, options: [option('Postgres')] }])).toThrow('2 to 5 options')
    expect(() => askUserQuestionToolCall(cline, 'c', [{ ...question, options: ['A', 'B', 'C', 'D', 'E', 'F'].map(option) }])).toThrow('2 to 5 options')
  })

  it('leads the subagent task with its description, which titles the registry row', () => {
    const call = spawnSubagentToolCall(cline, 'c', { description: 'Probe the subagent path', prompt: 'Reply with PONG.', background: true })
    expect(call.name).toBe('spawn_agent')
    expect(call.arguments).toEqual({ systemPrompt: expect.any(String), task: 'Probe the subagent path\n\nReply with PONG.' })
  })
})

// The Amp specs alone script these shapes, and CI runs no E2E, so these cases are
// the check that runs on every change.
describe('the Amp tool vocabulary', () => {
  const amp = AgentProvider.AMP

  it('runs a command through shell_command, and backgrounds it with a one-second wait', () => {
    expect(bashToolCall(amp, 'c', 'ls')).toEqual({ id: 'c', name: 'shell_command', arguments: { command: 'ls' } })
    expect(backgroundBashToolCall(amp, 'c', 'sleep 60')).toEqual({ id: 'c', name: 'shell_command', arguments: { command: 'sleep 60', timeout_ms: 1_000 } })
  })

  it('edits and writes through one apply_patch text, and reads by path', () => {
    expect(editToolCall(amp, 'c', { path: '/w/a.ts', before: 'old', after: 'new' })).toEqual({
      id: 'c',
      name: 'apply_patch',
      arguments: { patchText: '*** Begin Patch\n*** Update File: /w/a.ts\n@@\n-old\n+new\n*** End Patch' },
    })
    expect(writeToolCall(amp, 'c', { path: '/w/b.ts', content: 'x\ny' })).toEqual({
      id: 'c',
      name: 'apply_patch',
      arguments: { patchText: '*** Begin Patch\n*** Add File: /w/b.ts\n+x\n+y\n*** End Patch' },
    })
    expect(readToolCall(amp, 'c', '/w/a.ts')).toEqual({ id: 'c', name: 'Read', arguments: { path: '/w/a.ts' } })
  })

  it('spawns a subagent through Task, which Amp runs on its server', () => {
    expect(spawnSubagentToolCall(amp, 'c', { description: 'Probe it', prompt: 'Go.', background: true }))
      .toEqual({ id: 'c', name: 'Task', arguments: { description: 'Probe it', prompt: 'Go.' } })
  })

  it('offers no plan-mode, question, to-do, goal, or MCP tool', () => {
    for (const operation of ['enterPlanMode', 'exitPlanMode', 'exitPlanModeFromFile', 'askUserQuestion', 'updateTodos', 'createGoal', 'completeGoal', 'blockGoal', 'mcpTool'] as const)
      expect(hasToolFor(amp, operation), operation).toBe(false)
  })
})

/**
 * The patch text of an apply_patch call: Amp states it as an argument, and Codex
 * states it as the JSON string literal that its `exec` source passes.
 */
function patchTextOf(call: MockModelToolCall): string {
  const argument = call.arguments?.patchText
  if (typeof argument === 'string')
    return argument
  const literal = /tools\.apply_patch\(("(?:[^"\\]|\\.)*")\)/.exec(call.input ?? '')?.[1]
  if (literal === undefined)
    throw new Error(`The call ${call.name} states no apply_patch text`)
  return JSON.parse(literal) as string
}

// A patch marks each line of a hunk. An unmarked line is not part of the hunk, so
// apply_patch refuses the patch or applies another change.
describe('editToolCall patch text', () => {
  it.each([AgentProvider.AMP, AgentProvider.CODEX])('marks each line of a multi-line hunk for provider %s', (provider) => {
    const call = editToolCall(provider, 'c', { path: 'a.ts', before: 'one\ntwo', after: 'three\nfour' })
    expect(patchTextOf(call)).toBe('*** Begin Patch\n*** Update File: a.ts\n@@\n-one\n-two\n+three\n+four\n*** End Patch')
  })

  it.each([AgentProvider.AMP, AgentProvider.CODEX])('marks the one empty line of an empty side for provider %s', (provider) => {
    const call = editToolCall(provider, 'c', { path: 'a.ts', before: 'gone', after: '' })
    expect(patchTextOf(call)).toBe('*** Begin Patch\n*** Update File: a.ts\n@@\n-gone\n+\n*** End Patch')
  })

  it.each([AgentProvider.AMP, AgentProvider.CODEX])('writes the same added-file patch for provider %s', (provider) => {
    const call = writeToolCall(provider, 'c', { path: 'b.ts', content: 'x\ny' })
    expect(patchTextOf(call)).toBe('*** Begin Patch\n*** Add File: b.ts\n+x\n+y\n*** End Patch')
  })
})

// The MiMo Code specs alone script these shapes. Every input field is snake_case.
describe('the MiMo Code tool vocabulary', () => {
  const mimo = AgentProvider.MIMO_CODE

  it('addresses a file by file_path, and states the description that its shell tool requires', () => {
    expect(bashToolCall(mimo, 'c', 'ls')).toEqual({ id: 'c', name: 'bash', arguments: { command: 'ls', description: 'Run the scripted command' } })
    expect(editToolCall(mimo, 'c', { path: 'a.ts', before: 'a', after: 'b' })).toEqual({ id: 'c', name: 'edit', arguments: { file_path: 'a.ts', old_string: 'a', new_string: 'b' } })
    expect(writeToolCall(mimo, 'c', { path: 'a.ts', content: 'x' })).toEqual({ id: 'c', name: 'write', arguments: { file_path: 'a.ts', content: 'x' } })
    expect(readToolCall(mimo, 'c', 'a.ts')).toEqual({ id: 'c', name: 'read', arguments: { file_path: 'a.ts' } })
  })

  it('leaves plan mode through plan_exit, which carries no plan', () => {
    expect(exitPlanModeToolCall(mimo, 'c', 'The plan.')).toEqual({ id: 'c', name: 'plan_exit', arguments: {} })
    expect(hasToolFor(mimo, 'enterPlanMode')).toBe(false)
  })

  it('offers no whole-list to-do update, because its task tool acts on one item', () => {
    expect(hasToolFor(mimo, 'updateTodos')).toBe(false)
  })
})

describe('mimoWorkflowToolCall', () => {
  it('runs the script through the workflow tool', () => {
    expect(mimoWorkflowToolCall('c', 'await agent("Go.")')).toEqual({ id: 'c', name: 'workflow', arguments: { operation: 'run', script: 'await agent("Go.")' } })
  })
})

describe('the Codewhale tool vocabulary', () => {
  const codewhale = AgentProvider.CODEWHALE

  it('addresses a file by path, and edits through a list of replacements', () => {
    expect(bashToolCall(codewhale, 'c', 'ls')).toEqual({ id: 'c', name: 'bash', arguments: { command: 'ls' } })
    expect(editToolCall(codewhale, 'c', { path: 'a.ts', before: 'a', after: 'b' })).toEqual({ id: 'c', name: 'edit', arguments: { path: 'a.ts', edits: [{ oldText: 'a', newText: 'b' }] } })
    expect(writeToolCall(codewhale, 'c', { path: 'a.ts', content: 'x' })).toEqual({ id: 'c', name: 'write', arguments: { path: 'a.ts', content: 'x' } })
    expect(readToolCall(codewhale, 'c', 'a.ts')).toEqual({ id: 'c', name: 'read', arguments: { path: 'a.ts' } })
  })

  it('offers no plan-mode tool and no goal creation, which are thread settings', () => {
    for (const operation of ['enterPlanMode', 'exitPlanMode', 'createGoal', 'completeGoal'] as const)
      expect(hasToolFor(codewhale, operation), operation).toBe(false)
  })
})
