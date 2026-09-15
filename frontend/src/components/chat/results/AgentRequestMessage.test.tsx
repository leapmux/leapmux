import type { AgentResultSource } from './agentResult'
import type { ToolPresentation } from './toolPresentation'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { toolUseHeader } from '../toolStyles.css'
import { AgentRequestMessage, agentToolPresentation } from './AgentRequestMessage'

describe('shared agent request labels', () => {
  it('uses a visible fallback for blank labels and omits a blank agent type', () => {
    const { container } = render(() => <AgentRequestMessage source={{ toolName: ' Task ', description: ' \n ', agentType: ' \t ', prompt: '' }} />)
    expect(container.querySelector(`.${toolUseHeader}`)?.textContent).toBe('Task')
  })

  it('uses the common fallback when both labels are blank', () => {
    const { container } = render(() => <AgentRequestMessage source={{ toolName: ' ', description: ' ', prompt: '' }} />)
    expect(container.querySelector(`.${toolUseHeader}`)?.textContent).toBe('Agent')
  })
})

describe('agentToolPresentation', () => {
  function model(overrides: Partial<ToolPresentation> = {}): ToolPresentation {
    return { kind: 'other', title: 'task', label: 'Task', input: { prompt: 'Read it' }, output: 'Done', body: { type: 'text' }, unresolvedTerminals: [], ...overrides }
  }

  const request = { toolName: 'Task', description: 'Inspect the entry points', agentType: 'explore', prompt: 'Read it' }

  const result: AgentResultSource = { description: 'Inspect the entry points', agentId: 'child-1', status: 'completed', outcome: 'completed', metadata: [], body: 'Found two' }

  it('titles the row from the description and keeps the rest of the model', () => {
    expect(agentToolPresentation(model(), request)).toEqual({
      ...model(),
      kind: 'agent',
      title: 'Inspect the entry points',
      agentRequest: request,
      body: { type: 'text' },
    })
  })

  it('falls back to one word for a request that describes nothing', () => {
    // Five providers spelled this fallback separately, and two of them drew `Agent`
    // where the other three drew `Task`.
    expect(agentToolPresentation(model(), { ...request, description: '' }).title).toBe('Task')
    expect(agentToolPresentation(model(), { ...request, description: ' \n\t ' }).title).toBe('Task')
  })

  it('draws the report only once a result exists', () => {
    expect(agentToolPresentation(model(), request).body).toEqual({ type: 'text' })
    expect(agentToolPresentation(model(), request, result).body).toEqual({ type: 'agent', source: result })
  })

  it('lets the caller override the model fields it owns', () => {
    const presentation = { ...agentToolPresentation(model(), request, result), output: 'resolved report', label: 'Task' }
    expect(presentation.output).toBe('resolved report')
    expect(presentation.label).toBe('Task')
    expect(presentation.body).toEqual({ type: 'agent', source: result })
  })
})
