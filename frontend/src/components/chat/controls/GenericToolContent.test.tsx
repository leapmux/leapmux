import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { GenericToolContent } from '~/components/chat/controls/GenericToolControl'

const PERMISSION_REQUIRED_RE = /Permission Required:/
const BASH_RE = /Bash/
const KEY_19_RE = /key_19/

function makeRequest(input: Record<string, unknown>): ControlRequest {
  return {
    requestId: 'req-1',
    agentId: 'agent-1',
    payload: {
      request: { tool_name: 'Bash', input },
    },
  }
}

describe('genericToolContent', () => {
  it('renders tool name and short JSON without toggle', () => {
    render(() => <GenericToolContent request={makeRequest({ command: 'ls' })} />)

    expect(screen.getByText(PERMISSION_REQUIRED_RE)).toBeInTheDocument()
    expect(screen.getByText(BASH_RE)).toBeInTheDocument()
    // Short JSON should be fully visible with no toggle
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })

  it('truncates long JSON and shows toggle', () => {
    const longInput: Record<string, string> = {}
    for (let i = 0; i < 20; i++) {
      longInput[`key_${i}`] = `value_${i}`
    }
    render(() => <GenericToolContent request={makeRequest(longInput)} />)

    const toggle = screen.getByRole('button')
    expect(toggle).toHaveTextContent('more line')
  })

  it('expands long JSON when toggle is clicked', () => {
    const longInput: Record<string, string> = {}
    for (let i = 0; i < 20; i++) {
      longInput[`key_${i}`] = `value_${i}`
    }
    render(() => <GenericToolContent request={makeRequest(longInput)} />)

    fireEvent.click(screen.getByRole('button'))

    // All keys should now be visible
    expect(screen.getByText(KEY_19_RE)).toBeInTheDocument()
    expect(screen.getByRole('button')).toHaveTextContent('Show less')
  })
})
