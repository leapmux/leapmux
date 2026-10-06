import type { ControlExtractionInput, ExtractedControlRequest } from '../../../src/components/chat/providers/capabilities'
import type { MockModelToolCall } from './mockModelScript'
import type { NativePermissionOperationPlan } from './nativePermission'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { NativeStartupLaunch, NativeStartupWrapper } from './nativeStartupWrapper'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AgentStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { openAgentViaAPI } from './api'
import { writeMcpEchoServer } from './mcpEchoServer'
import { waitForMcpToolListed } from './mcpServerReceipt'
import { expectNoNativeStartupControl } from './nativeControlObservation'
import { sendNativeAnswer } from './nativeConversation'
import { nativeAgentById, nativeModelInstructionText } from './nativeScenario'
import { withNativeStartupWorker } from './nativeStartupWorker'
import { nativeToolResult } from './nativeToolResult'
import { retryUntilPass } from './retryUntilPass'
import { createTestDirectory, isFileNameComponent } from './runDirectory'
import { quotePosixShellArgument, uniqueMarker } from './shellArguments'
import { chooseSettingsOption, tabById, waitForSettingsIdle } from './ui'
import { exerciseUnsupportedControlThroughPermission } from './unsupportedNativeControl'
import { createGitRepo } from './worktree'

export interface NativeProjectConfiguration {
  directory: string
  marker: string
}

/** The project configuration that a workspace-trust scenario writes, and its proof that the provider read it. */
export interface NativeProjectConfigurationProof {
  prepare: (project: NativeProjectConfiguration) => Promise<void> | void
  prove: (context: ManagedNativeScenarioContext, project: NativeProjectConfiguration & { agentId: string }) => Promise<void>
}

interface NativeWorkspaceTrustBaseOptions {
  projectConfiguration: NativeProjectConfigurationProof
  optionValues?: Record<string, string>
  worker?: {
    launch: NativeStartupLaunch
    workerEnvironment: (wrapper: NativeStartupWrapper) => Record<string, string>
  }
}

export type NativeWorkspaceTrustOptions = NativeWorkspaceTrustBaseOptions & (
  { startup?: 'active' } | { startup: 'failed', startupError: string }
)

/**
 * A private Worker on which a provider loads the project configuration that the shared agent
 * environment turns off.
 *
 * `helpers/mockAgentEnvironment.ts` sets `disableVariable` to `true` for every agent of the suite,
 * so that no spec reads the configuration of the LeapMux checkout around its run directory. A
 * workspace-trust spec exists to prove that the provider loads project configuration, so it runs
 * its agent on a private Worker where the variable is `false`. The shared environment must
 * disable it, which also refuses a misspelled variable that would leave the configuration off.
 *
 * `launch` is the `nativeLaunch` of the provider, which finds the executable that a Worker
 * spawned with the agent environment finds.
 */
