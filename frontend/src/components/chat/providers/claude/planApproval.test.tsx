import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { ClaudePlanApprovalContent } from './planApproval'

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

describe('claude plan approval content', () => {
  it('keeps the full plan out of the approval area', () => {
    const request = makeRequest([{ tool: 'Bash', prompt: 'run tests' }])
    request.payload.request = { tool_name: 'ExitPlanMode', input: { plan: '# Proposed change\n\n- Keep **original bytes**.', allowedPrompts: [{ tool: 'Bash', prompt: 'run tests' }] } }
    render(() => <ClaudePlanApprovalContent request={request} />)
    expect(screen.queryByRole('heading', { name: 'Proposed change' })).toBeNull()
    expect(screen.queryByText('original bytes')).toBeNull()
    expect(screen.getByText('Bash: run tests')).toBeInTheDocument()
  })

  it('ignores malformed permission entries and keeps valid permissions', () => {
    const request = makeRequest()
    request.payload.request = { tool_name: 'ExitPlanMode', input: { plan: 'Review this plan.', allowedPrompts: [null, {}, { tool: 'Bash' }, { tool: 'Bash', prompt: 'run tests' }] } }
    render(() => <ClaudePlanApprovalContent request={request} />)
    expect(screen.queryByText('Review this plan.')).toBeNull()
    expect(screen.getByText('Bash: run tests')).toBeInTheDocument()
  })

  it('shows fallback message when no permissions are requested', () => {
    render(() => <ClaudePlanApprovalContent request={makeRequest()} />)

    expect(screen.getByText('Plan Ready for Review')).toBeInTheDocument()
    expect(screen.getByText('The agent finished planning and is ready to proceed.')).toBeInTheDocument()
  })

  it('groups permissions by tool name', () => {
    const prompts = [
      { tool: 'Bash', prompt: 'run tests' },
      { tool: 'Bash', prompt: 'run build' },
      { tool: 'Bash', prompt: 'install deps' },
    ]
    render(() => <ClaudePlanApprovalContent request={makeRequest(prompts)} />)

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
    render(() => <ClaudePlanApprovalContent request={makeRequest(prompts)} />)

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
    render(() => <ClaudePlanApprovalContent request={makeRequest(prompts)} />)

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
    render(() => <ClaudePlanApprovalContent request={makeRequest(prompts)} />)

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
    render(() => <ClaudePlanApprovalContent request={makeRequest(prompts)} />)

    fireEvent.click(screen.getByRole('button'))

    expect(screen.getByText(SEARCH_CODE_RE)).toBeInTheDocument()
    expect(screen.getByText(FIND_FILES_RE)).toBeInTheDocument()
    expect(screen.getByRole('button')).toHaveTextContent('Show less')
  })
})
