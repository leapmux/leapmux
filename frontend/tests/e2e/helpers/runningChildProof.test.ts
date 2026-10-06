import type { MockModelMatcher, MockModelRequestRecord } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { ManagedNativeScenarioContext, NativeScenarioContext } from './nativeScenario'
import type { NativeChildProfile, NativeChildScript, NativeChildScriptContext, NativeChildTask, ProfiledNativeChild, RunningChildOptions, RunningNativeChild } from './runningChildProof'
import { describe, expect, it } from 'vitest'
import { fakeLocator } from '~/test-support/fakeLocator'
import { AgentProvider, BackgroundTaskKind, BackgroundTaskStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { MOCK_MODEL_IDS } from './mockAgentEnvironment'
import { mockScenarioPrompt, readScenarioStatus, registerMockModelScenario } from './mockModelScenario'
import { matchesRequest, parseScenarioSpec } from './mockModelScript'
import { createMockModelServer } from './mockModelServer'
import { readToolCall, spawnSubagentToolCall } from './providerToolCalls'
import {
  childTaskAnywhere,
  childTaskAtStart,
  expectRunningChildCompletes,
  HELD_NATIVE_CHILD_DESCRIPTION,
  heldChildFinalRequest,
  heldChildIdentity,
  heldChildOptions,
  NATIVE_CHILD_FINAL_REPLY,
  nativeChildRuleId,
  nativeChildScriptContext,
  profiledChildOptions,
  runningNativeChildRules,
  selectRunningChildTask,
} from './runningChildProof'

describe('selectRunningChildTask', () => {
  const old: NativeChildTask = { id: 'old-native-task', kind: BackgroundTaskKind.SUBAGENT, childAgentId: 'old-worker-child', parentAgentId: 'actual-parent', title: 'leapmux-e2e-child', status: BackgroundTaskStatus.COMPLETED }
  const current: NativeChildTask = { id: 'current-native-task', kind: BackgroundTaskKind.SUBAGENT, childAgentId: 'current-worker-child', parentAgentId: 'actual-parent', title: 'leapmux-e2e-child', status: BackgroundTaskStatus.RUNNING }
  const selection = { parentId: 'actual-parent', rootAgentId: 'actual-parent', previousChildIds: new Set([old.childAgentId]), rowText: 'leapmux-e2e-child' }
  it('selects the exact new running child when an old native child has the same title', () => {
    expect(selectRunningChildTask([old, current], selection)).toEqual(current)
  })
  it('uses the provider-owned exact native task identity instead of the first matching title', () => {
    expect(selectRunningChildTask([old, current], { ...selection, taskId: current.id })).toEqual(current)
  })
  it('does not select a sibling from another native parent', () => {
    expect(selectRunningChildTask([{ ...current, parentAgentId: 'another-parent' }, current], selection)).toEqual(current)
  })
  it.each([
    { ...current, childAgentId: '' },
    { ...current, status: BackgroundTaskStatus.COMPLETED },
    { ...current, kind: BackgroundTaskKind.SHELL },
    { ...current, parentAgentId: 'another-parent' },
  ])('refuses a row without this actual running child identity: %j', (task) => {
    expect(selectRunningChildTask([task], selection)).toBeUndefined()
  })
  it('rejects ambiguous native task identities instead of choosing one child', () => {
    expect(() => selectRunningChildTask([current, { ...current, childAgentId: 'another-worker-child' }], { ...selection, taskId: current.id })).toThrow()
  })
})

function rules(gate: string) {
  return runningNativeChildRules({
    gate,
    child: {
      matcher: { user: `Native child ${gate}` },
      tool: readToolCall(AgentProvider.PI, `read-${gate}`, `/project/${gate}.txt`),
    },
  })
}

describe('NativeChildScript', () => {
  it('refuses a final matcher without the tool turn that it would follow', () => {
    // @ts-expect-error A final matcher selects the turn after the tool turn, so a script without a tool cannot state one.
    const script: NativeChildScript = { matcher: { user: 'The native task' }, finalMatcher: { user: 'The actual tool result' } }
    expect(script.matcher).toEqual({ user: 'The native task' })
  })

  it('accepts a final matcher together with the tool turn', () => {
    const script: NativeChildScript = {
      matcher: { user: 'The native task' },
      tool: readToolCall(AgentProvider.PI, 'native-read', '/project/native.txt'),
      finalMatcher: { user: 'The actual tool result' },
    }
    expect(script.finalMatcher).toEqual({ user: 'The actual tool result' })
  })
})

describe('runningNativeChildRules', () => {
  it('matches the final child reply against the actual tool result', () => {
    const child: NativeChildScript = {
      matcher: { user: 'The actual child task.' },
      tool: readToolCall(AgentProvider.DEEPSEEK_HARNESS, 'native-read', '/project/native.txt'),
      finalMatcher: { user: 'NATIVE_CHILD_FILE77' },
    }
    const childRules = runningNativeChildRules({ gate: 'after-native-read', child })
    expect(childRules[0]?.when).toBe(child.matcher)
    expect(childRules[0]?.respond.toolCalls).toEqual([child.tool])
    expect(childRules[1]?.when).toBe(child.finalMatcher)
    expect(childRules[1]?.respond.gate).toBe('after-native-read')
    expect(childRules[1]?.once).toBe(true)
  })

  it('holds the default final reply when the script states no final step', () => {
    const [final] = runningNativeChildRules({ gate: 'default-final', child: { matcher: { user: 'The actual child task.' } } })
    expect(final?.respond).toEqual({ text: NATIVE_CHILD_FINAL_REPLY, gate: 'default-final' })
  })

  it('applies the gate to a scripted final step, so a step cannot drop the hold', () => {
    const [final] = runningNativeChildRules({ gate: 'scripted-final', child: { matcher: { user: 'The actual child task.' }, finalStep: { text: 'The report.', gate: 'another-gate' } } })
    expect(final?.respond).toEqual({ text: 'The report.', gate: 'scripted-final' })
  })

  it('rejects an unrelated last-user turn that quotes the native notification tag over HTTP', async () => {
    const server = await createMockModelServer({ models: MOCK_MODEL_IDS })
    const id = 'native-child-notice-ownership'
    try {
      const childRules = runningNativeChildRules({ gate: 'native-notice-ownership' })
      expect(childRules).toEqual([])
      await registerMockModelScenario(server.url, id, { steps: [{ text: 'The scripted content turn completed.' }], rules: childRules })
      const send = (content: string) => fetch(`${server.url}/v1/chat/completions`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ model: 'mock-model', stream: false, messages: [{ role: 'user', content }] }),
      })
      const queued = await send(mockScenarioPrompt(id, 'Complete the scripted content turn.'))
      expect(queued.status).toBe(200)
      await queued.arrayBuffer()
      expect(await readScenarioStatus(server.url, id)).toMatchObject({ nextStep: 1, stepCount: 1, unexpectedRequests: [] })

      const unrelated = mockScenarioPrompt(id, 'Explain the literal "<task-notification>" text. This request does not report a child result.')
      const response = await send(unrelated)
      await response.arrayBuffer()
      const status = await readScenarioStatus(server.url, id)
      expect(response.status).toBe(409)
      expect(status.unexpectedRequests).toMatchObject([{
        protocol: 'openai-chat-completions',
        path: '/v1/chat/completions',
        reason: 'The scenario has no remaining scripted answer.',
        body: { messages: [{ role: 'user', content: unrelated }] },
      }])
      expect(status.unexpectedRequests).toHaveLength(1)
      expect(status.requests).toHaveLength(1)
    }
    finally {
      await server.close()
    }
  })

  it('supports two sequential native children in one scenario without duplicate rule IDs', () => {
    const combined = [...rules('first-child'), ...rules('second-child')]
    expect(() => parseScenarioSpec({ steps: [], rules: combined })).not.toThrow()
    expect(new Set(combined.map(rule => rule.name)).size).toBe(combined.length)
  })

  it('holds the actual final child response only once', () => {
    const final = rules('child-final').find(rule => rule.respond.gate === 'child-final')
    expect(final).toBeDefined()
    expect(final?.once).toBe(true)
  })

  it('keeps repeated provider rule IDs distinct for sequential native children', () => {
    const original = { name: 'provider notification', when: { user: '^Actual provider notification' }, respond: { text: 'The native notification completed.' }, once: true }
    const build = (gate: string) => runningNativeChildRules({ gate, rules: [original] })
    const combined = [...build('first-provider-child'), ...build('second-provider-child')]
    expect(() => parseScenarioSpec({ steps: [], rules: combined })).not.toThrow()
    expect(combined.filter(rule => rule.when === original.when).map(rule => rule.name)).toHaveLength(2)
    expect(new Set(combined.filter(rule => rule.when === original.when).map(rule => rule.name)).size).toBe(2)
    expect(original.name).toBe('provider notification')
    expect(combined.filter(rule => rule.when === original.when).map(rule => rule.respond)).toEqual([original.respond, original.respond])
    expect(combined.filter(rule => rule.when === original.when).every(rule => rule.once)).toBe(true)
  })

  it('preserves the provider matcher, final tool, and native notification replay behavior', () => {
    const original = rules('source-preservation')
    expect(original[0]?.when).toEqual({ user: 'Native child source-preservation' })
    expect(original[0]?.once).toBe(true)
    expect(original).toHaveLength(2)
    expect(original.some(rule => rule.when.user === '<task-notification>')).toBe(false)
    const notice = { name: 'provider-owned completion', when: { user: '^The actual provider completion$' }, respond: { text: 'The actual completion arrived.' } }
    const explicit = runningNativeChildRules({ gate: 'explicit-provider-notice', rules: [notice] })
    expect(explicit).toHaveLength(1)
    expect(explicit[0]?.when).toBe(notice.when)
    expect(explicit[0]?.respond).toBe(notice.respond)
    expect(explicit[0]?.once).toBeUndefined()
  })
})

