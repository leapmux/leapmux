/** @vitest-environment jsdom */
import type { Page } from '@playwright/test'
import type { ModelScript } from './modelScriptFixture'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectOpaqueNativeWorkflowResult, workflowGroupHeadingElement } from './workflowGrouping'

describe('workflowGroupHeadingElement', () => {
  it('keeps equal heading text in separate groups', () => {
    document.body.innerHTML = `
      <div>goal</div>
      <div id="run" data-testid="bg-task-row"></div>
      <div id="step" data-testid="bg-task-row"></div>
      <div>goal</div>
      <div id="other" data-testid="bg-task-row"></div>
    `
    const run = document.getElementById('run')
    const step = document.getElementById('step')
    const other = document.getElementById('other')
    if (!run || !step || !other)
      throw new Error('the workflow test rows are absent')
    const firstHeading = workflowGroupHeadingElement(run)
    expect(firstHeading?.textContent?.trim()).toBe('goal')
    expect(workflowGroupHeadingElement(step)).toBe(firstHeading)
    expect(workflowGroupHeadingElement(other)).not.toBe(firstHeading)
  })

  it('returns no heading when a row has no preceding element', () => {
    document.body.innerHTML = '<div id="first" data-testid="bg-task-row"></div>'
    const first = document.getElementById('first')
    if (!first)
      throw new Error('the workflow test row is absent')
    expect(workflowGroupHeadingElement(first)).toBeNull()
  })
})

describe('expectOpaqueNativeWorkflowResult', () => {
  it.each([{ ruleNames: [] }, { ruleNames: ['one'] }, { ruleNames: ['same', 'same'] }])('requires two distinct native assignment rules before browser access: $ruleNames', async ({ ruleNames }) => {
    const context: ManagedNativeScenarioContext = {
      provider: AgentProvider.CODEWHALE,
      workspaceId: 'workflow-boundary',
      leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' },
      get page(): Page {
        throw new Error('The assignment boundary must run before browser access.')
      },
      get modelScript(): ModelScript {
        throw new Error('The assignment boundary must run before model access.')
      },
    }
    await expect(expectOpaqueNativeWorkflowResult(context, { ruleNames, heading: 'Workflow' })).rejects.toThrow('requires two distinct assignment rules')
  })
})
