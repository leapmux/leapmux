import type { TestInfo } from '@playwright/test'
import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelRequestRecord, MockModelStep, MockModelToolCall } from './mockModelScript'
import type { NativeMessageSnapshot } from './nativeMessages'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { NativeToolOutput } from './nativeToolOutput'
import { nativeMessageBody, nativeMessageSupplement, readNativeMessageSnapshot } from './nativeMessages'
import { currentNativeAgent, nativeTextStep } from './nativeScenario'
import { waitForNativeToolSteps } from './nativeToolExecution'
import { bashToolCall } from './providerToolCalls'
import { quotePosixShellArgument } from './shellArguments'
import { sendMessage } from './ui'

export interface NativeToolOutputCapture<Context = ManagedNativeScenarioContext> {
  context: Context
  agent: AgentInfo
  snapshot: NativeMessageSnapshot
  call: MockModelToolCall
  nativeCallId: string
  request: MockModelRequestRecord
  output: NativeToolOutput
}

export interface NativeToolOutputCaptureOperations<Context = ManagedNativeScenarioContext> {
  prepare?: () => Promise<void>
  queue: (call: MockModelToolCall) => Promise<number>
  send: () => Promise<void>
  wait: (target: number, beforeIdle: () => Promise<void>) => Promise<void>
  request: (step: number) => Promise<MockModelRequestRecord>
  agent: () => Promise<AgentInfo>
  snapshot: (agent: AgentInfo) => Promise<NativeMessageSnapshot>
  nativeCallId?: (request: MockModelRequestRecord, scriptedId: string, snapshot: NativeMessageSnapshot) => string
  attach: (capture: NativeToolOutputCapture<Context>) => Promise<void>
  earlyProof?: (capture: NativeToolOutputCapture<Context>) => Promise<void>
  proof: (capture: NativeToolOutputCapture<Context>) => Promise<void>
}

/** Build the real Node command for a computed large result. `exitCode` sets the exit status of the process. */
export function nativeOutputFileCommand(output: NativeToolOutput, options: { exitCode?: number } = {}): string {
  const { exitCode } = options
  if (exitCode !== undefined && (!Number.isSafeInteger(exitCode) || exitCode < 0 || exitCode > 255))
    throw new Error('A native output command requires an exit code from 0 through 255.')
  const exit = exitCode === undefined ? '' : `; process.exitCode = ${exitCode}`
  return `node -e ${quotePosixShellArgument(`${output.source} process.stdout.write(completeOutput)${exit}`)}`
}

/** Keep native completion and evidence before provider assertions, with explicit test operations. */
export async function runNativeToolOutputCapture<Context>(
  context: Context,
  call: MockModelToolCall,
  output: NativeToolOutput,
  operations: NativeToolOutputCaptureOperations<Context>,
): Promise<NativeToolOutputCapture<Context>> {
  if (!call.id || !call.name || !output.source.trim() || !output.text || !output.omittedMarker || !output.text.includes(output.omittedMarker)
    || output.source.includes(output.text) || output.source.includes(output.omittedMarker)
    || output.text.length > 4 * 1024 * 1024 || output.source.length > 512 * 1024) {
    throw new Error('The native tool output capture requires limited computed output and exact tool identity.')
  }
  await operations.prepare?.()
  const initialAgent = await operations.agent()
  const owner = { id: initialAgent.id, sessionId: initialAgent.agentSessionId, workingDir: initialAgent.workingDir }
  if (!owner.id)
    throw new Error('The native tool output capture requires a started agent before its tool call.')
  const start = await operations.queue(call)
  if (!Number.isSafeInteger(start) || start < 0 || !Number.isSafeInteger(start + 2))
    throw new Error('The native tool output capture received an invalid queue index.')
  await operations.send()
  const readCapture = async (): Promise<NativeToolOutputCapture<Context>> => {
    const request = await operations.request(start + 1)
    if (request.stepIndex !== start + 1)
      throw new Error('The native tool output capture received a different model step.')
    const agent = await operations.agent()
    if (agent.id !== owner.id || agent.workingDir !== owner.workingDir || (owner.sessionId !== '' && agent.agentSessionId !== owner.sessionId))
      throw new Error('The native tool output capture changed its native session or working directory during the tool call.')
    if (!agent.agentSessionId)
      throw new Error('The native tool output capture requires the first tool turn to create its native session.')
    owner.sessionId = agent.agentSessionId
    const snapshot = await operations.snapshot(agent)
    if (snapshot.agentId !== owner.id || snapshot.agentSessionId !== owner.sessionId)
      throw new Error('The native tool output capture changed its agent or native session identity.')
    const nativeCallId = operations.nativeCallId?.(request, call.id, snapshot) ?? call.id
    if (!nativeCallId)
      throw new Error('The native tool output capture received no actual call ID.')
    return { context, agent, snapshot, call, nativeCallId, request, output }
  }
  await operations.wait(start + 2, async () => {
    const capture = await readCapture()
    await operations.attach(capture)
    await operations.earlyProof?.(capture)
  })
  const capture = await readCapture()
  await operations.attach(capture)
  const request = capture.request
  if (request.mockCredential?.accepted !== true)
    throw new Error('The native tool output request did not use the isolated mock credential.')
  await operations.proof(capture)
  return capture
}

