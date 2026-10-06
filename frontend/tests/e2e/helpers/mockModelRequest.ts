import type { IncomingMessage, ServerResponse } from 'node:http'
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
