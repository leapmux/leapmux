import type { Buffer } from 'node:buffer'
import type { IncomingMessage, ServerResponse } from 'node:http'
import type { Duplex } from 'node:stream'
import type { NativeInstructionFile, RunRootSentinelPolicy } from './ancestorInstructions'
import type { DisconnectSignals } from './mockHttp'
import type { MockModelCredential, MockModelDeliveredError, MockModelProtocol, MockModelRequestRecord, MockModelServerContext, MockModelStep } from './mockModelScript'
import type { ModelStream } from './modelStream'

/** Project one native request into the existing script matcher and request record. */
export interface ModelRequestContext {
  protocol: MockModelProtocol
  path: string
  body: unknown
  systemText: string
  userText: string
  scenarioID?: string
  serverContext?: MockModelServerContext
  mockCredential?: MockModelCredential
  requestHeaders?: MockModelRequestRecord['requestHeaders']
  nativeRequest?: MockModelRequestRecord['nativeRequest']
}

/** The script owns accounting. The native Surface decides when to deliver its selected answer. */
export type SelectedModelAnswer
  = | { kind: 'missing', message: string }
    | {
      kind: 'step'
      step: MockModelStep
      isClosed: () => boolean
      /** Hold the answer at a named gate until a test releases it. False when the client goes away first. */
      holdGate: (name: string, transport: DisconnectSignals) => Promise<boolean>
      /**
       * Hold the answer at the step's own gate, or for its delay: a step states at most one of them. False when the
       * client goes away first, and the surface then drops the answer. Each surface calls this once, before it
       * answers, so every surface holds a step by the same rules.
       */
      holdStep: (transport: DisconnectSignals) => Promise<boolean>
      stream: (response: ServerResponse, request?: IncomingMessage) => ModelStream
      bufferGeneration: (signal: AbortSignal) => Promise<boolean>
      recordHttpResponse: (response: ServerResponse, deliveredError: () => MockModelDeliveredError | undefined) => void
      recordServiceError: (error: NonNullable<MockModelRequestRecord['serviceResponse']>) => void
    }

/** Select and record an inference without interpreting its native transport. */
export interface MockModelScriptHost {
  hasScenario: (id: string) => boolean
  select: (context: ModelRequestContext, capabilities?: { allowServiceToolMetadata?: boolean }) => SelectedModelAnswer
}

/**
 * Check the instruction files that a native agent sent to its own service, outside any model request body
 * (./ancestorInstructions.ts). The mock counts each refusal as it counts a request body that holds the sentinel, so
 * the run fails at shutdown, and the scenario of `context` records the refusal as an unexpected request.
 */
export interface InstructionFileGuard {
  check: (context: ModelRequestContext, files: readonly NativeInstructionFile[], policy: RunRootSentinelPolicy) => void
}

/**
 * One native service that the mock model server answers.
 *
 * The server offers each request to each surface in the order of its registration
 * list. The first surface that claims the request answers it. A new surface is one
 * entry in that list.
 */
export interface MockSurface {
  /** Answer the request and return true, or return false, with nothing written, for a request of another service. */
  handleHttp: (request: IncomingMessage, response: ServerResponse, url: URL) => boolean | Promise<boolean>
  /** Answer a WebSocket upgrade and return true, or return false, with nothing written, for an upgrade of another service. */
  handleUpgrade?: (request: IncomingMessage, socket: Duplex, head: Buffer, url: URL) => boolean
  /** Forget the state that a deleted scenario left in the surface. */
  clearScenario?: (scenarioID: string) => void
  /** Release what the surface holds when the server closes. */
  close?: () => void
}
