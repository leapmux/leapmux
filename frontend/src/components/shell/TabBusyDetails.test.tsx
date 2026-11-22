import type { TabBusyAction } from './TabBusyDetails'
import { create } from '@bufbuild/protobuf'
import { cleanup, render, screen } from '@solidjs/testing-library'
import { afterEach, describe, expect, it } from 'vitest'
import { TerminalProcessSchema } from '~/generated/proto/leapmux/v1/terminal_pb'
import { TabBusyDetails } from './TabBusyDetails'

afterEach(cleanup)

describe('TabBusyDetails', () => {
  it('renders action-specific wording for close-tab, quit, and switch-mode', () => {
    const cases: Array<{ action?: TabBusyAction, phrase: string }> = [
      { phrase: 'Closing the tab' },
      { action: 'quit', phrase: 'Quitting' },
      { action: 'switch-mode', phrase: 'Switching mode' },
    ]

    for (const { action, phrase } of cases) {
      render(() => (
        <TabBusyDetails
          reason={{
            kind: 'agent-work',
            activeBackgroundTasks: ['Run tests'],
            activeChildAgents: [],
          }}
          {...(action !== undefined
            ? { action }
            : {})}
        />
      ))
      expect(screen.getByText(`This agent is still working. ${phrase} stops it.`)).toBeInTheDocument()
      cleanup()

      render(() => (
        <TabBusyDetails
          reason={{
            kind: 'terminal-processes',
            processes: [
              { pid: 10, name: 'vite' },
            ],
            totalCount: 1,
          }}
          {...(action !== undefined
            ? { action }
            : {})}
        />
      ))
      expect(
        screen.getByText(`1 process still runs in this terminal. ${phrase} stops it.`),
      ).toBeInTheDocument()
      cleanup()

      render(() => (
        <TabBusyDetails
          reason={{
            kind: 'terminal-processes',
            processes: [
              { pid: 10, name: 'node' },
              { pid: 11, name: 'esbuild' },
            ],
            totalCount: 2,
          }}
          {...(action !== undefined
            ? { action }
            : {})}
        />
      ))
      expect(
        screen.getByText(`2 processes still run in this terminal. ${phrase} stops them.`),
      ).toBeInTheDocument()
      cleanup()
    }
  })

  it('reports the total process count in the summary when the worker caps listed processes', () => {
    render(() => (
      <TabBusyDetails
        reason={{
          kind: 'terminal-processes',
          processes: [
            create(TerminalProcessSchema, { pid: 1, name: 'cc' }),
          ],
          totalCount: 48,
        }}
      />
    ))

    expect(
      screen.getByText('48 processes still run in this terminal. Closing the tab stops them.'),
    ).toBeInTheDocument()
    const rows = screen.getByTestId('busy-processes').querySelectorAll('li')
    expect([...rows].map(li => li.textContent)).toEqual(['cc (pid 1)', 'and 47 more'])
  })

  it('falls back to listed process length and unnamed process label when totalCount is smaller and name is empty', () => {
    render(() => (
      <TabBusyDetails
        reason={{
          kind: 'terminal-processes',
          processes: [
            { pid: 10, name: '' },
            { pid: 11, name: 'sleep' },
          ],
          totalCount: 0,
        }}
        action="quit"
      />
    ))

    expect(
      screen.getByText('2 processes still run in this terminal. Quitting stops them.'),
    ).toBeInTheDocument()
    const rows = screen.getByTestId('busy-processes').querySelectorAll('li')
    expect([...rows].map(li => li.textContent)).toEqual([
      'unnamed process (pid 10)',
      'sleep (pid 11)',
    ])
  })
})
