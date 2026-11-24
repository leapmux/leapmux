import type { Component } from 'solid-js'
import type { Worker } from '~/generated/proto/leapmux/v1/worker_pb'
import type { GetWorkerSkillsResponse } from '~/generated/proto/leapmux/v1/worker_system_pb'
import { batch, createEffect, createMemo, createSignal, For, onCleanup, onMount, Show } from 'solid-js'
import { workerSystem } from '~/api/workerRpc'
import { formatErrorMessage } from '~/lib/errors'
import { listAllWorkers } from '~/lib/listAllWorkers'
import { workerInfoStore } from '~/stores/workerInfo.store'
import { errorText } from '~/styles/shared.css'
import * as styles from './AgentSkillsControl.css'

const SKILL_TARGETS = [
  { id: 'agents', label: '~/.agents/skills (universal: Codex, OpenCode, Gemini CLI, Antigravity, Cursor)' },
  { id: 'claude', label: '~/.claude/skills' },
  { id: 'codex', label: '~/.codex/skills' },
  { id: 'cursor', label: '~/.cursor/skills' },
  { id: 'gemini', label: '~/.gemini/skills' },
] as const

function stateLabel(state: string): string {
  switch (state) {
    case 'up_to_date':
      return 'Installed'
    case 'outdated':
      return 'Update available'
    case 'modified':
      return 'Modified locally'
    case 'unmanaged':
      return 'Unmanaged copy'
    default:
      return 'Not installed'
  }
}

/**
 * Per-Worker Agent Skills management panel in Preferences -> Chat & Composer.
 * Reads and updates each Worker's curated LeapMux SKILL.md installation and
 * startup auto-sync configuration over the end-to-end encrypted Worker channel.
 */
