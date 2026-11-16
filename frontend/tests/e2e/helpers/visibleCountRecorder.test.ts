import type { Page } from '@playwright/test'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { installVisibleCountRecorder, maxVisibleCountDuring } from './visibleCountRecorder'

const SCOPE = { container: '.list', item: '.leaf' }

/** Read the record that the recorder keeps under `key`. */
function recordedMax(key: string): number {
  return (Reflect.get(window, key) as { max: number }).max
}

/** Wait for the MutationObserver callbacks of the changes so far. */
async function observed(): Promise<void> {
  await Promise.resolve()
}

/** Add an element that matches `className`, visible or not, to `parent`. */
function add(parent: Element, className: string, visible = true): HTMLElement {
  const element = document.createElement('div')
  element.className = className
  element.dataset.visible = String(visible)
  parent.append(element)
  return element
}

describe('installVisibleCountRecorder', () => {
  beforeEach(() => {
    // The test DOM computes no style and has no `checkVisibility`, so an element states its own visibility.
    Object.defineProperty(Element.prototype, 'checkVisibility', {
      configurable: true,
      value(this: Element) {
        return (this as HTMLElement).dataset.visible === 'true'
      },
    })
  })

  afterEach(() => {
    Reflect.deleteProperty(Element.prototype, 'checkVisibility')
    document.body.replaceChildren()
  })

  it('keeps the largest visible count, so a state that the page showed and replaced still counts', async () => {
    installVisibleCountRecorder(SCOPE, '__visible_largest')
    const list = add(document.body, 'list')
    add(list, 'leaf')
    const stale = add(list, 'leaf')
    await observed()
    stale.remove()
    await observed()
    expect(document.querySelectorAll('.leaf')).toHaveLength(1)
    expect(recordedMax('__visible_largest')).toBe(2)
  })

  it('counts no item that a reader cannot see, and no item of a hidden container', async () => {
    installVisibleCountRecorder(SCOPE, '__visible_hidden')
    const shown = add(document.body, 'list')
    add(shown, 'leaf', false)
    add(shown, 'leaf')
    const covered = add(document.body, 'list', false)
    add(covered, 'leaf')
    add(covered, 'leaf')
    await observed()
    expect(recordedMax('__visible_hidden')).toBe(1)
  })

  it('counts each container on its own, so two shown copies of one list count once', async () => {
    installVisibleCountRecorder(SCOPE, '__visible_copies')
    add(add(document.body, 'list'), 'leaf')
    add(add(document.body, 'list'), 'leaf')
    await observed()
    expect(recordedMax('__visible_copies')).toBe(1)
  })

  it('counts an item that becomes visible through an attribute change', async () => {
    installVisibleCountRecorder(SCOPE, '__visible_attribute')
    const list = add(document.body, 'list')
    add(list, 'leaf')
    const late = add(list, 'leaf', false)
    await observed()
    expect(recordedMax('__visible_attribute')).toBe(1)
    late.dataset.visible = 'true'
    await observed()
    expect(recordedMax('__visible_attribute')).toBe(2)
  })

  it('counts the items that exist when it starts', () => {
    add(add(document.body, 'list'), 'leaf')
    installVisibleCountRecorder(SCOPE, '__visible_start')
    expect(recordedMax('__visible_start')).toBe(1)
  })

  it('runs from its own source text, as the init script runs it', async () => {
    // eslint-disable-next-line no-new-func -- the page runs exactly this text, so the test runs it the same way
    new Function(`(${installVisibleCountRecorder.toString()})(${JSON.stringify(SCOPE)}, "__visible_source")`)()
    add(add(document.body, 'list'), 'leaf')
    await observed()
    expect(recordedMax('__visible_source')).toBe(1)
  })
})

describe('maxVisibleCountDuring', () => {
  /**
   * A page that records the order of the init script calls, the navigation, and the read. With `addFailure`, the page
   * refuses the init script.
   */
  function fakePage(options: { record?: unknown, navigate?: () => Promise<void>, addFailure?: Error } = {}) {
    const events: string[] = []
    const page = {
      addInitScript: async (script: { content: string }) => {
        events.push('addInitScript')
        expect(script.content).toContain(JSON.stringify(SCOPE))
        if (options.addFailure !== undefined)
          throw options.addFailure
        return {
          dispose: async () => {
            events.push('dispose')
          },
        }
      },
      evaluate: async (read: (name: string) => number, name: string) => {
        events.push('read')
        Object.defineProperty(window, name, {
          value: 'record' in options
            ? options.record
            : { max: 2 },
          configurable: true,
        })
        return read(name)
      },
    } as unknown as Page
    const navigate = async () => {
      events.push('navigate')
      await options.navigate?.()
    }
    return { page, events, navigate }
  }

  it('adds the recorder, navigates, reads the count, and removes the recorder', async () => {
    const { page, events, navigate } = fakePage()
    expect(await maxVisibleCountDuring(page, SCOPE, navigate)).toBe(2)
    expect(events).toEqual([
      'addInitScript',
      'navigate',
      'read',
      'dispose',
    ])
  })

  it('removes the recorder when the navigation fails', async () => {
    const failure = new Error('the reload failed')
    const { page, events, navigate } = fakePage({
      navigate: async () => {
        throw failure
      },
    })
    await expect(maxVisibleCountDuring(page, SCOPE, navigate)).rejects.toBe(failure)
    expect(events).toEqual([
      'addInitScript',
      'navigate',
      'dispose',
    ])
  })

  it('does not navigate when the page refuses the recorder', async () => {
    const failure = new Error('the page is closed')
    const { page, events, navigate } = fakePage({ addFailure: failure })
    await expect(maxVisibleCountDuring(page, SCOPE, navigate)).rejects.toBe(failure)
    expect(events).toEqual(['addInitScript'])
  })

  it.each([undefined, null, {}])('fails when the page holds no count (%j), as when the navigation started no new document', async (record) => {
    const { page, events, navigate } = fakePage({ record })
    await expect(maxVisibleCountDuring(page, SCOPE, navigate)).rejects.toThrow('did not start a new document')
    expect(events).toEqual([
      'addInitScript',
      'navigate',
      'read',
      'dispose',
    ])
  })

  it.each([
    { container: '', item: '.leaf' },
    { container: '.list', item: ' ' },
  ])('refuses the scope %j before it adds the recorder', async (scope) => {
    const { page, events, navigate } = fakePage()
    await expect(maxVisibleCountDuring(page, scope, navigate)).rejects.toThrow('needs a container selector and an item selector')
    expect(events).toEqual([])
  })
})
