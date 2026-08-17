import type { Component } from 'solid-js'
import { createSignal, For, Show } from 'solid-js'
import { Tooltip } from '~/components/common/Tooltip'
import { createKeyedSeq } from '~/lib/keyedSeq'
import { sanitizeName } from '~/lib/validate'
import { errorText } from '~/styles/shared.css'
import * as styles from './StringListControl.css'

/**
 * Backend cap on stored entries (usersettings.MaxFonts).
 *
 * The hub is the authority and refuses a longer list with its own message.
 * This copy exists so the refusal arrives before the write, and it must
 * never be the stricter of the two.
 */
export const MAX_STRING_LIST_ITEMS = 32

export interface StringListControlProps {
  value: string[]
  /** Label for the add affordance, e.g. "Add font". */
  addLabel: string
  ariaLabel: string
  /**
   * Write the whole list.
   *
   * The control AWAITS the returned promise to hold its busy state and its
   * pending list; it does not read the result. A refused write settles like
   * an accepted one, and the list then falls back to `value`.
   */
  onChange: (value: string[]) => void | Promise<unknown>
}

/**
 * An ordered list of names the user can reorder (drag, or Alt with the arrow
 * keys), rename inline (double-click, or Enter), add, and remove — the
 * font-stack editor, generalized from the old FontSettings component.
 * Validation applies the same `sanitizeName` rule the account RPC enforces,
 * so a name the hub would refuse never leaves the control. The hub REFUSES a
 * name that is not already sanitized rather than stripping it, so the two
 * rules must stay identical — a name this control accepts must be one the
 * hub stores unchanged. The hub's copy is `backend/util/validate/name.go`.
 */
