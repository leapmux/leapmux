import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { toolUseHeader } from '../toolStyles.css'
import { AgentRequestMessage } from './AgentRequestMessage'

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
