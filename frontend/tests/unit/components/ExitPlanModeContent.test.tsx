import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { ExitPlanModeContent } from '~/components/chat/controls/ExitPlanModeControl'

const BASH_ALL_PROMPTS_RE = /run tests, run build, install deps/
const BASH_TWO_PROMPTS_RE = /run tests, run build/
const READ_CONFIG_RE = /read config.json/
const SEARCH_CODE_RE = /search code/
const FIND_FILES_RE = /find files/

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

    expect(screen.getByText('Plan Ready for Review')).toBeInTheDocument()
    expect(screen.getByText('The agent has finished planning and is ready to proceed.')).toBeInTheDocument()
  })

  it('groups permissions by tool name', () => {
    const prompts = [
      { tool: 'Bash', prompt: 'run tests' },
      { tool: 'Bash', prompt: 'run build' },
      { tool: 'Bash', prompt: 'install deps' },
    ]
    render(() => <ExitPlanModeContent request={makeRequest(prompts)} />)

    expect(screen.getByText('Requested permissions:')).toBeInTheDocument()
    // All Bash prompts should be joined in a single list item
    expect(screen.getByText(BASH_ALL_PROMPTS_RE)).toBeInTheDocument()
  })

  it('renders separate groups for different tools', () => {
    const prompts = [
      { tool: 'Bash', prompt: 'run tests' },
      { tool: 'Read', prompt: 'read config.json' },
      { tool: 'Bash', prompt: 'run build' },
    ]
    render(() => <ExitPlanModeContent request={makeRequest(prompts)} />)

    // Bash group should have both prompts joined
    expect(screen.getByText(BASH_TWO_PROMPTS_RE)).toBeInTheDocument()
    // Read group should be separate
    expect(screen.getByText(READ_CONFIG_RE)).toBeInTheDocument()
  })

  it('does not show collapsible toggle with 3 or fewer groups', () => {
    const prompts = [
      { tool: 'Bash', prompt: 'run tests' },
      { tool: 'Read', prompt: 'read file' },
      { tool: 'Write', prompt: 'write file' },
    ]
    render(() => <ExitPlanModeContent request={makeRequest(prompts)} />)

    // No toggle button should be present
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
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
    expect(toggle).toHaveTextContent('Show 2 more')
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

    expect(screen.getByText(SEARCH_CODE_RE)).toBeInTheDocument()
    expect(screen.getByText(FIND_FILES_RE)).toBeInTheDocument()
    expect(screen.getByRole('button')).toHaveTextContent('Show less')
  })
})