/** Run one actual large native tool turn and attach original evidence before provider proof. */
export async function captureNativeToolOutput(
  context: ManagedNativeScenarioContext,
  testInfo: Pick<TestInfo, 'attach'>,
  options: {
    output: NativeToolOutput
    callId: string
    prepare?: () => Promise<void>
    call?: (output: NativeToolOutput, callId: string) => MockModelToolCall
    nativeCallId?: (request: MockModelRequestRecord, scriptedId: string, snapshot: NativeMessageSnapshot) => string
    finalStep?: MockModelStep
    beforeIdleProof?: (capture: NativeToolOutputCapture) => Promise<void>
    proof: (capture: NativeToolOutputCapture) => Promise<void>
  },
): Promise<NativeToolOutputCapture> {
  const command = nativeOutputFileCommand(options.output)
  const call = options.call?.(options.output, options.callId) ?? bashToolCall(context.provider, options.callId, command)
  return runNativeToolOutputCapture(context, call, options.output, {
    ...(options.prepare ? { prepare: options.prepare } : {}),
    ...(options.nativeCallId ? { nativeCallId: options.nativeCallId } : {}),
    ...(options.beforeIdleProof ? { earlyProof: options.beforeIdleProof } : {}),
    queue: nativeCall => context.modelScript.queue({ toolCalls: [nativeCall] }, options.finalStep ?? nativeTextStep(context, 'The native large tool output ended.')),
    send: () => sendMessage(context.page, context.modelScript.prompt('Run the native large output tool once.')),
    wait: (target, beforeIdle) => waitForNativeToolSteps(context, target, { beforeIdle }),
    request: step => context.modelScript.requestAt(step),
    agent: () => currentNativeAgent(context),
    snapshot: agent => readNativeMessageSnapshot(context, agent.id),
    attach: async capture => testInfo.attach('native-tool-output-records', {
      body: JSON.stringify({ agentId: capture.agent.id, agentSessionId: capture.agent.agentSessionId, call: capture.call, nativeCallId: capture.nativeCallId, request: capture.request, output: { source: capture.output.source, firstMarker: capture.output.firstMarker, omittedMarker: capture.output.omittedMarker, lastMarker: capture.output.lastMarker }, messages: capture.snapshot.messages.map(message => ({ id: message.id, spanId: message.spanId, spanType: message.spanType, completion: message.completion, frame: nativeMessageBody(message), supplement: nativeMessageSupplement(message) })) }, null, 2),
      contentType: 'application/json',
    }),
    proof: options.proof,
  })
}