describe('nativeChildRuleId', () => {
  it('keeps the original rule text after the unique child prefix', () => {
    expect(nativeChildRuleId('native-child', 'the actual native rule')).toBe('[native-child] the actual native rule')
  })

  it.each(['', 'invalid gate', '[ambiguous]', 'a'.repeat(65)])('rejects an invalid completion control ID: %s', (gate) => {
    expect(() => nativeChildRuleId(gate, 'native rule')).toThrow('Model gate must use')
  })
})

describe('heldChildFinalRequest', () => {
  /** One recorded request that the rule `rule` answered. */
  function record(rule: string | undefined, text: string): MockModelRequestRecord {
    return { protocol: 'openai-chat-completions', path: '/v1/chat/completions', ...(rule === undefined ? {} : { rule }), body: { text } }
  }
  const [toolRule, finalRule] = rules('held-request').map(rule => rule.name)
  const [, otherFinalRule] = rules('other-child').map(rule => rule.name)

  it('returns the request of the final rule of the child with the gate', () => {
    if (!toolRule || !finalRule || !otherFinalRule)
      throw new Error('The child script built no tool rule and final rule.')
    const final = record(finalRule, 'the final request')
    const requests = [record(toolRule, 'the tool request'), record(otherFinalRule, 'another child'), record(undefined, 'a step'), final]
    expect(heldChildFinalRequest(requests, 'held-request')).toBe(final)
  })

  it('refuses a child whose final request is absent', () => {
    if (!toolRule)
      throw new Error('The child script built no tool rule.')
    expect(() => heldChildFinalRequest([record(toolRule, 'the tool request')], 'held-request')).toThrow('has no recorded final model request')
  })

  it('refuses a final rule that answered twice, because the rule answers once', () => {
    if (!finalRule)
      throw new Error('The child script built no final rule.')
    expect(() => heldChildFinalRequest([record(finalRule, 'first'), record(finalRule, 'second')], 'held-request')).toThrow('has 2 final model requests')
  })
})

