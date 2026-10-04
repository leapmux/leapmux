import type { AgentRun } from '../model/tools/agent'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentResultBody } from './agentResult'
import '../providers/testMocks'

function source(body: string): AgentRun {
  return { description: 'Read the module', agentId: 'owned-child', outcome: 'completed', metadata: [{ label: 'Agent ID', value: 'NATIVE_AGENT_OUTPUT' }], body }
}

describe('AgentResultBody', () => {
  it.each([false, true])('marks the report without marking properties when expanded is %s', (expanded) => {
    const { container } = render(() => <AgentResultBody source={source('NATIVE_AGENT_OUTPUT')} context={{ getMessageUiState: () => expanded }} />)
    const outputs = container.querySelectorAll('[data-tool-output-preview]')
    expect(outputs).toHaveLength(1)
    expect(outputs[0]?.textContent).toBe('NATIVE_AGENT_OUTPUT')
    const text = container.textContent ?? ''
    expect(text.indexOf('Agent ID:')).toBeLessThan(text.lastIndexOf('NATIVE_AGENT_OUTPUT'))
  })

  it('does not certify metadata as output when the report is empty', () => {
    const { container } = render(() => <AgentResultBody source={source('')} />)
    expect(container.textContent).toContain('NATIVE_AGENT_OUTPUT')
    expect(container.querySelector('[data-tool-output-preview]')).toBeNull()
  })
})
