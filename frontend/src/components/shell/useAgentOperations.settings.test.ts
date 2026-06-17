import type { AvailableOptionGroup } from '~/generated/leapmux/v1/agent_pb'
import type { createTabStore } from '~/stores/tab.store'
import { create } from '@bufbuild/protobuf'
import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { showWarnToast } from '~/components/common/Toast'
import { AvailableOptionGroupSchema, AvailableOptionSchema } from '~/generated/leapmux/v1/agent_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createTabStore as makeTabStore } from '~/stores/tab.store'

// Drives the optimistic-vs-settled reconciliation: the mock stands in for the
// worker, mutating the tab's catalog mid-RPC (what the status push does) before
// resolving the response.
const updateAgentSettings = vi.fn()

vi.mock('~/api/workerRpc', () => ({
  updateAgentSettings: (workerId: string, req: unknown) => updateAgentSettings(workerId, req),
}))

// The rollback path surfaces a toast; stub it so the failure-case test doesn't
// reach the real DOM-bound toast renderer.
vi.mock('~/components/common/Toast', () => ({
  showWarnToast: vi.fn(),
}))

const { useAgentOperations } = await import('./useAgentOperations')

function opt(id: string, name: string) {
  return create(AvailableOptionSchema, { id, name })
}

function modelGroup(currentValue: string) {
  return create(AvailableOptionGroupSchema, {
    id: 'model',
    label: 'Model',
    order: 10,
    mutable: true,
    currentValue,
    defaultValue: 'default',
    options: [
      opt('default', 'Default (recommended)'),
      opt('fable[1m]', 'Fable 5'),
      opt('opus[1m]', 'Opus (1M context)'),
      opt('sonnet', 'Sonnet'),
    ],
  })
}

function effortGroup(ids: string[], currentValue: string, defaultValue: string) {
  return create(AvailableOptionGroupSchema, {
    id: 'effort',
    label: 'Effort',
    order: 20,
    mutable: true,
    currentValue,
    defaultValue,
    options: ids.map(id => opt(id, id)),
  })
}

const permissionGroup = create(AvailableOptionGroupSchema, {
  id: 'permissionMode',
  label: 'Permission Mode',
  order: 90,
  mutable: true,
  currentValue: 'default',
  options: [opt('default', 'Default')],
})

// The tab's starting catalog before any switch (running on Opus[1m] at xhigh). The
// reconcile now reads the settled values from the RPC reply, not this catalog, so the
// tests drive the resolved model/effort through updateAgentSettings' confirmedOptions.
function opusCatalog(): AvailableOptionGroup[] {
  return [modelGroup('opus[1m]'), effortGroup(['auto', 'high', 'xhigh', 'ultracode', 'max'], 'xhigh', 'high'), permissionGroup]
}

function stubProps(tabStore: ReturnType<typeof createTabStore>) {
  return {
    tabStore,
    settingsLoading: { start: () => {}, stop: () => {} },
    getCurrentTabContext: () => ({ workerId: '', workingDir: '' }),
    agentSessionStore: {},
    chatStore: {},
    controlStore: {},
    layoutStore: {},
    isActiveWorkspaceMutatable: () => true,
    activeWorkspace: () => null,
    newAgentDialog: {},
    setNewAgentLoadingProvider: () => {},
  } as unknown as Parameters<typeof useAgentOperations>[0]
}

async function runChange(
  tabStore: ReturnType<typeof createTabStore>,
  change: { groupKey: string, value: string },
) {
  await createRoot(async (dispose) => {
    const ops = useAgentOperations(stubProps(tabStore))
    await ops.handleAgentSettingChange('a1', { sets: { [change.groupKey]: change.value } })
    dispose()
  })
}

