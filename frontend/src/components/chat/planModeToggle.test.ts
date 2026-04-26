import { describe, expect, it } from 'vitest'
import { decidePlanModeToggle } from './planModeToggle'

describe('decidePlanModeToggle', () => {
  it('switches into plan mode and remembers the previous mode', () => {
    const decision = decidePlanModeToggle({
      currentMode: 'default',
      planValue: 'plan',
      previousNonPlanMode: 'default',
    })
    expect(decision.nextMode).toBe('plan')
    expect(decision.updatePreviousNonPlanMode).toBe('default')
  })

  it('returning to default after a Default → Plan toggle restores Default', () => {
    // Step 1: leave Default → Plan, remember 'default'.
    let prev = 'default'
    const enterPlan = decidePlanModeToggle({
      currentMode: 'default',
      planValue: 'plan',
      previousNonPlanMode: prev,
    })
    expect(enterPlan.nextMode).toBe('plan')
    if (enterPlan.updatePreviousNonPlanMode !== undefined)
      prev = enterPlan.updatePreviousNonPlanMode

    // Step 2: leave Plan → Default. Don't update the remembered value.
    const leavePlan = decidePlanModeToggle({
      currentMode: 'plan',
      planValue: 'plan',
      previousNonPlanMode: prev,
    })
    expect(leavePlan.nextMode).toBe('default')
    expect(leavePlan.updatePreviousNonPlanMode).toBeUndefined()
  })

  it('toggling from Accept Edits → Plan → toggle returns to Accept Edits, not Default', () => {
    // Mirror of the e2e "toggle back to non-default mode" assertion: after
    // switching to acceptEdits via the dropdown, Shift+Tab → Plan should
    // remember acceptEdits, and the next Shift+Tab returns there (not default).
    let prev = 'default'

    // User chose 'acceptEdits' via dropdown (no toggle yet).
    // Now they Shift+Tab from Accept Edits → Plan.
    const enterPlan = decidePlanModeToggle({
      currentMode: 'acceptEdits',
      planValue: 'plan',
      previousNonPlanMode: prev,
    })
    expect(enterPlan.nextMode).toBe('plan')
    expect(enterPlan.updatePreviousNonPlanMode).toBe('acceptEdits')
    if (enterPlan.updatePreviousNonPlanMode !== undefined)
      prev = enterPlan.updatePreviousNonPlanMode

    // Shift+Tab again → Plan → Accept Edits (NOT Default).
    const leavePlan = decidePlanModeToggle({
      currentMode: 'plan',
      planValue: 'plan',
      previousNonPlanMode: prev,
    })
    expect(leavePlan.nextMode).toBe('acceptEdits')
  })

  it('does not overwrite previousNonPlanMode when leaving plan mode', () => {
    const decision = decidePlanModeToggle({
      currentMode: 'plan',
      planValue: 'plan',
      previousNonPlanMode: 'bypassPermissions',
    })
    expect(decision.nextMode).toBe('bypassPermissions')
    expect(decision.updatePreviousNonPlanMode).toBeUndefined()
  })

  it('uses provider-specific plan values verbatim', () => {
    // Some providers represent plan mode with a different identifier; the
    // function should be opaque to that — it only compares to planValue.
    const decision = decidePlanModeToggle({
      currentMode: 'codex.plan',
      planValue: 'codex.plan',
      previousNonPlanMode: 'codex.default',
    })
    expect(decision.nextMode).toBe('codex.default')
    expect(decision.updatePreviousNonPlanMode).toBeUndefined()
  })

  it('full Default → Plan → Default round-trip leaves remembered mode at default', () => {
    let prev = 'default'

    const a = decidePlanModeToggle({ currentMode: 'default', planValue: 'plan', previousNonPlanMode: prev })
    if (a.updatePreviousNonPlanMode !== undefined)
      prev = a.updatePreviousNonPlanMode
    expect(a.nextMode).toBe('plan')

    const b = decidePlanModeToggle({ currentMode: 'plan', planValue: 'plan', previousNonPlanMode: prev })
    expect(b.nextMode).toBe('default')
    if (b.updatePreviousNonPlanMode !== undefined)
      prev = b.updatePreviousNonPlanMode

    expect(prev).toBe('default')
  })
})
