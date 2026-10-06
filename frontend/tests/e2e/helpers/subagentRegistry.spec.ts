import { expect } from '@playwright/test'
import { test } from '../fixtures'
import { expectRegistryRow } from './subagentRegistry'

test('matches background task filters on each row before its nested content', async ({ page }) => {
  await page.setContent(`
    <div data-testid="section-header-background_tasks">Background tasks</div>
    <div id="shell-decoy" data-testid="bg-task-row" data-kind="shell" data-status="running">
      Shell task <span data-kind="subagent" data-status="completed">nested child label</span>
    </div>
    <div id="child-done" data-testid="bg-task-row" data-kind="subagent" data-status="completed">
      Completed child <span data-status="running">old progress text</span>
    </div>
    <div id="child-running" data-testid="bg-task-row" data-kind="subagent" data-status="running">Running child</div>
    <div id="workflow-done" data-testid="bg-task-row" data-kind="workflow" data-status="completed">Completed workflow</div>
    <div id="hidden-decoy" data-testid="bg-task-row">Hidden nested field <span style="display:none" data-kind="shell" data-status="paused">old state</span></div>
    <div id="legacy-row" data-testid="bg-task-row">Legacy row <span data-kind="shell" data-status="paused">nested fields</span></div>
  `)

  await expect(await expectRegistryRow(page, { kind: 'subagent' })).toHaveAttribute('id', 'child-done')
  await expect(await expectRegistryRow(page, { status: 'completed' })).toHaveAttribute('id', 'child-done')
  await expect(await expectRegistryRow(page, { kind: 'subagent', status: 'running' })).toHaveAttribute('id', 'child-running')
  await expect(await expectRegistryRow(page, { kind: 'workflow', status: 'completed' })).toHaveAttribute('id', 'workflow-done')
  await expect(await expectRegistryRow(page, { kind: 'shell', status: 'paused' })).toHaveAttribute('id', 'legacy-row')
  await expect(await expectRegistryRow(page, { kind: 'shell', status: 'paused', titleContains: 'Legacy row' })).toHaveAttribute('id', 'legacy-row')
})