export const AgentSkillsControl: Component = () => {
  const [workers, setWorkers] = createSignal<Worker[]>([])
  const [selectedWorkerId, setSelectedWorkerId] = createSignal<string>('')
  const [skillsState, setSkillsState] = createSignal<GetWorkerSkillsResponse | null>(null)
  const [loading, setLoading] = createSignal(true)
  const [saving, setSaving] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)

  let disposed = false
  let loadSeq = 0

  onCleanup(() => {
    disposed = true
  })

  const onlineWorkers = createMemo(() => workers().filter(w => w.online))

  async function loadWorkers(): Promise<void> {
    setLoading(true)
    setError(null)
    try {
      const list = await listAllWorkers()
      if (disposed)
        return
      const online = list.filter(w => w.online)
      for (const w of online) {
        void workerInfoStore.fetchWorkerInfo(w.id)
      }
      batch(() => {
        setWorkers(list)
        if (!selectedWorkerId() && online.length > 0) {
          setSelectedWorkerId(online[0]!.id)
        }
        else if (online.length === 0) {
          setLoading(false)
        }
      })
    }
    catch (err) {
      if (disposed)
        return
      batch(() => {
        setError(formatErrorMessage(err, 'Could not load Workers.'))
        setLoading(false)
      })
    }
  }

  async function loadSkills(workerId: string): Promise<void> {
    const seq = ++loadSeq
    setLoading(true)
    setError(null)
    try {
      const resp = await workerSystem.getWorkerSkills(workerId, {})
      if (disposed || seq !== loadSeq)
        return
      batch(() => {
        setSkillsState(resp)
        setLoading(false)
      })
    }
    catch (err) {
      if (disposed || seq !== loadSeq)
        return
      batch(() => {
        setSkillsState(null)
        setError(formatErrorMessage(err, 'Could not read Agent Skills from the selected Worker.'))
        setLoading(false)
      })
    }
  }

  onMount(() => {
    void loadWorkers()
  })

  createEffect(() => {
    const wid = selectedWorkerId()
    if (!wid)
      return
    void loadSkills(wid)
  })

  async function applyUpdate(req: Parameters<typeof workerSystem.updateWorkerSkills>[1]): Promise<void> {
    const wid = selectedWorkerId()
    if (!wid || saving())
      return
    setSaving(true)
    setError(null)
    try {
      const resp = await workerSystem.updateWorkerSkills(wid, req)
      if (disposed)
        return
      if (resp.state) {
        setSkillsState(resp.state)
      }
    }
    catch (err) {
      if (disposed)
        return
      setError(formatErrorMessage(err, 'Could not update Agent Skills on this Worker.'))
    }
    finally {
      if (!disposed) {
        setSaving(false)
      }
    }
  }

  function toggleAutoSync(checked: boolean): void {
    void applyUpdate({ autoSync: checked })
  }

  function toggleSkill(name: string, enabled: boolean): void {
    const current = skillsState()
    if (!current)
      return
    const next = current.skills
      .filter((s) => {
        if (s.name === name)
          return enabled
        return s.enabled
      })
      .map(s => s.name)
    void applyUpdate({
      updateEnabledSkills: true,
      enabledSkills: next,
      syncNow: true,
    })
  }

  function toggleTarget(targetId: string, checked: boolean): void {
    const current = skillsState()
    if (!current)
      return
    const set = new Set(current.targets)
    if (checked) {
      set.add(targetId)
    }
    else if (set.size > 1) {
      set.delete(targetId)
    }
    else {
      // Keep at least one target directory selected.
      return
    }
    void applyUpdate({
      targets: Array.from(set),
      syncNow: true,
    })
  }

  return (
    <div class={styles.container} data-testid="agent-skills-control">
      <Show when={onlineWorkers().length > 1}>
        <div class={styles.toolbar}>
          <label class={styles.hint} for="agent-skills-worker">Worker</label>
          <select
            id="agent-skills-worker"
            class={styles.workerSelect}
            value={selectedWorkerId()}
            disabled={saving()}
            onChange={e => setSelectedWorkerId(e.currentTarget.value)}
            data-testid="agent-skills-worker-select"
          >
            <For each={onlineWorkers()}>
              {worker => (
                <option value={worker.id}>{workerInfoStore.getName(worker.id) || worker.id}</option>
              )}
            </For>
          </select>
        </div>
      </Show>

      <Show when={error()}>
        <div class={errorText} role="alert" data-testid="agent-skills-error">
          {error()}
        </div>
      </Show>

      <Show when={!loading() && onlineWorkers().length === 0 && !error()}>
        <div class={styles.empty} data-testid="agent-skills-no-worker">
          Connect a Worker to manage Agent Skills from Preferences, or run
          {' '}
          <code>leapmux skill install --all</code>
          {' '}
          locally.
        </div>
      </Show>

      <Show when={skillsState()}>
        {state => (
          <>
            <div class={styles.toggleRow}>
              <label for="agent-skills-autosync">
                Refresh enabled skills automatically on Worker startup
              </label>
              <input
                id="agent-skills-autosync"
                type="checkbox"
                checked={state().autoSync}
                disabled={saving()}
                onChange={e => toggleAutoSync(e.currentTarget.checked)}
                data-testid="agent-skills-autosync"
              />
            </div>

            <div class={styles.targetsRow} data-testid="agent-skills-targets">
              <span>Install targets:</span>
              <For each={SKILL_TARGETS}>
                {target => (
                  <label class={styles.targetCheck}>
                    <input
                      type="checkbox"
                      checked={state().targets.includes(target.id)}
                      disabled={saving() || (state().targets.length === 1 && state().targets.includes(target.id))}
                      onChange={e => toggleTarget(target.id, e.currentTarget.checked)}
                      data-testid={`agent-skills-target-${target.id}`}
                    />
                    <span>{target.label}</span>
                  </label>
                )}
              </For>
            </div>

            <div class={styles.skillList}>
              <For each={state().skills}>
                {skill => (
                  <div class={styles.skillRow} data-testid={`agent-skill-row-${skill.name}`}>
                    <div class={styles.skillBody}>
                      <div class={styles.skillHeader}>
                        <span class={styles.skillName}>{skill.name}</span>
                        <span class={styles.skillState}>{stateLabel(skill.state)}</span>
                      </div>
                      <span class={styles.skillDesc}>{skill.description}</span>
                    </div>
                    <input
                      type="checkbox"
                      aria-label={`Enable ${skill.name} skill`}
                      checked={skill.enabled}
                      disabled={saving()}
                      onChange={e => toggleSkill(skill.name, e.currentTarget.checked)}
                      data-testid={`agent-skill-toggle-${skill.name}`}
                    />
                  </div>
                )}
              </For>
            </div>

            <div class={styles.toolbar}>
              <span class={styles.hint}>
                CLI:
                {' '}
                <code>leapmux skill install --all</code>
                {' '}
                or
                {' '}
                <code>npx skills add leapmux/leapmux</code>
              </span>
              <button
                type="button"
                class="outline"
                disabled={saving() || !state().skills.some(s => s.enabled)}
                onClick={() => void applyUpdate({ syncNow: true })}
                data-testid="agent-skills-sync-now"
              >
                Sync now
              </button>
            </div>
          </>
        )}
      </Show>
    </div>
  )
}