export const StringListControl: Component<StringListControlProps> = (props) => {
  const [newName, setNewName] = createSignal('')
  const [message, setMessage] = createSignal<string | null>(null)
  // A write is in flight. The add affordance is disabled for its duration,
  // so a second entry cannot be queued against a list the hub has not
  // accepted yet.
  const [busy, setBusy] = createSignal(false)

  // Inline edit state
  const [editingIndex, setEditingIndex] = createSignal<number | null>(null)
  const [editingValue, setEditingValue] = createSignal('')
  const [editingError, setEditingError] = createSignal<string | null>(null)

  // Drag state
  const [dragIndex, setDragIndex] = createSignal<number | null>(null)

  /**
   * The list this control last sent, while its write is still in flight.
   *
   * `props.value` catches up only when the binding applies the write. The
   * account binding applies it locally and synchronously; a hub-scope
   * binding applies nothing until the RPC returns — so two edits inside
   * that window would both compute from the pre-edit list, and the second
   * would discard the first. Rendering from this same helper keeps what the
   * user SEES and what the next edit computes from as one list, which is
   * also what makes an index taken from the rendered rows meaningful.
   */
  const [pending, setPending] = createSignal<string[] | null>(null)
  const currentValue = (): string[] => pending() ?? props.value

  /**
   * The newest write, so a slower older one cannot end the busy state.
   *
   * Unkeyed: one control edits one list, so every write here supersedes the
   * one before it.
   */
  const writeSeq = createKeyedSeq()

  const write = (next: string[]) => {
    const seq = writeSeq.next()
    const newest = () => writeSeq.isNewest(undefined, seq)
    setPending(next)
    const result = props.onChange(next)
    if (!(result instanceof Promise)) {
      if (newest())
        setPending(null)
      return
    }
    setBusy(true)
    void result.finally(() => {
      // Only the NEWEST write ends the busy state and drops the pending
      // list. An older one settling later would otherwise re-enable the add
      // control while a newer write is still running, and would publish the
      // list from before that newer edit.
      if (!newest())
        return
      setBusy(false)
      setPending(null)
    })
  }

  const add = () => {
    const { value: sanitized, error } = sanitizeName(newName())
    if (error) {
      setMessage(error)
      return
    }
    if (currentValue().length >= MAX_STRING_LIST_ITEMS) {
      setMessage(`Too many entries (max ${MAX_STRING_LIST_ITEMS})`)
      return
    }
    write([...currentValue(), sanitized])
    setNewName('')
    setMessage(null)
  }

  const remove = (index: number) => {
    write(currentValue().filter((_, i) => i !== index))
  }

  /**
   * Move the entry at `from` to `to`, or do nothing when either index sits
   * outside the list. The drop handler and the Alt+Arrow handler share it,
   * so a mouse reorder and a keyboard reorder produce the same list.
   */
  const moveItem = (from: number, to: number) => {
    const items = [...currentValue()]
    if (from === to || from < 0 || to < 0 || from >= items.length || to >= items.length)
      return
    const [moved] = items.splice(from, 1)
    items.splice(to, 0, moved)
    write(items)
  }

  const handleDragOver = (e: DragEvent) => {
    e.preventDefault()
  }

  const handleDrop = (targetIndex: number) => {
    const srcIndex = dragIndex()
    setDragIndex(null)
    if (srcIndex === null)
      return
    moveItem(srcIndex, targetIndex)
  }

  const startEdit = (index: number, current: string) => {
    setEditingIndex(index)
    setEditingValue(current)
    setEditingError(null)
  }

  /**
   * Commit the inline rename of `index`, unless that edit already ended.
   *
   * The guard is `editingIndex`, never a "was cancelled" flag. Escape ends
   * the edit by clearing `editingIndex`, which unmounts the input — and
   * removing a focused element dispatches NO blur event, so a flag that
   * `onBlur` was supposed to consume stays set and swallows the NEXT
   * rename's Enter instead. Reading the state that the edit itself owns
   * makes that whole class impossible: a commit for an index that is no
   * longer being edited is a no-op, whatever ended it.
   */
  const commitEdit = (index: number) => {
    if (editingIndex() !== index)
      return
    const { value, error } = sanitizeName(editingValue())
    if (error) {
      setEditingError(error)
      return
    }
    const items = currentValue()
    if (value !== items[index]) {
      const next = [...items]
      next[index] = value
      write(next)
    }
    setEditingIndex(null)
    setEditingError(null)
  }

  const cancelEdit = () => {
    setEditingIndex(null)
    setEditingError(null)
  }

  /**
   * Rename and reorder from the keyboard.
   *
   * Reordering was bound to `draggable` alone, so the priority order of a
   * font stack — the whole point of the list — needed a mouse. Enter and
   * Space open the same inline editor a double-click opens; Alt with an
   * arrow key moves the entry, which is the shape a reorderable listbox
   * uses and which leaves the plain arrow keys to the browser.
   */
  const handleItemKeyDown = (
    e: KeyboardEvent & { currentTarget: HTMLElement },
    index: number,
    item: string,
  ) => {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      startEdit(index, item)
      return
    }
    if (!e.altKey || (e.key !== 'ArrowUp' && e.key !== 'ArrowDown'))
      return
    e.preventDefault()
    // Solid applies the list change after this handler returns, and the row
    // moves in the DOM with it. A reorder that drops focus cannot be
    // repeated, which is the whole point of the keyboard path, so restore
    // focus once the move is applied.
    const el = e.currentTarget
    moveItem(index, e.key === 'ArrowUp' ? index - 1 : index + 1)
    queueMicrotask(() => el.focus())
  }

  return (
    <div class="vstack gap-2">
      <Show
        when={currentValue().length > 0}
        fallback={<div class={styles.listEmpty}>None configured</div>}
      >
        <div class={styles.list}>
          <For each={currentValue()}>
            {(item, i) => (
              <div
                class={styles.listItem}
                draggable={editingIndex() !== i()}
                onDragStart={() => setDragIndex(i())}
                onDragOver={handleDragOver}
                onDrop={() => handleDrop(i())}
                onDragEnd={() => setDragIndex(null)}
              >
                <span class={styles.dragHandle} aria-hidden="true">&#x283F;</span>
                <Show
                  when={editingIndex() === i()}
                  fallback={(
                    <span
                      class={styles.itemName}
                      role="button"
                      tabIndex={0}
                      aria-label={`Rename ${item}`}
                      aria-keyshortcuts="Alt+ArrowUp Alt+ArrowDown"
                      onDblClick={() => startEdit(i(), item)}
                      onKeyDown={e => handleItemKeyDown(e, i(), item)}
                    >
                      {item}
                    </span>
                  )}
                >
                  <div class={styles.editWrapper}>
                    <input
                      class={styles.editInput}
                      type="text"
                      aria-label={`Rename ${props.ariaLabel}`}
                      value={editingValue()}
                      onInput={(e) => {
                        setEditingValue(e.currentTarget.value)
                        setEditingError(null)
                      }}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter') {
                          commitEdit(i())
                        }
                        else if (e.key === 'Escape') {
                          cancelEdit()
                        }
                      }}
                      onBlur={() => commitEdit(i())}
                      ref={(el) => {
                        requestAnimationFrame(() => {
                          el.focus()
                          el.select()
                        })
                      }}
                    />
                    <Show when={editingError()}>
                      <span class={errorText}>{editingError()}</span>
                    </Show>
                  </div>
                </Show>
                <Tooltip text="Remove" ariaLabel>
                  <button
                    class={styles.removeButton}
                    onClick={() => remove(i())}
                  >
                    &#xd7;
                  </button>
                </Tooltip>
              </div>
            )}
          </For>
        </div>
      </Show>
      <div class={styles.addRow}>
        <input
          type="text"
          aria-label={props.addLabel}
          placeholder="Name"
          value={newName()}
          disabled={busy()}
          onInput={e => setNewName(e.currentTarget.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              add()
            }
          }}
        />
        <Tooltip text={props.addLabel} ariaLabel>
          <button
            type="button"
            class="small outline"
            onClick={() => add()}
            disabled={busy() || !newName().trim()}
          >
            +
          </button>
        </Tooltip>
      </div>
      <Show when={message()}>
        <div class={errorText}>{message()}</div>
      </Show>
    </div>
  )
}
