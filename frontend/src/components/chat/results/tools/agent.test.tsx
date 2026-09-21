import type { ToolResultRenderContext } from '~/components/chat/renderContext'
import { render } from '@solidjs/testing-library'
import { beforeAll, describe, expect, it } from 'vitest'
import { ToolMessage } from '~/components/chat/results/ToolMessage'
import { checkKindModule } from '~/test-support/kindTestHarness'
import { elementText } from '~/test-support/messageRenderProbes'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'
import { subagentsFrom } from '../../renderContext'
import { agentRenderer } from './agent'
import { parsedCall } from './renderer'

// jsdom does not provide ResizeObserver, which the shared layouts observe with.
beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
})

describe('agent renderer', () => {
  // The registry knows what the subagent DOES, where a launch may hold the tool's own
  // label alone (Codex's `spawnAgent` calls itself "Subagent"). The row then reads the
  // same as the Background tasks entry it points at.
  it('prefers the background-task title over the launch description', () => {
    const call = toolCallFixture('agent', {
      request: { description: 'Subagent', prompt: 'Go.', registryKey: 'child-1' },
    })
    const navigation = subagentsFrom({ backgroundTask: () => ({ title: 'Fix the failing build' }) as never })
    if (navigation === undefined)
      throw new Error('The subagent test needs navigation capabilities')
    const context: ToolResultRenderContext = { subagents: navigation }
    expect(elementText(agentRenderer.title(parsedCall(call), context))).toContain('Fix the failing build')
    // A key the registry does not answer for leaves the launch's own words.
    const emptyNavigation = subagentsFrom({ backgroundTask: () => undefined })
    if (emptyNavigation === undefined)
      throw new Error('The subagent test needs empty navigation capabilities')
    const empty: ToolResultRenderContext = { subagents: emptyNavigation }
    expect(elementText(agentRenderer.title(parsedCall(call), empty))).toContain('Subagent')
  })

  it('draws a completed outcome when a paired result lists no agents', () => {
    const call = toolCallFixture('agent', {
      request: { description: 'Wait for agents', prompt: '' },
      result: { agents: [] },
    })
    const { container } = render(() => <ToolMessage row={toolRow(call, 'result', { request: true })} />)

    expect(container.textContent).toContain('Completed')
    expect(container.firstElementChild).not.toBeNull()
  })

  it('keeps the failed outcome for an empty paired result', () => {
    const call = toolCallFixture('agent', {
      status: 'failed',
      request: { description: 'Wait for agents', prompt: '' },
      result: { agents: [] },
    })
    const { container } = render(() => <ToolMessage row={toolRow(call, 'result', { request: true })} />)

    expect(container.textContent).toContain('Error')
    expect(container.textContent).not.toContain('Completed')
  })

  checkKindModule({
    kind: 'agent',
    request: { description: 'Fix the build', agentType: 'ci-fix', prompt: 'Run the tests.' },
    titlePart: 'Fix the build',
    result: { agents: [{ description: 'Fix the build', agentId: 'a1', statusLabel: 'completed', outcome: 'completed', metadata: [], body: 'done' }] },
    resultPart: 'done',
  })
})
