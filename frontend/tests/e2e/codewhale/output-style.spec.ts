import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { agentOpenOptions } from '../agentSettings'
import { codewhaleTest } from '../codewhale-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { CODEWHALE_VISION_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { currentNativeAgent, expectSameNativeSession, nativeOptionGroup } from '../helpers/nativeScenario'
import { exerciseModelSwitchKeepsOption, exerciseNativeOptionSequence } from '../helpers/nativeSettings'
import { withPrivateNativeWorkspace } from '../helpers/privateNativeWorkspace'
import { CODEWHALE_AGENT, nativeContext } from './scenarios'

/** Inspect only native instruction messages, so conversation text cannot supply the style marker. */
function expectNativeStyle(request: MockModelRequestRecord, concise: boolean) {
  expect(request.protocol).toBe('openai-chat-completions')
  if (!isObject(request.body) || !Array.isArray(request.body.messages))
    throw new Error('The native Codewhale request has no messages.')
  const messages = request.body.messages.filter(isObject)
  const instructions = JSON.stringify(messages.filter(message => message.role === 'system' || message.role === 'developer'))
  expect(instructions.includes('## Concise Output Discipline')).toBe(concise)
  return { body: request.body, messages }
}

codewhaleTest('applies native output styles and preserves the configured default and conversation across restarts', async ({ page, leapmuxServer, modelScript }) => {
  await withPrivateNativeWorkspace(page, leapmuxServer, {
    prefix: 'codewhale-style',
    workerName: 'Codewhale output style test',
    providerAgent: CODEWHALE_AGENT,
    // Reuse the suite's mock configuration. A nested private home exceeds the native Unix socket path limit.
    prepare: () => ({ agentEnv: { ...leapmuxServer.agentEnv, CODEWHALE_VERBOSITY: 'concise' }, setup: undefined }),
    openAgent: (server, workspaceId, workingDir) => openAgentViaAPI(server, workspaceId, workingDir, agentOpenOptions(CODEWHALE_AGENT.provider)),
  }, async ({ server, workspaceId }) => {
    const native = await nativeContext({ page, leapmuxServer: server, workspaceId, modelScript })
    const before = await currentNativeAgent(native)
    const group = nativeOptionGroup(before, 'outputStyle')
    expect(group?.mutable).toBe(true)
    expect(group?.options.map(option => option.id)).toEqual(['auto', 'normal', 'concise'])
    let initialModel: unknown
    let initialEffort: unknown
    let firstUser: string | undefined
    await exerciseNativeOptionSequence(native, {
      groupId: 'outputStyle',
      steps: [
        { value: 'auto', via: 'default' },
        { value: 'normal', via: 'choose' },
        { value: 'normal', via: 'reload' },
        { value: 'concise', via: 'choose' },
        { value: 'concise', via: 'reload' },
        { value: 'auto', via: 'choose' },
      ],
      nativeProof: async (request, step, index) => {
        const { body, messages } = expectNativeStyle(request, step.value !== 'normal')
        if (index === 0) {
          initialModel = body.model
          initialEffort = body.reasoning_effort
          const content = messages.find(message => message.role === 'user')?.content
          if (typeof content !== 'string' || !content)
            throw new Error('The baseline Codewhale request has no user text.')
          // Codewhale appends metadata to the current turn but omits it from stored history.
          firstUser = content.replace(/\n<turn_meta>[\s\S]*<\/turn_meta>\s*$/, '')
          expect(firstUser).not.toBe('')
        }
        if (index > 0)
          expect(messages.some(message => message.role === 'user' && message.content === firstUser)).toBe(true)
        expect(body.model).toBe(initialModel)
        expect(body.reasoning_effort).toBe(initialEffort)
        expectSameNativeSession(before, await currentNativeAgent(native), 'Changing the native output style')
      },
    })
  })
})

codewhaleTest('keeps the native output style after a model switch and reload', async ({ native }) => {
  const group = nativeOptionGroup(await currentNativeAgent(native), 'outputStyle')
  expect(group?.mutable).toBe(true)
  await exerciseModelSwitchKeepsOption(native, {
    kept: { groupId: 'outputStyle', value: 'concise' },
    model: CODEWHALE_VISION_MODEL_ID,
    nativeProof: (request) => {
      expectNativeStyle(request, true)
      expect(request.body).toHaveProperty('model', CODEWHALE_VISION_MODEL_ID)
    },
  })
})
