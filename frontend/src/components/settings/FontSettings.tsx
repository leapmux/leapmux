import type { Component } from 'solid-js'
import { createSignal, For, onMount, Show } from 'solid-js'
import { usePreferences } from '~/context/PreferencesContext'
import { sanitizeName } from '~/lib/validate'
import * as styles from './PreferencesPage.css'

export const FontSettings: Component = () => {
  const prefs = usePreferences()

  // Font editing (account-level)
  const [uiFonts, setUiFonts] = createSignal<string[]>([])
  const [monoFonts, setMonoFonts] = createSignal<string[]>([])
  const [fontSaving, setFontSaving] = createSignal(false)
  const [fontMessage, setFontMessage] = createSignal<{ type: 'success' | 'error', text: string } | null>(null)
  const [newUiFont, setNewUiFont] = createSignal('')
  const [newMonoFont, setNewMonoFont] = createSignal('')

  // Inline edit state
  const [editingKey, setEditingKey] = createSignal<string | null>(null)
  const [editingValue, setEditingValue] = createSignal('')
  const [editingError, setEditingError] = createSignal<string | null>(null)
  let editCancelled = false

  // Drag state
  const [dragIndex, setDragIndex] = createSignal<number | null>(null)
  const [dragList, setDragList] = createSignal<'ui' | 'mono' | null>(null)

  onMount(() => {
    setUiFonts([...prefs.uiFonts()])
    setMonoFonts([...prefs.monoFonts()])
  })

  const saveAccountFonts = async (uiEnabled: boolean, monoEnabled: boolean, ui: string[], mono: string[]) => {
    setFontSaving(true)
    setFontMessage(null)
    try {
      prefs.setAccountUiFontCustomEnabled(uiEnabled)
      prefs.setAccountMonoFontCustomEnabled(monoEnabled)
      prefs.setUiFonts(ui)
      prefs.setMonoFonts(mono)
      await prefs.saveAccountPreferences()
      setFontMessage({ type: 'success', text: 'Font preferences saved.' })
    }
    catch (e) {
      setFontMessage({ type: 'error', text: e instanceof Error ? e.message : 'Failed to save font preferences' })
    }
    finally {
      setFontSaving(false)
    }
  }

  const handleToggleUiFonts = (enabled: boolean) => {
    saveAccountFonts(enabled, prefs.accountMonoFontCustomEnabled(), uiFonts(), monoFonts())
  }

  const handleToggleMonoFonts = (enabled: boolean) => {
    saveAccountFonts(prefs.accountUiFontCustomEnabled(), enabled, uiFonts(), monoFonts())
  }

  const addFont = (list: 'ui' | 'mono') => {
    const name = list === 'ui' ? newUiFont() : newMonoFont()
    const { value: sanitized, error } = sanitizeName(name)
    if (error) {
      setFontMessage({ type: 'error', text: error })
      return
    }
    if (list === 'ui') {
      const updated = [...uiFonts(), sanitized]
      setUiFonts(updated)
      setNewUiFont('')
      saveAccountFonts(prefs.accountUiFontCustomEnabled(), prefs.accountMonoFontCustomEnabled(), updated, monoFonts())
    }
    else {
      const updated = [...monoFonts(), sanitized]
      setMonoFonts(updated)
      setNewMonoFont('')
      saveAccountFonts(prefs.accountUiFontCustomEnabled(), prefs.accountMonoFontCustomEnabled(), uiFonts(), updated)
    }
  }

  const removeFont = (list: 'ui' | 'mono', index: number) => {
    if (list === 'ui') {
      const updated = uiFonts().filter((_, i) => i !== index)
      setUiFonts(updated)
      saveAccountFonts(prefs.accountUiFontCustomEnabled(), prefs.accountMonoFontCustomEnabled(), updated, monoFonts())
    }
    else {
      const updated = monoFonts().filter((_, i) => i !== index)
      setMonoFonts(updated)
      saveAccountFonts(prefs.accountUiFontCustomEnabled(), prefs.accountMonoFontCustomEnabled(), uiFonts(), updated)
    }
  }

  const handleDragStart = (list: 'ui' | 'mono', index: number) => {
    setDragIndex(index)
    setDragList(list)
  }

  const handleDragOver = (e: DragEvent) => {
    e.preventDefault()
  }

  const handleDrop = (list: 'ui' | 'mono', targetIndex: number) => {
    const srcIndex = dragIndex()
    const srcList = dragList()
    if (srcIndex === null || srcList !== list)
      return
    if (srcIndex === targetIndex)
      return

    const fonts = list === 'ui' ? [...uiFonts()] : [...monoFonts()]
    const [moved] = fonts.splice(srcIndex, 1)
    fonts.splice(targetIndex, 0, moved)

    if (list === 'ui') {
      setUiFonts(fonts)
      saveAccountFonts(prefs.accountUiFontCustomEnabled(), prefs.accountMonoFontCustomEnabled(), fonts, monoFonts())
    }
    else {
      setMonoFonts(fonts)
      saveAccountFonts(prefs.accountUiFontCustomEnabled(), prefs.accountMonoFontCustomEnabled(), uiFonts(), fonts)
    }

    setDragIndex(null)
    setDragList(null)
  }

  const handleDragEnd = () => {
    setDragIndex(null)
    setDragList(null)
  }

  // --- Inline font editing ---
  const startFontEdit = (list: 'ui' | 'mono', index: number, currentName: string) => {
    setEditingKey(`${list}-${index}`)
    setEditingValue(currentName)
    setEditingError(null)
  }

  const commitFontEdit = (list: 'ui' | 'mono', index: number) => {
    if (editCancelled) {
      editCancelled = false
      return
    }
    const { value, error } = sanitizeName(editingValue())
    if (error) {
      setEditingError(error)
      return
    }
    const fonts = list === 'ui' ? [...uiFonts()] : [...monoFonts()]
    if (value !== fonts[index]) {
      fonts[index] = value
      if (list === 'ui') {
        setUiFonts(fonts)
        saveAccountFonts(prefs.accountUiFontCustomEnabled(), prefs.accountMonoFontCustomEnabled(), fonts, monoFonts())
      }
      else {
        setMonoFonts(fonts)
        saveAccountFonts(prefs.accountUiFontCustomEnabled(), prefs.accountMonoFontCustomEnabled(), uiFonts(), fonts)
      }
    }
    setEditingKey(null)
    setEditingError(null)
  }

  const cancelFontEdit = () => {
    editCancelled = true
    setEditingKey(null)
    setEditingError(null)
  }

  const renderFontList = (label: string, list: 'ui' | 'mono', fonts: () => string[]) => {
    const inputValue = () => list === 'ui' ? newUiFont() : newMonoFont()
    const setInputValue = (v: string) => list === 'ui' ? setNewUiFont(v) : setNewMonoFont(v)

    return (
      <div class="vstack gap-4">
        <span class={styles.fieldLabel}>{label}</span>
        <Show
          when={fonts().length > 0}
          fallback={<div class={styles.fontListEmpty}>No fonts configured</div>}
        >
          <div class={styles.fontList}>
            <For each={fonts()}>
              {(font, i) => (
                <div
                  class={styles.fontListItem}
                  draggable={editingKey() !== `${list}-${i()}`}
                  onDragStart={() => handleDragStart(list, i())}
                  onDragOver={handleDragOver}
                  onDrop={() => handleDrop(list, i())}
                  onDragEnd={handleDragEnd}
                >
                  <span class={styles.fontDragHandle}>&#x283F;</span>
                  <Show
                    when={editingKey() === `${list}-${i()}`}
                    fallback={(
                      <span
                        class={styles.fontName}
                        onDblClick={() => startFontEdit(list, i(), font)}
                      >
                        {font}
                      </span>
                    )}
                  >
                    <div class={styles.fontEditWrapper}>
                      <input
                        class={styles.fontEditInput}
                        type="text"
                        value={editingValue()}
                        onInput={(e) => {
                          const { value, error } = sanitizeName(e.currentTarget.value)
                          setEditingValue(value)
                          setEditingError(error)
                        }}
                        onKeyDown={(e) => {
                          if (e.key === 'Enter') {
                            commitFontEdit(list, i())
                          }
                          else if (e.key === 'Escape') {
                            cancelFontEdit()
                          }
                        }}
                        onBlur={() => commitFontEdit(list, i())}
                        ref={(el) => {
                          requestAnimationFrame(() => {
                            el.focus()
                            el.select()
                          })
                        }}
                      />
                      <Show when={editingError()}>
                        <span class={styles.fontEditError}>{editingError()}</span>
                      </Show>
                    </div>
                  </Show>
                  <button
                    class={styles.fontRemoveButton}
                    onClick={() => removeFont(list, i())}
                    title="Remove"
                  >
                    &#xd7;
                  </button>
                </div>
              )}
            </For>
          </div>
        </Show>
        <div class={styles.fontAddRow}>
          <input
            type="text"
            placeholder="Font name"
            value={inputValue()}
            onInput={e => setInputValue(sanitizeName(e.currentTarget.value).value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                addFont(list)
              }
            }}
            disabled={fontSaving()}
          />
          <button
            class="small outline"
            onClick={() => addFont(list)}
            disabled={fontSaving() || !inputValue().trim()}
            title={`Add ${label.toLowerCase()}`}
          >
            +
          </button>
        </div>
      </div>
    )
  }

  return (
    <div class={styles.section}>
      <h2>Fonts</h2>
      <div class="vstack gap-4">
        <label class={styles.toggleLabel}>
          Custom UI fonts
          <input type="checkbox" role="switch" checked={prefs.accountUiFontCustomEnabled()} onChange={e => handleToggleUiFonts(e.currentTarget.checked)} />
        </label>

        <label class={styles.toggleLabel}>
          Custom monospace fonts
          <input type="checkbox" role="switch" checked={prefs.accountMonoFontCustomEnabled()} onChange={e => handleToggleMonoFonts(e.currentTarget.checked)} />
        </label>
      </div>

      <Show when={prefs.accountUiFontCustomEnabled()}>
        {renderFontList('UI Fonts', 'ui', uiFonts)}
      </Show>
      <Show when={prefs.accountMonoFontCustomEnabled()}>
        {renderFontList('Monospace Fonts', 'mono', monoFonts)}
      </Show>

      <Show when={fontMessage()}>
        {msg => <div class={msg().type === 'success' ? styles.successText : styles.errorText}>{msg().text}</div>}
      </Show>
    </div>
  )
}
