import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { ToolMetadata } from './ToolMetadata'

describe('ToolMetadata', () => {
  it('lists each fact under its label', () => {
    const { container } = render(() => (
      <ToolMetadata items={[
        { label: 'Shell ID', value: 'shell-1' },
      ]}
      />
    ))
    expect(container.textContent).toBe('Shell ID:shell-1')
  })

  // A command states its working directory relative to the agent's, on whichever row
  // draws it.
  it('shows a path relative to the working directory', () => {
    const { container } = render(() => (
      <ToolMetadata
        items={[
          { label: 'Working directory', value: '/home/me/project/sub', path: true },
        ]}
        workingDir="/home/me/project"
        homeDir="/home/me"
      />
    ))
    expect(container.textContent).toBe('Working directory:sub')
  })

  it('shows a value that is no path as it is', () => {
    const { container } = render(() => (
      <ToolMetadata
        items={[
          { label: 'Shell ID', value: '/home/me/project/sub' },
        ]}
        workingDir="/home/me/project"
      />
    ))
    expect(container.textContent).toBe('Shell ID:/home/me/project/sub')
  })

  it('draws nothing for no facts', () => {
    expect(render(() => <ToolMetadata items={[]} />).container.innerHTML).toBe('')
  })
})