/** A script context that marks each prompt, as a model script does. */
const SCRIPT: NativeChildScriptContext = {
  provider: AgentProvider.LETTA,
  prompt: text => `${text}\nSCENARIO_MARK`,
  textStep: text => ({ text: `answer: ${text}` }),
}

describe('nativeChildScriptContext', () => {
  function context(textStep?: NativeScenarioContext['textStep']): NativeScenarioContext {
    return {
      provider: AgentProvider.DIRAC,
      // The script context reads only `prompt`. Any other model access fails the test.
      modelScript: Object.assign({} as ModelScript, { prompt: (text: string) => `${text}\nMARKED` }),
      ...(textStep ? { textStep } : {}),
      get page(): never {
        throw new Error('The script context must not read the page.')
      },
    }
  }

  it('marks a prompt through the model script and keeps the provider', () => {
    const script = nativeChildScriptContext(context())
    expect(script.provider).toBe(AgentProvider.DIRAC)
    expect(script.prompt('The task.')).toBe('The task.\nMARKED')
  })

  it('answers through the text step of the provider, or with plain text without one', () => {
    expect(nativeChildScriptContext(context(text => ({ toolCalls: [{ id: 'respond', name: 'respond', arguments: { text } }] }))).textStep('Done.'))
      .toEqual({ toolCalls: [{ id: 'respond', name: 'respond', arguments: { text: 'Done.' } }] })
    expect(nativeChildScriptContext(context()).textStep('Done.')).toEqual({ text: 'Done.' })
  })
})

