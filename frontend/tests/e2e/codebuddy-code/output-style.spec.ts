import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { agentOpenOptions } from '../agentSettings'
import { codebuddyTest } from '../codebuddy-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { createCodebuddyEnvironment } from '../helpers/codebuddyEnvironment'
import { CODEBUDDY_ALT_MODEL_ID, CODEBUDDY_ALT_MODEL_WIRE_ID, CODEBUDDY_MODEL_ID, MOCK_MODELS, MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, expectSameNativeSession, nativeOptionGroup } from '../helpers/nativeScenario'
import { exerciseModelSwitchKeepsOption, exerciseNativeOptionSequence } from '../helpers/nativeSettings'
import { writePrivateJSON } from '../helpers/privateConfigFile'
import { withPrivateNativeWorkspace } from '../helpers/privateNativeWorkspace'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { CODEBUDDY_AGENT, nativeContext } from './scenarios'

const customStyles = new Map([
  ['Configured Custom', 'LEAPMUX_CONFIGURED_STYLE_PROOF: Explain the result in detail.'],
  ['Second Custom', 'LEAPMUX_SECOND_STYLE_PROOF: State the result briefly.'],
])

/** Inspect native instructions only; conversation text cannot prove the selected style. */
function expectNativeStyle(request: MockModelRequestRecord, style: string): void {
  expect(request.protocol).toBe('openai-chat-completions')
  if (!isObject(request.body) || !Array.isArray(request.body.messages))
    throw new Error('The native CodeBuddy request has no messages.')
  const instructions = request.body.messages.filter(isObject).filter(message => message.role === 'system' || message.role === 'developer')
  expect(instructions.length).toBeGreaterThan(0)
  const text = JSON.stringify(instructions)
  for (const name of ['Concise', 'Explanatory'])
    expect(text.includes(`# Output Style: ${name}`)).toBe(style === name)
  for (const [name, marker] of customStyles)
    expect(text.includes(marker)).toBe(style === name)
  if (style === 'Default')
    expect(text).not.toContain('# Output Style:')
}

codebuddyTest('applies native styles across process restarts and browser reloads in the same session', async ({ native }) => {
  const baseline = 'STYLESESSIONANSWERPROOF'
  await sendNativeAnswer(native, 'Remember this conversation before changing style.', baseline)
  const before = await currentNativeAgent(native)
  const group = nativeOptionGroup(before, 'outputStyle')
  expect(group?.mutable).toBe(true)
  expect(group?.options.map(option => option.id)).toEqual(expect.arrayContaining(['Default', 'Concise', 'Explanatory']))
  await exerciseNativeOptionSequence(native, {
    groupId: 'outputStyle',
    steps: [
      { value: 'Default', via: 'default' },
      { value: 'Concise', via: 'choose' },
      { value: 'Concise', via: 'reload' },
      { value: 'Explanatory', via: 'choose' },
      { value: 'Default', via: 'choose' },
    ],
    nativeProof: async (request, step) => {
      expectNativeStyle(request, step.value)
      expect(JSON.stringify(request.body)).toContain(baseline)
      expectSameNativeSession(before, await currentNativeAgent(native), 'Changing the native output style')
    },
  })
})

codebuddyTest('discovers custom native styles and changes them without rewriting the profile', async ({ page, leapmuxServer, modelScript }) => {
  await withPrivateNativeWorkspace(page, leapmuxServer, {
    prefix: 'codebuddy-style',
    workerName: 'CodeBuddy output style test',
    providerAgent: CODEBUDDY_AGENT,
    prepare: (directory) => {
      const home = join(directory, 'home')
      const environment = createCodebuddyEnvironment({
        homeDir: home,
        baseURL: `${leapmuxServer.mockModelUrl}/v1`,
        modelKey: MODEL_KEY,
        modelID: MOCK_MODELS.deepseek,
        alternateModelID: CODEBUDDY_ALT_MODEL_WIRE_ID,
        startModel: CODEBUDDY_MODEL_ID,
      })
      const config = join(home, '.codebuddy')
      const styles = join(config, 'output-styles')
      mkdirSync(styles, { recursive: true })
      for (const [name, marker] of customStyles)
        writeFileSync(join(styles, `${name}.md`), `---\nname: ${name}\ndescription: A private native test style.\n---\n${marker}\n`)
      const settings = join(config, 'settings.json')
      writePrivateJSON(settings, { model: CODEBUDDY_MODEL_ID, outputStyle: 'Configured Custom' })
      return {
        agentEnv: { ...leapmuxServer.agentEnv, ...environment, HOME: home, USERPROFILE: home, ZDOTDIR: home },
        setup: { settings },
      }
    },
    openAgent: (server, workspaceId, workingDir) => openAgentViaAPI(server, workspaceId, workingDir, agentOpenOptions(CODEBUDDY_AGENT.provider)),
  }, async ({ server, workspaceId, setup }) => {
    const native = await nativeContext({ page, leapmuxServer: server, workspaceId, modelScript })
    const before = await currentNativeAgent(native)
    await exerciseNativeOptionSequence(native, {
      groupId: 'outputStyle',
      steps: [
        { value: 'Configured Custom', via: 'default' },
        { value: 'Concise', via: 'choose' },
        { value: 'Default', via: 'choose' },
        { value: 'Second Custom', via: 'choose' },
        { value: 'Second Custom', via: 'reload' },
      ],
      nativeProof: async (request, step) => {
        expectNativeStyle(request, step.value)
        expectSameNativeSession(before, await currentNativeAgent(native), 'Changing a configured native style')
        await retryUntilPass(async () => {
          const group = nativeOptionGroup(await currentNativeAgent(native), 'outputStyle')
          expect(group?.options.map(option => option.id)).toContain('Second Custom')
        })
        expect(JSON.parse(readFileSync(setup.settings, 'utf8'))).toHaveProperty('outputStyle', 'Configured Custom')
      },
    })
  })
})

codebuddyTest('keeps the selected native style after a model change and browser reload', async ({ native }) => {
  await exerciseModelSwitchKeepsOption(native, {
    kept: { groupId: 'outputStyle', value: 'Concise' },
    model: CODEBUDDY_ALT_MODEL_ID,
    nativeProof: (request) => {
      expectNativeStyle(request, 'Concise')
      expect(request.body).toHaveProperty('model', CODEBUDDY_ALT_MODEL_WIRE_ID)
    },
  })
})
