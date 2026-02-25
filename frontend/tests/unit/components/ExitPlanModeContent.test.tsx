import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { ExitPlanModeContent } from '~/components/chat/controls/ExitPlanModeControl'

function makeRequest(allowedPrompts?: Array<{ tool: string, prompt: string }>): ControlRequest {
  return {
    requestId: 'req-1',
    agentId: 'agent-1',
    payload: {
      request: {
        tool_name: 'ExitPlanMode',
        input: allowedPrompts ? { allowedPrompts } : {},
      },
    },
  }
}

describe('exitPlanModeContent', () => {
  it('shows fallback message when no permissions are requested', () => {
    render(() => <ExitPlanModeContent request={makeRequest()} />)

    expect(screen.getByText('Plan Ready for Review')).toBeTruthy()
    expect(screen.getByText('The agent has finished planning and is ready to proceed.')).toBeTruthy()
  })

  it('groups permissions by tool name', () => {
    const prompts = [
      { tool: 'Bash', prompt: 'run tests' },
      { tool: 'Bash', prompt: 'run build' },
      { tool: 'Bash', prompt: 'install deps' },
    ]
    render(() => <ExitPlanModeContent request={makeRequest(prompts)} />)

    expect(screen.getByText('Requested permissions:')).toBeTruthy()
    // All Bash prompts should be joined in a single list item
    expect(screen.getByText(/run tests, run build, install deps/)).toBeTruthy()
  })

  it('renders separate groups for different tools', () => {
    const prompts = [
      { tool: 'Bash', prompt: 'run tests' },
      { tool: 'Read', prompt: 'read config.json' },
      { tool: 'Bash', prompt: 'run build' },
    ]
    render(() => <ExitPlanModeContent request={makeRequest(prompts)} />)

    // Bash group should have both prompts joined
    expect(screen.getByText(/run tests, run build/)).toBeTruthy()
    // Read group should be separate
    expect(screen.getByText(/read config.json/)).toBeTruthy()
  })

  it('does not show collapsible toggle with 3 or fewer groups', () => {
    const prompts = [
      { tool: 'Bash', prompt: 'run tests' },
      { tool: 'Read', prompt: 'read file' },
      { tool: 'Write', prompt: 'write file' },
    ]
    render(() => <ExitPlanModeContent request={makeRequest(prompts)} />)

    // No toggle button should be present
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('shows collapsible toggle with more than 3 groups', () => {
    const prompts = [
      { tool: 'Bash', prompt: 'run tests' },
      { tool: 'Read', prompt: 'read file' },
      { tool: 'Write', prompt: 'write file' },
      { tool: 'Grep', prompt: 'search code' },
      { tool: 'Glob', prompt: 'find files' },
    ]
    render(() => <ExitPlanModeContent request={makeRequest(prompts)} />)

    const toggle = screen.getByRole('button')
    expect(toggle.textContent).toContain('Show 2 more')
  })

  it('expands all groups when toggle is clicked', () => {
    const prompts = [
      { tool: 'Bash', prompt: 'run tests' },
      { tool: 'Read', prompt: 'read file' },
      { tool: 'Write', prompt: 'write file' },
      { tool: 'Grep', prompt: 'search code' },
      { tool: 'Glob', prompt: 'find files' },
    ]
    render(() => <ExitPlanModeContent request={makeRequest(prompts)} />)

    fireEvent.click(screen.getByRole('button'))

    expect(screen.getByText(/search code/)).toBeTruthy()
    expect(screen.getByText(/find files/)).toBeTruthy()
    expect(screen.getByRole('button').textContent).toBe('Show less')
  })
})