describe('heldChildIdentity', () => {
  it('gives each child a new task, spawn call ID, gate, and description', () => {
    const first = heldChildIdentity(SCRIPT)
    const second = heldChildIdentity(SCRIPT)
    expect(first.task).toMatch(/^NATIVECHILDTASK[0-9a-f]{32} report one word\.$/)
    expect(first.task).not.toBe(second.task)
    expect(first.spawn.id).not.toBe(second.spawn.id)
    expect(first.gate).not.toBe(second.gate)
    expect(first.description).not.toBe(second.description)
  })

  it('keeps the description within a provider label of 32 characters', () => {
    const { description } = heldChildIdentity(SCRIPT)
    expect(description).toMatch(/^Native held child [0-9a-f]{8}$/)
    expect(description.length).toBeLessThanOrEqual(32)
  })

  it('builds the spawn call of the provider with the marked prompt and a valid gate', () => {
    const child = heldChildIdentity(SCRIPT)
    expect(child.prompt).toBe(`${child.task}\nSCENARIO_MARK`)
    expect(child.spawn).toEqual(spawnSubagentToolCall(AgentProvider.LETTA, child.spawn.id, { description: child.description, prompt: child.prompt }))
    expect(() => nativeChildRuleId(child.gate, 'a rule')).not.toThrow()
  })

  it('uses the task and the spawn fields that the provider supplies', () => {
    const child = heldChildIdentity({ ...SCRIPT, provider: AgentProvider.DROID }, { task: 'The provider task.', spawn: { background: true } })
    expect(child.task).toBe('The provider task.')
    expect(child.spawn).toEqual(spawnSubagentToolCall(AgentProvider.DROID, child.spawn.id, { description: child.description, prompt: child.prompt, background: true }))
  })

  it.each(['', '   '])('refuses an empty task: %j', (task) => {
    expect(() => heldChildIdentity(SCRIPT, { task })).toThrow('task that is not empty')
  })
})

/** Decide whether `matcher` selects a child turn whose last user text is `userText`. */
function selectsTurn(matcher: MockModelMatcher, userText: string): boolean {
  return matchesRequest(matcher, { protocol: 'openai-chat-completions', userText, systemText: '', body: {} })
}

describe('childTaskAtStart', () => {
  it('selects a turn that opens with the task, and refuses a turn that only quotes it', () => {
    const matcher = childTaskAtStart('NATIVECHILDTASK1 report one word.')
    expect(selectsTurn(matcher, 'NATIVECHILDTASK1 report one word.\n\nSCENARIO_MARK')).toBe(true)
    expect(selectsTurn(matcher, 'The parent quotes NATIVECHILDTASK1 report one word.')).toBe(false)
  })

  it('reads regular expression syntax in the task as plain text', () => {
    const matcher = childTaskAtStart('Read a.b (once) [now]+?')
    expect(selectsTurn(matcher, 'Read a.b (once) [now]+?')).toBe(true)
    expect(selectsTurn(matcher, 'Read aXb (once) [now]+?')).toBe(false)
  })

  it.each(['', '  '])('refuses an empty task: %j', (task) => {
    expect(() => childTaskAtStart(task)).toThrow('task that is not empty')
  })
})

