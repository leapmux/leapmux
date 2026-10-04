import type { ControlRequest } from '~/stores/control.store'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { prettifyJson } from '~/lib/jsonFormat'
import { PermissionRequestContent } from './PermissionRequestContent'

const PERMISSION_REQUIRED_RE = /Permission Required/
const BASH_RE = /Bash/
const KEY_19_RE = /key_19/

const REQUEST: ControlRequest = { requestId: 'req-1', agentId: 'agent-1', payload: {} }

/** Twenty keys, which is past the point where the body offers an expansion control. */
function longInput(): Record<string, string> {
  const input: Record<string, string> = {}
  for (let i = 0; i < 20; i++) {
    input[`key_${i}`] = `value_${i}`
  }
  return input
}

function renderBody(source: { title?: string, command?: string, input?: unknown, text?: string }) {
  return render(() => <PermissionRequestContent request={REQUEST} source={source} />)
}

/**
 * The argument block, which is the LAST `pre` the body draws.
 *
 * A command draws its own `pre` above the arguments, so the first one is the command
 * whenever the request states one.
 */
function argumentsText(container: HTMLElement): string | undefined {
  const blocks = container.querySelectorAll('pre')
  return blocks[blocks.length - 1]?.textContent ?? undefined
}

describe('PermissionRequestContent', () => {
  // A runtime can state a request in prose, such as a subagent's plan. The body
  // draws it as markdown, not as a JSON string among the arguments.
  it('draws the text of the request as markdown', () => {
    const { container } = renderBody({ title: 'ExitPlanMode', text: '# The plan\n\n- First step' })
    expect(container.querySelector('h1')?.textContent).toBe('The plan')
    expect(container.querySelector('li')?.textContent).toBe('First step')
    expect(container.querySelector('pre')).toBeNull()
  })

  it('uses Fractured JSON for the remaining tool arguments', () => {
    const { container } = renderBody({ title: 'Bash', input: { timeout: 0, quiet: false } })
    expect(container.querySelector('pre')?.textContent).toBe(prettifyJson({ timeout: 0, quiet: false }))
  })

  it('renders the tool name and a short command without a toggle', () => {
    renderBody({ title: 'Bash', command: 'ls', input: { command: 'ls' } })

    expect(screen.getByText(PERMISSION_REQUIRED_RE)).toBeInTheDocument()
    expect(screen.getByText(BASH_RE)).toBeInTheDocument()
    // A short command needs no expansion control.
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })

  // One command, shown once: the code block above the arguments is the whole ask, so
  // the key it repeats is dropped from the JSON below it.
  it('drops the command key the code block already states', () => {
    const { container } = renderBody({ title: 'Bash', command: 'ls', input: { command: 'ls', timeout: 5 } })
    expect(argumentsText(container)).toBe(prettifyJson({ timeout: 5 }))
  })

  // A DIFFERENT command is not the same ask, so both stay: dropping the key would
  // hide the argument the call actually runs with.
  it('keeps a command argument that differs from the one it draws', () => {
    const { container } = renderBody({ title: 'Bash', command: 'ls -al', input: { command: 'ls', timeout: 5 } })
    expect(argumentsText(container)).toBe(prettifyJson({ command: 'ls', timeout: 5 }))
  })

  it('truncates long JSON and shows toggle', () => {
    renderBody({ title: 'Bash', input: longInput() })

    const toggle = screen.getByRole('button')
    expect(toggle).toHaveTextContent('more line')
  })

  it('expands long JSON when toggle is clicked', () => {
    renderBody({ title: 'Bash', input: longInput() })

    fireEvent.click(screen.getByRole('button'))

    // Expansion shows every key.
    expect(screen.getByText(KEY_19_RE)).toBeInTheDocument()
    expect(screen.getByRole('button')).toHaveTextContent('Show less')
  })

  it('states the working directory the call runs in', () => {
    const { container } = render(() => (
      <PermissionRequestContent request={REQUEST} source={{ title: 'Bash', workingDirectory: '/srv/app' }} />
    ))
    expect(container.textContent ?? '').toContain('/srv/app')
  })

  // An empty argument object states nothing a reader can weigh, so the body draws no
  // JSON block at all rather than an empty one.
  it('draws no JSON block for empty arguments', () => {
    const { container } = renderBody({ title: 'Read', input: {} })
    expect(container.querySelector('pre')).toBeNull()
  })
})
