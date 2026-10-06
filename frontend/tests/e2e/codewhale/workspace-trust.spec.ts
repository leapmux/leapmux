import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { codewhaleExtractControl } from '../../../src/components/chat/providers/codewhale/extractControl'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { agentOpenOptions } from '../agentSettings'
import { codewhaleTest } from '../codewhale-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { withCleanup } from '../helpers/cleanup'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { currentNativeAgent, nativeModelContextText } from '../helpers/nativeScenario'
import { withNativeWorker } from '../helpers/nativeWorker'
import { exerciseMissingWorkspaceTrustRoute, exerciseNativeWorkspaceTrustLimit } from '../helpers/nativeWorkspaceTrustLimit'
import { createTestDirectory } from '../helpers/runDirectory'
import { getGlobalState } from '../helpers/server'
import { openWorkspace, tabById, waitForSettingsHydrated } from '../helpers/ui'
import { withAgentWorkspace } from '../helpers/workspace'
import { CODEWHALE_AGENT, nativeContext } from './scenarios'

codewhaleTest('classifies real native controls and proves the missing workspace-trust route', async ({ native }) => {
  await exerciseMissingWorkspaceTrustRoute(native, { askOption: 'permissionMode-ask', classify: codewhaleExtractControl })
})

codewhaleTest('keeps project config unloaded and applies the actual global configuration', async ({ page, modelScript, leapmuxServer, authenticatedCodewhaleWorkspace }) => {
  void authenticatedCodewhaleWorkspace
  const observed: { baseline?: { model: string, effort: string | undefined, control: string } } = {}
  // The HTTP app server loads global configuration. It does not merge the interactive project file.
  await withNativeWorker(leapmuxServer, {
    dataDirPrefix: 'codewhale-project-config-worker',
    workerName: 'Codewhale project configuration',
    env: { LEAPMUX_CODEWHALE_DEFAULT_EFFORT: '' },
  }, async ({ server }) => {
    await withAgentWorkspace(server, { ...CODEWHALE_AGENT, prefix: 'codewhale-project-config' }, async ({ workspaceId }) => {
      await openWorkspace(page, workspaceId)
      const context = await nativeContext({ page, modelScript, leapmuxServer: server, workspaceId })
      const baselineAgent = await currentNativeAgent(context)
      const selectedModel = baselineAgent.optionGroups.find(group => group.id === 'model')?.currentValue
      if (!selectedModel || !baselineAgent.agentSessionId)
        throw new Error('The native Codewhale baseline requires its selected model and session ID.')
      const effort = baselineAgent.optionGroups.find(group => group.id === 'effort')
      expect(effort?.currentValue).toBe('auto')
      expect(effort?.defaultValue).toBe('auto')
      expect(effort?.options.map(option => option.id)).toEqual(expect.arrayContaining(['low', 'high']))
      const baselinePrompt = 'Reply once before any project effort configuration exists.'
      const baseline = await sendNativeAnswer(context, baselinePrompt, 'The native project configuration baseline completed.')
      if (!isObject(baseline.body))
        throw new Error('The native Codewhale baseline requires a model request object.')
      const baselineEffort = baseline.body.reasoning_effort
      if (baselineEffort !== undefined && typeof baselineEffort !== 'string')
        throw new Error('The native Codewhale baseline effort must be a string or absent.')
      const projectEffort = baselineEffort === 'low' ? 'high' : 'low'
      expect(baseline.body.model).toBe(selectedModel)
      observed.baseline = { model: selectedModel, effort: baselineEffort, control: projectEffort }
      await exerciseNativeWorkspaceTrustLimit(context, {
        projectConfiguration: {
          prepare: ({ directory, marker }) => {
            mkdirSync(join(directory, '.codewhale'), { recursive: true })
            writeFileSync(join(directory, '.codewhale', 'config.toml'), `# ${marker}\nreasoning_effort = "${projectEffort}"\n`)
          },
          prove: async (privateContext, { directory, marker, agentId }) => {
            const config = join(directory, '.codewhale', 'config.toml')
            expect(readFileSync(config, 'utf8')).toContain(marker)
            const projectAgent = await currentNativeAgent(privateContext)
            expect(projectAgent.id).toBe(agentId)
            expect(projectAgent.workingDir).toBe(directory)
            expect(projectAgent.agentSessionId).not.toBe(baselineAgent.agentSessionId)
            expect(projectAgent.optionGroups.find(group => group.id === 'model')?.currentValue).toBe(selectedModel)
            expect(projectAgent.optionGroups.find(group => group.id === 'effort')?.currentValue).toBe('auto')
            for (const prompt of ['Reply once while the project effort file is present.', 'Reply once again while the same project effort file remains present.']) {
              const project = await sendNativeAnswer(privateContext, prompt, `The native project configuration check completed for ${prompt}`)
              if (!isObject(project.body))
                throw new Error('The native Codewhale project check requires a model request object.')
              expect(project.body.model).toBe(selectedModel)
              expect(project.body.reasoning_effort).toBe(baselineEffort)
              expect(project.body.reasoning_effort).not.toBe(projectEffort)
              expect(nativeModelContextText(project)).not.toContain(baselinePrompt)
            }
            rmSync(config)
            const nextId = await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId, directory, agentOpenOptions(AgentProvider.CODEWHALE))
            await tabById(page, nextId).click()
            const restoredAgent = await currentNativeAgent(privateContext)
            expect(restoredAgent.id).toBe(nextId)
            expect(restoredAgent.workingDir).toBe(directory)
            expect(restoredAgent.agentSessionId).not.toBe(projectAgent.agentSessionId)
            expect(restoredAgent.optionGroups.find(group => group.id === 'model')?.currentValue).toBe(selectedModel)
            expect(restoredAgent.optionGroups.find(group => group.id === 'effort')?.currentValue).toBe('auto')
            const restored = await sendNativeAnswer(privateContext, 'Reply once after the project effort configuration is removed.', 'The native project configuration default returned.')
            if (!isObject(restored.body))
              throw new Error('The restored native Codewhale turn requires a model request object.')
            expect(restored.body.model).toBe(selectedModel)
            expect(restored.body.reasoning_effort).toBe(baselineEffort)
            expect(restored.body.reasoning_effort).not.toBe(projectEffort)
          },
        },
      })
    })
  })
  const baseline = observed.baseline
  if (!baseline)
    throw new Error('The global Codewhale control requires the observed native baseline.')
  const configHome = leapmuxServer.agentEnv.CODEWHALE_HOME
  if (!configHome)
    throw new Error('The global Codewhale control requires the isolated native configuration directory.')
  assertPrivateNativePath(configHome, getGlobalState().tmpDir)
  const originalConfig = readFileSync(join(configHome, 'config.toml'), 'utf8')
  const globalDirectory = createTestDirectory('codewhale-global-config-control-')
  await withCleanup(async () => {
    const globalConfig = join(globalDirectory, 'config.toml')
    // Prepend the root key. Appending it would place it inside the final TOML table.
    writeFileSync(globalConfig, `reasoning_effort = "${baseline.control}"\n${originalConfig}`)
    await withNativeWorker(leapmuxServer, {
      dataDirPrefix: 'codewhale-global-config-worker',
      workerName: 'Codewhale global configuration control',
      env: { LEAPMUX_CODEWHALE_DEFAULT_EFFORT: '', CODEWHALE_CONFIG_PATH: globalConfig },
    }, async ({ server }) => {
      await withAgentWorkspace(server, { ...CODEWHALE_AGENT, prefix: 'codewhale-global-config' }, async ({ workspaceId }) => {
        await openWorkspace(page, workspaceId)
        const context = await nativeContext({ page, modelScript, leapmuxServer: server, workspaceId })
        expect((await currentNativeAgent(context)).optionGroups.find(group => group.id === 'effort')?.currentValue).toBe('auto')
        const global = await sendNativeAnswer(context, 'Reply through the actual private global Codewhale configuration.', 'The native global configuration control completed.')
        expect(global.body).toHaveProperty('model', baseline.model)
        expect(global.body).toHaveProperty('reasoning_effort', baseline.control)
        await page.reload()
        await waitForSettingsHydrated(page)
        const restored = await sendNativeAnswer(context, 'Reply after reloading the actual private global configuration.', 'The native global configuration control remained active.')
        expect(restored.body).toHaveProperty('model', baseline.model)
        expect(restored.body).toHaveProperty('reasoning_effort', baseline.control)
      })
    })
  }, async () => rmSync(globalDirectory, { recursive: true, force: true }))
})
