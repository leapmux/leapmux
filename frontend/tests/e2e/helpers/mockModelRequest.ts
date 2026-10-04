import type { IncomingMessage, ServerResponse } from 'node:http'
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

export interface ModelGateTransport {
  request?: IncomingMessage
  response?: ServerResponse
  signal?: AbortSignal
}

/** The script owns accounting. The native Surface decides when to deliver its selected answer. */
export type SelectedModelAnswer
  = | { kind: 'missing', message: string }
    | {
      kind: 'step'
      step: MockModelStep
      isClosed: () => boolean
      holdGate: (name: string, transport: ModelGateTransport) => Promise<boolean>
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