export function projectConfigurationWorker(
  environment: Record<string, string> | undefined,
  launch: NativeStartupLaunch,
  disableVariable: string,
): NonNullable<NativeWorkspaceTrustBaseOptions['worker']> {
  if (environment?.[disableVariable] !== 'true')
    throw new Error(`The shared agent environment does not turn off project configuration through ${disableVariable}.`)
  return {
    launch,
    workerEnvironment: () => ({ [disableVariable]: 'false' }),
  }
}

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
  const project = { directory: createTestDirectory('native-workspace-trust-'), marker: uniqueMarker('NATIVEWORKSPACECONFIG') }
  await options.projectConfiguration.prepare(project)
  const run = async (privateContext: ManagedNativeScenarioContext, wrapper?: NativeStartupWrapper) => {
    let agentId = ''
    const expectedStatus = options.startup === 'failed' ? AgentStatus.STARTUP_FAILED : AgentStatus.ACTIVE
    const completeStartup = async () => {
      const agent = await retryUntilPass(async () => {
        const current = await nativeAgentById(privateContext, agentId)
        expect(current?.status, 'the Worker ends the startup with the expected status').toBe(expectedStatus)
        return current
      })
      if (options.startup === 'failed')
        expect(agent?.startupError).toContain(options.startupError)
    }
    await expectNoNativeStartupControl(privateContext, {
      testId: 'control-banner',
      additionalTestIds: ['dialog-editor'],
      start: async () => {
        const server = privateContext.leapmuxServer
        const overrides = options.optionValues ? { optionValues: options.optionValues } : {}
        agentId = await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, privateContext.workspaceId, project.directory, agentOpenOptions(privateContext.provider, overrides))
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
      relatedProof: () => options.projectConfiguration.prove(privateContext, { ...project, agentId }),
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

/**
 * A project that holds one instruction file, and the proof that the text of the file reached the model.
 *
 * The proof reads the request through the context reader of the provider when the provider sets one, and through
 * the generic instruction reader otherwise. The generic reader takes the instructions and the user text only, so a
 * marker in a tool schema or a tool result does not satisfy it.
 */
export function instructionFileConfiguration(
  fileName: string,
  options: {
    /**
     * Make the directory the root of a git repository of its own. A provider that reads the file from each
     * directory up to the repository root then stops at the directory, and does not also read the instructions of
     * the LeapMux checkout around it.
     */
    gitRoot?: boolean
    /** The text by which the provider states the path of the file that it loaded. */
    sourceLine?: (path: string) => string
  } = {},
): NativeProjectConfigurationProof {
  if (!isFileNameComponent(fileName))
    throw new Error('The project instruction file requires one file name component.')
  return {
    prepare: ({ directory, marker }) => {
      if (options.gitRoot)
        createGitRepo(directory, '.')
      writeFileSync(join(directory, fileName), `# Native project configuration\nKeep ${marker} as a standing project instruction.\n`)
    },
    prove: async (context, { directory, marker }) => {
      const request = await sendNativeAnswer(context, 'Reply once after native project configuration loads.', 'The native project configuration turn completed.')
      const text = context.readModelContext?.(request) ?? nativeModelInstructionText(request)
      expect(text).toContain(marker)
      if (options.sourceLine)
        expect(text).toContain(options.sourceLine(join(directory, fileName)))
    },
  }
}

/** The private MCP server that `mcpServerProjectConfiguration` registers, as the configuration of a provider states it. */
export interface ProjectMcpServer {
  /** The server name in the configuration. */
  name: string
  command: string
  args: string[]
}

/** The receipt file of the server that `mcpServerProjectConfiguration` registers, in the project directory. */
const PROJECT_MCP_RECEIPT = 'workspace-mcp-receipt.json'

/**
 * A git project that registers a private MCP echo server, and the proof that the provider started that server from
 * the project configuration: one native turn completes, and the server answered `initialize` and listed `echo`.
 * `writeConfiguration` writes the project configuration file of the provider, which starts `server`.
 */
export function mcpServerProjectConfiguration(writeConfiguration: (directory: string, server: ProjectMcpServer) => void): NativeProjectConfigurationProof {
  return {
    prepare: ({ directory }) => {
      createGitRepo(directory, '.')
      const { script } = writeMcpEchoServer(directory, { receiptLog: join(directory, PROJECT_MCP_RECEIPT) })
      writeConfiguration(directory, { name: 'trust_probe', command: process.execPath, args: [script] })
    },
    prove: async (context, { directory }) => {
      await sendNativeAnswer(context, 'Return one native response from this scratch project.', 'The native project configuration probe completed.')
      await waitForMcpToolListed(join(directory, PROJECT_MCP_RECEIPT), 'echo')
    },
  }
}

/**
 * Prove that LeapMux offers no workspace-trust route for a provider that asks before a tool runs.
 *
 * The provider asks for one real operation, the browser classifier reads each control that the provider sends, and
 * no control offers a trust decision.
 */
export async function exerciseMissingWorkspaceTrustRoute(
  context: ManagedNativeScenarioContext,
  options: {
    /** The test ID of the settings option under which the provider asks before a tool runs. */
    askOption: string
    classify: (input: ControlExtractionInput) => ExtractedControlRequest | null
    /**
     * Prepare the operation that the provider asks for, after the ask option applies. The default writes one file
     * in the working directory through the shell tool of the provider.
     */
    operation?: () => Promise<NativePermissionOperationPlan> | NativePermissionOperationPlan
  },
): Promise<void> {
  if (!options.askOption)
    throw new Error('The missing workspace-trust route requires the option under which the provider asks.')
  await chooseSettingsOption(context.page, options.askOption)
  await waitForSettingsIdle(context.page)
  // The operation is prepared after the ask option applies, because a provider can restart its process to apply it.
  await exerciseUnsupportedControlThroughPermission(context, {
    purpose: 'workspace-trust',
    classify: options.classify,
    ...(options.operation ? { operation: await options.operation() } : {}),
  })
}

/**
 * A shell command that writes one file in a private directory outside the working directory, and prints the text
 * of that file. `toolCall` gives the shell tool call of the provider that carries the command.
 */
export function outsideFileWriteOperation(toolCall: (callId: string, command: string) => MockModelToolCall): NativePermissionOperationPlan {
  const file = join(createTestDirectory('native-workspace-trust-'), 'native-control.txt')
  const callId = 'native-control-permission'
  const command = `printf 'NATIVECONTROL%s\\n' "$((40 + 2))" > ${quotePosixShellArgument(file)}; cat ${quotePosixShellArgument(file)}`
  return {
    toolCall: toolCall(callId, command),
    beforeDecision: () => {
      expect(existsSync(file)).toBe(false)
    },
    nativeProof: (request) => {
      expect(nativeToolResult(request, callId)).toContain('NATIVECONTROL42')
      expect(readFileSync(file, 'utf8')).toBe('NATIVECONTROL42\n')
    },
  }
}