describe('childTaskAnywhere', () => {
  it('selects a turn that holds the task after text of its own', () => {
    const matcher = childTaskAnywhere('NATIVECHILDTASK2 report one word.')
    expect(selectsTurn(matcher, '<context>Provider text.</context>\nNATIVECHILDTASK2 report one word.')).toBe(true)
    expect(selectsTurn(matcher, 'NATIVECHILDTASK2 report another word.')).toBe(false)
  })

  it('reads regular expression syntax in the task as plain text', () => {
    expect(selectsTurn(childTaskAnywhere('a.b'), 'aXb')).toBe(false)
  })

  it.each(['', '  '])('refuses an empty task: %j', (task) => {
    expect(() => childTaskAnywhere(task)).toThrow('task that is not empty')
  })
})

describe('profiledChildOptions', () => {
  /** A scenario context that the option builder may read only for the provider, the prompt, and the text step. */
  function context(): ManagedNativeScenarioContext {
    return {
      provider: AgentProvider.KILO,
      providerAgent: { provider: AgentProvider.KILO, prefix: 'native-e2e' },
      workspaceId: 'profiled-child',
      modelScript: Object.assign({} as ModelScript, { prompt: (text: string) => `${text}\nMARKED` }),
      leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' },
      get page(): never {
        throw new Error('The option builder must not read the page.')
      },
    }
  }

  function child(): ProfiledNativeChild {
    return { ...heldChildIdentity(nativeChildScriptContext(context())), report: 'NATIVECHILDREPORT1' }
  }

  it('matches the task through the profile, holds the unique report, and answers the parent once', () => {
    const tasks: string[] = []
    const profile: NativeChildProfile = { childTask: (task) => {
      tasks.push(task)
      return { user: `matcher for ${task}` }
    }, rowTitleHoldsDescription: false }
    const held = child()
    const options = profiledChildOptions(context(), profile, held)
    expect(tasks).toEqual([held.task])
    expect(options).toEqual({
      spawn: held.spawn,
      gate: held.gate,
      child: { matcher: { user: `matcher for ${held.task}` }, finalStep: { text: 'NATIVECHILDREPORT1' } },
      parentSteps: [{ toolCalls: [held.spawn] }, { text: 'The native parent received its child report.' }],
      allowExistingRows: false,
    })
  })

  it('adds the tool turn, the earlier rows, and the row title when the caller and the profile state them', () => {
    const tool = readToolCall(AgentProvider.KILO, 'native-child-read', '/project/native.txt')
    const held = child()
    const options = profiledChildOptions(context(), { childTask: childTaskAtStart, rowTitleHoldsDescription: true }, held, { childTool: tool, allowExistingRows: true })
    expect(options.child).toEqual({ matcher: childTaskAtStart(held.task), tool, finalStep: { text: 'NATIVECHILDREPORT1' } })
    expect(options.allowExistingRows).toBe(true)
    expect(options.rowText).toBe(held.description)
  })

  it('forwards the context and the child to each callback of the profile', async () => {
    const calls: string[] = []
    const scenario = context()
    const held = child()
    const profile: NativeChildProfile = {
      childTask: childTaskAtStart,
      rowTitleHoldsDescription: false,
      prepare: async (received) => {
        expect(received).toBe(scenario)
        calls.push('prepare')
      },
      beforeRelease: async (received, receivedChild) => {
        expect(received).toBe(scenario)
        expect(receivedChild).toBe(held)
        calls.push('beforeRelease')
      },
      resolveTaskId: async (received, parentId, receivedChild) => {
        expect(received).toBe(scenario)
        expect(receivedChild).toBe(held)
        calls.push(`resolveTaskId ${parentId}`)
        return 'native-task'
      },
    }
    const options = profiledChildOptions(scenario, profile, held)
    await options.prepare?.()
    await options.beforeRelease?.()
    expect(await options.resolveTaskId?.('native-parent')).toBe('native-task')
    expect(calls).toEqual(['prepare', 'beforeRelease', 'resolveTaskId native-parent'])
  })

  it('leaves out each callback that the profile does not state', () => {
    const options = profiledChildOptions(context(), { childTask: childTaskAtStart, rowTitleHoldsDescription: false }, child())
    expect(options.prepare).toBeUndefined()
    expect(options.beforeRelease).toBeUndefined()
    expect(options.resolveTaskId).toBeUndefined()
  })
})

