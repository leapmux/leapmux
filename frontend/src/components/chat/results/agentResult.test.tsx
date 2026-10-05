import type { AgentRun } from '../model/tools/agent'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { toolResultCollapsed } from '../toolStyles.css'
import { AgentResultBody } from './agentResult'
import '../providers/testMocks'

function source(body: string): AgentRun {
  return { description: 'Read the module', agentId: 'owned-child', outcome: 'completed', metadata: [{ label: 'Agent ID', value: 'NATIVE_AGENT_OUTPUT' }], body }
}

describe('AgentResultBody', () => {
  it.each([false, true])('marks the report without marking properties or the body label when expanded is %s', (expanded) => {
    const body = ['NATIVE_AGENT_OUTPUT', ...Array.from({ length: 8 }, (_, index) => `report line ${index}`), 'NATIVE_AGENT_LAST'].join('\n')
    const { container } = render(() => <AgentResultBody source={{ ...source(body), bodyLabel: 'Report' }} context={{ getMessageUiState: () => expanded }} />)
    const outputs = container.querySelectorAll('[data-tool-output-preview]')
    expect(outputs).toHaveLength(1)
    expect(outputs[0]?.textContent).toContain('NATIVE_AGENT_OUTPUT')
    expect(outputs[0]?.textContent).not.toContain('Report')
    // A Markdown report always shows its full text. Collapse only adds the fade class.
    expect(outputs[0]?.textContent).toContain('NATIVE_AGENT_LAST')
    expect(outputs[0]?.classList.contains(toolResultCollapsed)).toBe(!expanded)
    expect(container.textContent).toContain('Report')
    // The property value repeats the report marker, so compare element positions, not text offsets.
    const property = [...container.querySelectorAll('div')].find(element => element.textContent?.startsWith('Agent ID:') && element.children.length === 2)
    if (!property || !outputs[0])
      throw new Error('The agent fixture requires its property row and its report.')
    expect(property.compareDocumentPosition(outputs[0]) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0)
  })

  it('does not certify metadata as output when the report is empty', () => {
    const { container } = render(() => <AgentResultBody source={source('')} />)
    expect(container.textContent).toContain('NATIVE_AGENT_OUTPUT')
    expect(container.querySelector('[data-tool-output-preview]')).toBeNull()
  })
})