describe('useAgentOperations settings reconciliation', () => {
  beforeEach(() => {
    updateAgentSettings.mockReset()
    vi.mocked(showWarnToast).mockClear()
  })

  it('snaps the optimistic account-default sentinel to the settled concrete model', async () => {
    const tabStore = makeTabStore()
    tabStore.addTab({
      type: TabType.AGENT,
      id: 'a1',
      workerId: 'w1',
      optionValues: { model: 'opus[1m]', effort: 'xhigh', permissionMode: 'default' },
      optionGroups: opusCatalog(),
    } as never)

    // The worker relaunches without --model, the CLI resolves "default" to a concrete
    // Sonnet, and the RPC reply carries the settled options (model + the effort the
    // relaunch reset to). The reconcile reads those directly -- no dependence on the
    // status broadcast arriving first.
    updateAgentSettings.mockResolvedValue({ confirmedOptions: { model: 'sonnet', effort: 'high' } })

    await runChange(tabStore, { groupKey: 'model', value: 'default' })

    expect(updateAgentSettings).toHaveBeenCalledWith('w1', {
      agentId: 'a1',
      settings: { options: { model: 'default' } },
    })
    const tab = tabStore.getAgentTab('a1')
    // Without the post-RPC reconciliation, the optimistic model would be frozen on
    // "default" (rendered as "Default (recommended)" with no effort group). It must
    // settle on the resolved concrete model instead.
    expect(tab?.optionValues?.model).toBe('sonnet')
    // The effort axis comes back with it (the sentinel carries none), resolved
    // to Sonnet's confirmed level rather than the carried-over xhigh.
    expect(tab?.optionValues?.effort).toBe('high')
  })

  it('leaves a live-apply effort change on its optimistic value (no stale revert)', async () => {
    const tabStore = makeTabStore()
    tabStore.addTab({
      type: TabType.AGENT,
      id: 'a1',
      workerId: 'w1',
      optionValues: { model: 'opus[1m]', effort: 'high', permissionMode: 'default' },
      optionGroups: opusCatalog(),
    } as never)

    // A pure effort change is live-applied and confirms its requested value verbatim.
    // The reconcile is scoped to MODEL changes, so an effort change ignores the reply's
    // confirmedOptions entirely -- even when (as here) the reply echoes a stale "xhigh".
    updateAgentSettings.mockResolvedValue({ confirmedOptions: { effort: 'xhigh' } })

    await runChange(tabStore, { groupKey: 'effort', value: 'max' })

    // The optimistic "max" is preserved (a later status push reconciles it), not
    // snapped back to the reply's stale "xhigh".
    expect(tabStore.getAgentTab('a1')?.optionValues?.effort).toBe('max')
  })

  it('snaps the model but leaves effort untouched when switching to an effort-less model', async () => {
    const tabStore = makeTabStore()
    tabStore.addTab({
      type: TabType.AGENT,
      id: 'a1',
      workerId: 'w1',
      optionValues: { model: 'opus[1m]', effort: 'xhigh', permissionMode: 'default' },
      optionGroups: opusCatalog(),
    } as never)

    // Switching to Haiku relaunches onto a model with no effort tiers, so the reply's
    // confirmed options carry the concrete model but NO effort key. The reconcile must
    // still snap the optimistic model to Haiku, but with no confirmed effort to read it
    // must leave the prior effort on its value rather than wiping it.
    updateAgentSettings.mockResolvedValue({ confirmedOptions: { model: 'haiku' } })

    await runChange(tabStore, { groupKey: 'model', value: 'haiku' })

    const tab = tabStore.getAgentTab('a1')
    // model and effort live in ONE map: the model snaps to haiku while the prior
    // effort rides along untouched (no effort row to reconcile from). A naive
    // whole-map replacement would have dropped effort -- this guards the spread.
    expect(tab?.optionValues?.model).toBe('haiku')
    expect(tab?.optionValues?.effort).toBe('xhigh')
  })

  it('rolls back a failed change by deleting the key when it had no prior value', async () => {
    const tabStore = makeTabStore()
    tabStore.addTab({
      type: TabType.AGENT,
      id: 'a1',
      workerId: 'w1',
      // permissionMode is absent from optionValues -- the catalog's currentValue
      // is authoritative for it.
      optionValues: { model: 'opus[1m]' },
      optionGroups: opusCatalog(),
    } as never)

    updateAgentSettings.mockRejectedValue(new Error('rpc failed'))

    await runChange(tabStore, { groupKey: 'permissionMode', value: 'plan' })

    const tab = tabStore.getAgentTab('a1')
    // The optimistic 'plan' is rolled back by DELETING the key (not writing ''),
    // so agentTabOptionGroups falls through to the catalog's confirmed value
    // instead of blanking the group with a spurious empty override.
    expect(tab?.optionValues && 'permissionMode' in tab.optionValues).toBe(false)
  })

  it('a failed change does not clobber a newer same-key change made while it was in flight', async () => {
    const tabStore = makeTabStore()
    tabStore.addTab({
      type: TabType.AGENT,
      id: 'a1',
      workerId: 'w1',
      optionValues: { model: 'opus[1m]', effort: 'high', permissionMode: 'default' },
      optionGroups: opusCatalog(),
    } as never)

    // Change A (effort -> 'max') hangs on its RPC; change B (effort -> 'low') resolves
    // immediately while A is still in flight.
    let rejectA: (e: Error) => void = () => {}
    const aPending = new Promise((_resolve, reject) => {
      rejectA = reject
    })
    updateAgentSettings
      .mockReturnValueOnce(aPending)
      .mockResolvedValueOnce({ confirmedOptions: {} })

    await createRoot(async (dispose) => {
      const ops = useAgentOperations(stubProps(tabStore))
      // A: optimistic write effort='max', RPC hangs (not awaited).
      const aDone = ops.handleAgentSettingChange('a1', { sets: { effort: 'max' } })
      expect(tabStore.getAgentTab('a1')?.optionValues?.effort).toBe('max')
      // B: optimistic write effort='low', RPC resolves -> B is the newest selection.
      await ops.handleAgentSettingChange('a1', { sets: { effort: 'low' } })
      expect(tabStore.getAgentTab('a1')?.optionValues?.effort).toBe('low')
      // A now fails. Its rollback must NOT restore A's prior 'high' over B's 'low':
      // A's optimistic value is no longer current, so the rollback is skipped.
      rejectA(new Error('rpc failed'))
      await aDone
      expect(tabStore.getAgentTab('a1')?.optionValues?.effort).toBe('low')
      dispose()
    })
  })

  it('a settled model change does not clobber a concurrent effort edit made while it was in flight', async () => {
    const tabStore = makeTabStore()
    tabStore.addTab({
      type: TabType.AGENT,
      id: 'a1',
      workerId: 'w1',
      optionValues: { model: 'opus[1m]', effort: 'high', permissionMode: 'default' },
      optionGroups: opusCatalog(),
    } as never)

    // Change A switches to the account-default sentinel; its RPC hangs. While it is in
    // flight the user changes effort (change B), which resolves immediately. When A finally
    // settles, its model reconcile must snap the model to the resolved concrete value WITHOUT
    // snapping effort back to the reply's (pre-B) 'high' over B's newer 'low' -- the per-axis
    // guard skips the effort snap because effort no longer holds the value A left it at.
    let resolveA: (v: { confirmedOptions: Record<string, string> }) => void = () => {}
    const aPending = new Promise<{ confirmedOptions: Record<string, string> }>((resolve) => {
      resolveA = resolve
    })
    updateAgentSettings
      .mockReturnValueOnce(aPending) // A: model change, hangs
      .mockResolvedValueOnce({ confirmedOptions: { effort: 'low' } }) // B: effort change

    await createRoot(async (dispose) => {
      const ops = useAgentOperations(stubProps(tabStore))
      // A: optimistic model='default' (expectedEffort captured as the pre-change 'high').
      const aDone = ops.handleAgentSettingChange('a1', { sets: { model: 'default' } })
      expect(tabStore.getAgentTab('a1')?.optionValues?.model).toBe('default')
      // B: optimistic effort='low', RPC resolves -> B is the newest effort selection.
      await ops.handleAgentSettingChange('a1', { sets: { effort: 'low' } })
      expect(tabStore.getAgentTab('a1')?.optionValues?.effort).toBe('low')
      // A settles: the relaunch resolved 'default' -> 'sonnet' and reset effort to 'high'.
      resolveA({ confirmedOptions: { model: 'sonnet', effort: 'high' } })
      await aDone
      const tab = tabStore.getAgentTab('a1')
      // Model snaps to the settled concrete model (A's own axis, still 'default' in the store).
      expect(tab?.optionValues?.model).toBe('sonnet')
      // Effort is NOT snapped back to A's reply 'high': B's newer 'low' wins.
      expect(tab?.optionValues?.effort).toBe('low')
      dispose()
    })
  })

  it('a reconcile fault after a successful model RPC does not revert the change or toast failure', async () => {
    const tabStore = makeTabStore()
    tabStore.addTab({
      type: TabType.AGENT,
      id: 'a1',
      workerId: 'w1',
      optionValues: { model: 'opus[1m]', effort: 'xhigh', permissionMode: 'default' },
      optionGroups: opusCatalog(),
    } as never)

    // The account-default switch settles to a concrete model, so the post-RPC reconcile
    // runs and writes the settled values.
    updateAgentSettings.mockResolvedValue({ confirmedOptions: { model: 'sonnet', effort: 'high' } })

    // Make ONLY the reconcile's store write fault: the first updateTab is the optimistic
    // write (pass through), the second is the reconcile snap (throw). This simulates a
    // success-path fault that must NOT be caught as an RPC failure.
    const original = tabStore.updateTab.bind(tabStore)
    vi.spyOn(tabStore, 'updateTab')
      .mockImplementationOnce((...a: Parameters<typeof original>) => original(...a))
      .mockImplementationOnce(() => { throw new Error('reconcile store fault') })

    const stop = vi.fn()
    const props = { ...stubProps(tabStore), settingsLoading: { start: () => {}, stop } } as Parameters<typeof useAgentOperations>[0]

    await createRoot(async (dispose) => {
      const ops = useAgentOperations(props)
      // The reconcile fault surfaces (a genuine programming error), but it happens AFTER
      // the success path -- not inside the RPC-failure rollback guard.
      const pending = ops.handleAgentSettingChange('a1', { sets: { model: 'default' } })
      await expect(pending).rejects.toThrow('reconcile store fault')
      dispose()
    })

    // The optimistic 'default' is preserved -- NOT rolled back to 'opus[1m]', which is what
    // would happen if the reconcile fault were mistaken for an RPC failure (the bug).
    expect(tabStore.getAgentTab('a1')?.optionValues?.model).toBe('default')
    // The spinner was cleared before the reconcile ran, so the agent isn't stranded pending. The
    // stop is scoped to this change's axes ([S6] per-axis suppression) so only `model` is released.
    expect(stop).toHaveBeenCalledWith('a1', ['model'])
    // No failure toast: the RPC succeeded.
    expect(showWarnToast).not.toHaveBeenCalled()
  })

  it('keeps a concrete (live-applied) model switch when the catalog has not caught up', async () => {
    // A Cursor-shaped catalog: a model group with concrete (bracketed) options and NO
    // effort axis. Cursor applies a model switch LIVE -- no relaunch -- so the settled
    // catalog may still hold the pre-switch model when this RPC resolves.
    const cursorModel = (currentValue: string) => create(AvailableOptionGroupSchema, {
      id: 'model',
      label: 'Model',
      order: 10,
      mutable: true,
      currentValue,
      options: [opt('auto', 'Auto'), opt('claude-fable-5[effort=high]', 'claude-fable-5'), opt('gpt-5.5[reasoning=medium]', 'gpt-5.5')],
    })
    const tabStore = makeTabStore()
    tabStore.addTab({
      type: TabType.AGENT,
      id: 'a1',
      workerId: 'w1',
      optionValues: { model: 'auto', permissionMode: 'agent' },
      optionGroups: [cursorModel('auto'), permissionGroup],
    } as never)

    // Cursor applies the switch LIVE and the reply confirms the same concrete model.
    // The reconcile only snaps the account-default sentinel, so a concrete switch keeps
    // its optimistic value untouched regardless of what the reply echoes.
    updateAgentSettings.mockResolvedValue({ confirmedOptions: { model: 'claude-fable-5[effort=high]', permissionMode: 'agent' } })

    await runChange(tabStore, { groupKey: 'model', value: 'claude-fable-5[effort=high]' })

    // The optimistic concrete model is preserved (the reconcile must not snap a
    // concrete model -- only the account-default sentinel).
    expect(tabStore.getAgentTab('a1')?.optionValues?.model).toBe('claude-fable-5[effort=high]')
  })

  it('applies a multi-axis change optimistically and sends both axes in ONE RPC', async () => {
    const tabStore = makeTabStore()
    tabStore.addTab({
      type: TabType.AGENT,
      id: 'a1',
      workerId: 'w1',
      optionValues: { model: 'opus[1m]', effort: 'high', permissionMode: 'default' },
      optionGroups: opusCatalog(),
    } as never)

    updateAgentSettings.mockResolvedValue({ confirmedOptions: {} })

    await createRoot(async (dispose) => {
      const ops = useAgentOperations(stubProps(tabStore))
      // An action button (e.g. Codex "Bypass permissions") carries several axes.
      await ops.handleAgentSettingChange('a1', { sets: { network_access: 'enabled', permissionMode: 'never' } })
      dispose()
    })

    // Both axes are written optimistically in one update.
    const tab = tabStore.getAgentTab('a1')
    expect(tab?.optionValues?.network_access).toBe('enabled')
    expect(tab?.optionValues?.permissionMode).toBe('never')
    // ONE RPC carrying BOTH axes -- not one RPC per axis -- so the worker applies them
    // atomically and can't leave the agent half-bypassed.
    expect(updateAgentSettings).toHaveBeenCalledTimes(1)
    expect(updateAgentSettings).toHaveBeenCalledWith('w1', {
      agentId: 'a1',
      settings: { options: { network_access: 'enabled', permissionMode: 'never' } },
    })
  })

  it('rolls back every axis of a failed multi-axis change (restoring priors, deleting keys that had none)', async () => {
    const tabStore = makeTabStore()
    tabStore.addTab({
      type: TabType.AGENT,
      id: 'a1',
      workerId: 'w1',
      // permissionMode has a prior optimistic value; network_access has none.
      optionValues: { model: 'opus[1m]', permissionMode: 'default' },
      optionGroups: opusCatalog(),
    } as never)

    updateAgentSettings.mockRejectedValue(new Error('rpc failed'))

    await createRoot(async (dispose) => {
      const ops = useAgentOperations(stubProps(tabStore))
      await ops.handleAgentSettingChange('a1', { sets: { network_access: 'enabled', permissionMode: 'never' } })
      dispose()
    })

    const tab = tabStore.getAgentTab('a1')
    // permissionMode had a prior 'default' -> restored exactly.
    expect(tab?.optionValues?.permissionMode).toBe('default')
    // network_access had no prior -> the key is DELETED (not blanked to ''), so the group
    // falls back to the catalog's confirmed currentValue rather than a spurious empty override.
    expect(tab?.optionValues && 'network_access' in tab.optionValues).toBe(false)
    // One failure toast for the whole change, naming both axes.
    expect(showWarnToast).toHaveBeenCalledTimes(1)
  })

  it('rolls back only the still-current axes of a failed multi-axis change (per-axis guard)', async () => {
    const tabStore = makeTabStore()
    tabStore.addTab({
      type: TabType.AGENT,
      id: 'a1',
      workerId: 'w1',
      optionValues: { model: 'opus[1m]', permissionMode: 'default' },
      optionGroups: opusCatalog(),
    } as never)

    // Change A (two axes) hangs; change B (only permissionMode) resolves while A is in flight.
    let rejectA: (e: Error) => void = () => {}
    const aPending = new Promise((_resolve, reject) => {
      rejectA = reject
    })
    updateAgentSettings
      .mockReturnValueOnce(aPending)
      .mockResolvedValueOnce({ confirmedOptions: {} })

    await createRoot(async (dispose) => {
      const ops = useAgentOperations(stubProps(tabStore))
      // A: network_access='enabled' + permissionMode='never', RPC hangs (not awaited).
      const aDone = ops.handleAgentSettingChange('a1', { sets: { network_access: 'enabled', permissionMode: 'never' } })
      expect(tabStore.getAgentTab('a1')?.optionValues?.network_access).toBe('enabled')
      expect(tabStore.getAgentTab('a1')?.optionValues?.permissionMode).toBe('never')
      // B: overwrites ONLY permissionMode and resolves -> B is the newest selection for that axis.
      await ops.handleAgentSettingChange('a1', { sets: { permissionMode: 'plan' } })
      expect(tabStore.getAgentTab('a1')?.optionValues?.permissionMode).toBe('plan')
      // A now fails. Its rollback must SKIP permissionMode (B's 'plan' is newer, no longer A's
      // 'never') yet still roll back network_access (still A's 'enabled', no prior -> deleted).
      rejectA(new Error('rpc failed'))
      await aDone
      const tab = tabStore.getAgentTab('a1')
      expect(tab?.optionValues?.permissionMode).toBe('plan') // B preserved, not clobbered
      expect(tab?.optionValues && 'network_access' in tab.optionValues).toBe(false) // A rolled back
      dispose()
    })
  })

  it('treats an empty sets change as a no-op (no RPC, no tab write)', async () => {
    const tabStore = makeTabStore()
    tabStore.addTab({
      type: TabType.AGENT,
      id: 'a1',
      workerId: 'w1',
      optionValues: { model: 'opus[1m]' },
      optionGroups: opusCatalog(),
    } as never)

    const updateSpy = vi.spyOn(tabStore, 'updateTab')

    await createRoot(async (dispose) => {
      const ops = useAgentOperations(stubProps(tabStore))
      await ops.handleAgentSettingChange('a1', { sets: {} })
      dispose()
    })

    // The early return fires before any optimistic write or RPC.
    expect(updateAgentSettings).not.toHaveBeenCalled()
    expect(updateSpy).not.toHaveBeenCalled()
  })

  it('refuses a change for an agent that has reported no option catalog yet', async () => {
    const tabStore = makeTabStore()
    // An agent mid-startup whose catalog has not arrived: optionGroups is empty. A
    // programmatic caller (the control-request "& Bypass Permissions" button, gated on a
    // static provider constant rather than the live catalog) can still dispatch here.
    tabStore.addTab({
      type: TabType.AGENT,
      id: 'a1',
      workerId: 'w1',
      optionValues: { model: 'opus[1m]' },
      optionGroups: [],
    } as never)

    const updateSpy = vi.spyOn(tabStore, 'updateTab')

    await createRoot(async (dispose) => {
      const ops = useAgentOperations(stubProps(tabStore))
      await ops.handleAgentSettingChange('a1', { sets: { permissionMode: 'plan' } })
      dispose()
    })

    // No optimistic write nothing can reconcile, and no RPC against an axis the empty
    // catalog can't back -- the change is refused outright.
    expect(updateAgentSettings).not.toHaveBeenCalled()
    expect(updateSpy).not.toHaveBeenCalled()
    expect(tabStore.getAgentTab('a1')?.optionValues).toEqual({ model: 'opus[1m]' })
  })
})
