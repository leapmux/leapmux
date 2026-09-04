import type { CustomEditorComponent } from '../types'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import X from 'lucide-solid/icons/x'
import { createEffect, For, Show, untrack } from 'solid-js'
import { createStore } from 'solid-js/store'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { Alert } from '~/components/common/Alert'
import { DropdownMenu, DropdownMenuCheckableItem } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { StatusLine } from '~/components/common/StatusLine'
import { MAX_TRUSTED_PROXY_SELECTORS, TRUSTED_PROXY_PROVIDERS } from '~/generated/contracts/trusted-proxies'
import { createAccountAction } from '../account/createAccountAction'
import * as styles from './TrustedProxiesControl.css'

interface SelectorRow {
  id: number
  value: string
}

let nextSelectorRowID = 1

const selectorExamples = [
  '192.0.2.10',
  '2001:db8::10',
  '192.168.0.1-100',
  '192.168.0.1-192.168.0.100',
  '2001:db8::1-2001:db8::ffff',
  '192.168.0.0/24',
  '2001:db8::/64',
  'cloudflare',
  'cloudfront',
] as const

function row(value: string): SelectorRow {
  return { id: nextSelectorRowID++, value }
}

/** Edits the trusted reverse-proxy selector list as one staged value. */
export const TrustedProxiesControl: CustomEditorComponent = (props) => {
  const [state, setState] = createStore<{ rows: SelectorRow[] }>({ rows: [] })
  const [dirty, setDirty] = createStore({ value: false })
  const action = createAccountAction()

  const storedSelectors = (): string[] => {
    const value = props.binding.value()
    return Array.isArray(value) ? value.filter(item => typeof item === 'string') : []
  }

  createEffect(() => {
    const stored = storedSelectors()
    if (!untrack(() => dirty.value))
      setState('rows', stored.map(row))
  })

  const configuredProvider = (token: string) =>
    state.rows.some(item => item.value.trim().toLowerCase() === token)

  const canAdd = () => state.rows.length < MAX_TRUSTED_PROXY_SELECTORS

  const add = (value: string) => {
    if (!canAdd())
      return
    setDirty('value', true)
    setState('rows', state.rows.length, row(value))
  }

  const update = (id: number, value: string) => {
    setDirty('value', true)
    setState('rows', item => item.id === id, 'value', value)
  }

  const remove = (id: number) => {
    setDirty('value', true)
    setState('rows', items => items.filter(item => item.id !== id))
  }

  const cancel = () => {
    setDirty('value', false)
    setState('rows', storedSelectors().map(row))
    action.clear()
  }

  const apply = async () => {
    if (!dirty.value || action.running())
      return
    await action.run({
      fallback: 'Failed to update trusted reverse proxies',
      work: async () => {
        await props.binding.set(state.rows.map(item => item.value))
        setState('rows', storedSelectors().map(row))
        setDirty('value', false)
        return 'Trusted reverse proxies updated.'
      },
    })
  }

  return (
    <div class="vstack gap-4">
      <Alert variant="warning" label="Provider range warning">
        <ul class={styles.warningList}>
          <li>These ranges identify shared provider infrastructure, not your account.</li>
          <li>Restrict the origin to your distribution or use authenticated origin requests.</li>
          <li>Each proxy must remove client-supplied forwarding headers or append verified values.</li>
        </ul>
      </Alert>

      <Show
        when={state.rows.length > 0}
        fallback={<div class={styles.note}>No proxy is trusted. Forwarding headers are ignored.</div>}
      >
        <div class="vstack gap-2">
          <For each={state.rows}>
            {item => (
              <div class={styles.selectorRow} data-testid="trusted-proxy-row">
                <input
                  type="text"
                  class={styles.selectorInput}
                  value={item.value}
                  aria-label="Trusted proxy selector"
                  placeholder="192.168.0.0/24"
                  spellcheck={false}
                  onInput={event => update(item.id, event.currentTarget.value)}
                />
                <button
                  type="button"
                  class={`outline ${styles.removeButton}`}
                  data-variant="danger"
                  aria-label={`Remove ${item.value || 'empty selector'}`}
                  onClick={() => remove(item.id)}
                >
                  <Icon icon={X} size="xs" aria-hidden="true" />
                </button>
              </div>
            )}
          </For>
        </div>
      </Show>

      <div class={styles.examples}>
        <span>Examples:</span>
        <ul class={styles.exampleList}>
          <For each={selectorExamples}>
            {example => <li><code>{example}</code></li>}
          </For>
        </ul>
      </div>

      <div>
        <DropdownMenu
          aria-label="Add trusted proxy"
          trigger={triggerProps => (
            <button type="button" class="outline small" disabled={!canAdd()} {...triggerProps}>
              Add
              {' '}
              <Icon icon={ChevronDown} size="xs" aria-hidden="true" />
            </button>
          )}
        >
          <DropdownMenuCheckableItem
            kind="checkbox"
            label="IP address or range"
            checked={false}
            disabled={!canAdd()}
            onSelect={() => add('')}
          />
          <For each={TRUSTED_PROXY_PROVIDERS}>
            {provider => (
              <DropdownMenuCheckableItem
                kind="checkbox"
                label={provider.label}
                detail={() => provider.help}
                checked={configuredProvider(provider.token)}
                disabled={!canAdd() || configuredProvider(provider.token)}
                onSelect={() => add(provider.token)}
              />
            )}
          </For>
        </DropdownMenu>
        <Show when={!canAdd()}>
          <span class={styles.note}>{` At most ${MAX_TRUSTED_PROXY_SELECTORS} selectors.`}</span>
        </Show>
      </div>

      <StatusLine message={action.message()} />
      <div class={actionsFooter}>
        <button type="button" class="outline" disabled={action.running()} onClick={cancel}>Cancel</button>
        <button type="button" disabled={!dirty.value || action.running()} onClick={() => void apply()}>
          {action.running() ? 'Applying...' : 'Apply'}
        </button>
      </div>
    </div>
  )
}
