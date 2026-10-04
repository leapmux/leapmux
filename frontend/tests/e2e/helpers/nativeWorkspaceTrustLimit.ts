import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { NativeStartupLaunch, NativeStartupWrapper } from './nativeStartupWrapper'
import { randomUUID } from 'node:crypto'
import { expect } from '@playwright/test'
import { AgentStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { openAgentViaAPI } from './api'
import { expectNoNativeStartupControl } from './nativeControlObservation'
import { nativeAgentById } from './nativeScenario'
import { withNativeStartupWorker } from './nativeStartupWorker'
import { createTestDirectory } from './runDirectory'
import { tabById } from './ui'

export interface NativeProjectConfiguration {
  directory: string
  marker: string
}

interface NativeWorkspaceTrustBaseOptions {
  projectConfiguration: {
    prepare: (project: NativeProjectConfiguration) => Promise<void> | void
    prove: (context: ManagedNativeScenarioContext, project: NativeProjectConfiguration & { agentId: string }) => Promise<void>
  }
  optionValues?: Record<string, string>
  worker?: {
    launch: NativeStartupLaunch
    workerEnvironment: (wrapper: NativeStartupWrapper) => Record<string, string>
  }
}

export type NativeWorkspaceTrustOptions = NativeWorkspaceTrustBaseOptions & (
  { startup?: 'active' } | { startup: 'failed', startupError: string }
)

/** Observe actual project configuration processing from native startup through its completed turn. */
export async function exerciseNativeWorkspaceTrustLimit(
  context: ManagedNativeScenarioContext,
  options: NativeWorkspaceTrustOptions,
): Promise<void> {
  if (!options?.projectConfiguration || typeof options.projectConfiguration.prepare !== 'function' || typeof options.projectConfiguration.prove !== 'function')
    throw new Error('The workspace trust scenario requires an actual native project configuration proof.')
  if (options.startup !== undefined && options.startup !== 'active' && options.startup !== 'failed')
    throw new Error('The workspace trust startup must be active or failed.')
  if (options.startup === 'failed' && (typeof options.startupError !== 'string' || options.startupError.trim().length === 0))
    throw new Error('The failed workspace startup requires the native configuration error.')
  const project = { directory: createTestDirectory('native-workspace-trust-'), marker: `NATIVEWORKSPACECONFIG${randomUUID().replaceAll('-', '')}` }
  await options.projectConfiguration.prepare(project)
  const run = async (privateContext: ManagedNativeScenarioContext, wrapper?: NativeStartupWrapper) => {
    let agentId = ''
    const expectedStatus = options.startup === 'failed' ? AgentStatus.STARTUP_FAILED : AgentStatus.ACTIVE
    const completeStartup = async () => {
      await expect.poll(async () => (await nativeAgentById(privateContext, agentId))?.status).toBe(expectedStatus)
      if (options.startup === 'failed')
        expect((await nativeAgentById(privateContext, agentId))?.startupError).toContain(options.startupError)
    }
    await expectNoNativeStartupControl(privateContext, {
      testId: 'control-banner',
      additionalTestIds: ['dialog-editor'],
      start: async () => {
        const defaults = agentOpenOptions(agentSettings(privateContext.provider))
        const server = privateContext.leapmuxServer
        agentId = await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, privateContext.workspaceId, project.directory, {
          agentProvider: privateContext.provider,
          ...defaults,
          optionValues: { ...defaults.optionValues, ...options.optionValues },
        })
        if (wrapper) {
          await wrapper.entry
          await wrapper.release()
        }
        await tabById(privateContext.page, agentId).click()
        await completeStartup()
        const agent = await nativeAgentById(privateContext, agentId)
        expect(agent?.id).toBe(agentId)
        expect(agent?.workingDir).toBe(project.directory)
      },
      relatedControl: () => options.projectConfiguration.prove(privateContext, { ...project, agentId }),
      ...(options.startup === 'failed' ? { nativeCompletion: completeStartup } : {}),
    })
  }
  if (options.worker) {
    await withNativeStartupWorker(context, options.worker.launch, { workerEnvironment: options.worker.workerEnvironment }, async (workerId, wrapper) => {
      await run({ ...context, leapmuxServer: { ...context.leapmuxServer, workerId } }, wrapper)
    })
  }
  else {
    await run(context)
  }
}
