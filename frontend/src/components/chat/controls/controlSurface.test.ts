import type { ControlRequest } from '~/stores/control.store'
import { createRoot, createSignal } from 'solid-js'
import { afterEach, describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { controlSurface, createControlSurface } from './controlSurface'
import '../providers'

function request(payload: Record<string, unknown>): ControlRequest {
  return { requestId: 'r-1', agentId: 'agent', payload }
}

function questionRequest(): ControlRequest {
  return request({ request: { tool_name: 'AskUserQuestion', input: { questions: [{ question: 'Which one?', options: [{ label: 'A' }] }] } } })
}

describe('controlSurface', () => {
  it('reports the question form for a question payload', () => {
    const surface = controlSurface(questionRequest(), AgentProvider.CLAUDE_CODE, undefined)
    expect(surface?.kind).toBe('question')
  })

  it('reports the elicitation form for an elicitation payload', () => {
    const surface = controlSurface(
      request({ method: 'elicitation/create', params: { mode: 'form', message: 'Choose.', requestedSchema: { type: 'object', properties: { count: { type: 'integer' } } } } }),
      AgentProvider.GOOSE,
      undefined,
    )
    expect(surface?.kind).toBe('elicitation')
  })

  // The provider's extractControl reads a permission that no shared form answers.
  // The old plugin kind left that decision to each provider's component.
  it('reads everything else as a permission the provider extracted', () => {
    const surface = controlSurface(
      request({ request: { tool_name: 'Bash', input: { command: 'pwd' } } }),
      AgentProvider.CLAUDE_CODE,
      undefined,
    )
    expect(surface).toEqual({
      kind: 'permission',
      permission: {
        title: 'Bash',
        input: { command: 'pwd' },
        command: 'pwd',
        options: [
          { optionId: 'once', kind: 'allow_once', name: 'Allow once' },
          { optionId: 'session', kind: 'allow_always', name: 'Allow for this session' },
          { optionId: 'deny', kind: 'reject_once', name: 'Deny' },
        ],
      },
    })
  })

  /*
   * The generic fallback states no arguments.
   * The old fallback displayed the whole JSON-RPC envelope as Arguments.
   * Its Allow button sent payload.request.input instead, or an empty object when input was absent.
   * The banner therefore displayed data that the agent did not receive.
   */
  it('states no arguments for a payload no provider reads', () => {
    // A provider whose plugin recognizes nothing in this envelope.
    const surface = controlSurface(request({ unknown_envelope: { nothing: 'here' } }), AgentProvider.GOOSE, undefined)
    expect(surface).toEqual({ kind: 'permission', permission: { options: [] } })
  })

  it('states no arguments for a provider with no plugin at all', () => {
    const surface = controlSurface(request({ request: { tool_name: 'Bash' } }), undefined, undefined)
    expect(surface).toEqual({ kind: 'permission', permission: { options: [] } })
  })

  it('reports nothing for a request the store already removed', () => {
    expect(controlSurface(null, AgentProvider.CLAUDE_CODE, undefined)).toBeUndefined()
    expect(controlSurface(undefined, AgentProvider.CLAUDE_CODE, undefined)).toBeUndefined()
  })

  // The request's own provider wins over the agent's, so the banner and the
  // composer classify a queued request from another provider the same way.
  it('prefers the provider the request carries', () => {
    const goose: ControlRequest = {
      ...request({ method: 'elicitation/create', params: { mode: 'form', message: 'Choose.', requestedSchema: { type: 'object', properties: {} } } }),
      agentProvider: AgentProvider.GOOSE,
    }
    expect(controlSurface(goose, AgentProvider.CLAUDE_CODE, undefined)?.kind).toBe('elicitation')
  })
})

// The composer creates one surface for the active request and passes it to both parts of the banner.
// Neither part classifies the request again.
describe('createControlSurface', () => {
  const disposers: (() => void)[] = []
  afterEach(() => {
    for (const dispose of disposers.splice(0))
      dispose()
  })

  function live(agentProvider: AgentProvider | undefined, current: () => ControlRequest | null) {
    return createRoot((dispose) => {
      disposers.push(dispose)
      return createControlSurface(current, () => undefined, () => agentProvider)
    })
  }

  it('reclassifies as the request it follows changes', () => {
    const [current, setCurrent] = createSignal<ControlRequest | null>(questionRequest())
    const surface = live(AgentProvider.CLAUDE_CODE, current)
    expect(surface.provider()).toBe(AgentProvider.CLAUDE_CODE)
    expect(surface.surface()?.kind).toBe('question')

    setCurrent(request({ request: { tool_name: 'Bash', input: { command: 'pwd' } } }))
    expect(surface.surface()?.kind).toBe('permission')

    setCurrent(null)
    expect(surface.surface()).toBeUndefined()
  })

  // One memo, so every reader of the same request gets the same answer without
  // parsing the payload again.
  it('answers every reader of one request from the same classification', () => {
    const surface = live(AgentProvider.CLAUDE_CODE, questionRequest)
    expect(surface.surface()).toBe(surface.surface())
  })

  it('prefers the provider the request carries', () => {
    const goose = { ...questionRequest(), agentProvider: AgentProvider.GOOSE }
    const surface = live(AgentProvider.CLAUDE_CODE, () => goose)
    expect(surface.provider()).toBe(AgentProvider.GOOSE)
  })
})