describe('heldChildOptions', () => {
  it('holds the child answer, answers the parent once, and selects the child by its description', () => {
    const child = heldChildIdentity(SCRIPT)
    const options: RunningChildOptions = heldChildOptions(SCRIPT, child)
    expect(options).toEqual({
      spawn: child.spawn,
      gate: child.gate,
      child: { matcher: { user: child.task }, finalStep: { text: 'answer: NATIVECHILDCOMPLETE' } },
      parentSteps: [{ toolCalls: [child.spawn] }, { text: 'answer: The native parent completed.' }],
      allowExistingRows: false,
      rowText: child.description,
    })
  })

  it('replaces each default that the provider overrides and keeps the identity', () => {
    const child = heldChildIdentity(SCRIPT)
    const script: NativeChildScript = { matcher: { system: 'The child system prompt', body: child.task } }
    const options = heldChildOptions(SCRIPT, child, { child: script, allowExistingRows: true, rules: [] })
    expect(options.child).toBe(script)
    expect(options.allowExistingRows).toBe(true)
    expect(options.rules).toEqual([])
    expect(options.spawn).toBe(child.spawn)
    expect(options.gate).toBe(child.gate)
    expect(options.rowText).toBe(child.description)
  })

  it('starts each description with the shared held child description', () => {
    expect(heldChildIdentity(SCRIPT).description.startsWith(`${HELD_NATIVE_CHILD_DESCRIPTION} `)).toBe(true)
  })
})

describe('expectRunningChildCompletes', () => {
  /** A child whose row passes each check and logs it, with each reload of its page and the finish of the child. */
  function child(log: string[], ids: { childId: string, parentId: string } = { childId: 'child', parentId: 'parent' }): RunningNativeChild {
    const row = fakeLocator((check) => {
      log.push(`${check.expression}${check.expressionArg ? ` ${String(check.expressionArg)}` : ''}=${check.expectedText?.[0]?.string ?? ''}`)
      return true
    }, { page: () => ({ reload: async () => log.push('reload') }) })
    return { row, ...ids, finish: async () => {
      log.push('finish')
    } }
  }

  it('requires the running row, finishes the child, then requires the completed row', async () => {
    const log: string[] = []
    await expectRunningChildCompletes(child(log), { rowText: 'Native held child' })
    expect(log).toEqual([
      'to.have.text=Native held child',
      'to.have.attribute.value data-kind=subagent',
      'to.have.attribute.value data-status=running',
      'finish',
      'to.have.attribute.value data-status=completed',
    ])
  })

  it('checks no text without a row text, and requires the completed row again after a reload', async () => {
    const log: string[] = []
    await expectRunningChildCompletes(child(log), { reload: true })
    expect(log).toEqual([
      'to.have.attribute.value data-kind=subagent',
      'to.have.attribute.value data-status=running',
      'finish',
      'to.have.attribute.value data-status=completed',
      'reload',
      'to.have.attribute.value data-status=completed',
    ])
  })

  it('finishes a child that is its own parent, and fails', async () => {
    const log: string[] = []
    await expect(expectRunningChildCompletes(child(log, { childId: 'same', parentId: 'same' }))).rejects.toThrow('the child runs as an agent of its own')
    expect(log).toContain('finish')
    expect(log).not.toContain('to.have.attribute.value data-status=completed')
  })
})
