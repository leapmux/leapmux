import type { RenderContext } from '~/components/chat/messageRenderers'
import { beforeAll, describe, expect, it } from 'vitest'
import { checkKindModule } from '~/test-support/kindTestHarness'
import { elementText } from '~/test-support/messageRenderProbes'
import { toolCallIr } from '~/test-support/toolCallIr'
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
    const call = toolCallIr('agent', {
      request: { description: 'Subagent', prompt: 'Go.', registryKey: 'child-1' },
    })
    const context = { subagents: subagentsFrom({ backgroundTask: () => ({ title: 'Fix the failing build' }) as never }) } as unknown as RenderContext
    expect(elementText(agentRenderer.title(parsedCall(call), context))).toContain('Fix the failing build')
    // A key the registry does not answer for leaves the launch's own words.
    const empty = { subagents: subagentsFrom({ backgroundTask: () => undefined }) } as unknown as RenderContext
    expect(elementText(agentRenderer.title(parsedCall(call), empty))).toContain('Subagent')
  })

  checkKindModule({
    kind: 'agent',
    request: { description: 'Fix the build', agentType: 'ci-fix', prompt: 'Run the tests.' },
    titlePart: 'Fix the build',
    result: { agents: [{ description: 'Fix the build', agentId: 'a1', statusLabel: 'completed', outcome: 'completed', metadata: [], body: 'done' }] },
    resultPart: 'done',
  })
})
