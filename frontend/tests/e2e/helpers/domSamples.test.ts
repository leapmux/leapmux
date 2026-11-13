import type { Page } from '@playwright/test'
import { afterEach, describe, expect, it } from 'vitest'
import { domSamplesDuring, finishDomSampler, installDomSampler } from './domSamples'

/** Wait for the MutationObserver callbacks of the changes so far. */
async function observed(): Promise<void> {
  await Promise.resolve()
}

/** The number of elements in the document body, as a sample reads it. */
function childCount(): number {
  return document.body.childElementCount
}

/**
 * A page whose `evaluate` runs in this test's document: a string as the source text that the page runs, and a
 * function with its argument.
 */
function fakePage(): Page {
  return {
    evaluate: async (script: string | ((arg: string) => unknown), arg?: string) => {
      if (typeof script === 'string') {
        // eslint-disable-next-line no-new-func -- the page runs exactly this text, so the test runs it the same way
        return new Function(script)()
      }
      return script(arg!)
    },
  } as unknown as Page
}

describe('installDomSampler', () => {
  afterEach(() => {
    document.body.replaceChildren()
  })

  it('samples once at the start, after each DOM change, and on each scroll of an element, until it stops', async () => {
    installDomSampler(childCount, '__samples_each')
    const scroller = document.createElement('div')
    document.body.append(scroller)
    await observed()
    scroller.dispatchEvent(new Event('scroll'))

    const samples = finishDomSampler('__samples_each')
    expect(samples).toEqual([0, 1, 1])
    document.body.append(document.createElement('div'))
    scroller.dispatchEvent(new Event('scroll'))
    await observed()
    // The sampler pushes into the same array that it returned, so a sampler
    // that kept running would add to it.
    expect(samples).toEqual([0, 1, 1])
    expect(Reflect.get(window, '__samples_each')).toBeUndefined()
  })

  // A state can show through an attribute or a text change alone, such as a
  // class that marks a loading state.
  it('samples after an attribute change and after a text change', async () => {
    const target = document.createElement('div')
    const text = document.createTextNode('before')
    target.append(text)
    document.body.append(target)
    installDomSampler(() => `${target.className}:${target.textContent}`, '__samples_kinds')

    target.className = 'loading'
    await observed()
    text.data = 'after'
    await observed()

    expect(finishDomSampler('__samples_kinds')).toEqual([':before', 'loading:before', 'loading:after'])
  })

  it('runs from its own source text, as the page runs it', async () => {
    // eslint-disable-next-line no-new-func -- the page runs exactly this text, so the test runs it the same way
    new Function(`(${installDomSampler.toString()})(${childCount.toString()}, "__samples_source")`)()
    document.body.append(document.createElement('div'))
    await observed()

    expect(finishDomSampler('__samples_source')).toEqual([0, 1])
  })
})

describe('finishDomSampler', () => {
  it('answers undefined for a key that holds no sampler', () => {
    expect(finishDomSampler('__samples_missing')).toBeUndefined()
  })

  it('answers undefined for a key that holds null, and leaves the key', () => {
    Object.defineProperty(window, '__samples_null', { value: null, configurable: true })
    try {
      expect(finishDomSampler('__samples_null')).toBeUndefined()
      expect(Reflect.has(window, '__samples_null')).toBe(true)
    }
    finally {
      Reflect.deleteProperty(window, '__samples_null')
    }
  })
})

/** The keys of the samplers that `window` holds now. The sampler's own property is not enumerable. */
function samplerKeys(): string[] {
  return Object.getOwnPropertyNames(window).filter(key => key.startsWith('__e2eDomSamples_'))
}

describe('domSamplesDuring', () => {
  afterEach(() => {
    document.body.replaceChildren()
  })

  it('returns the samples from before the action until it returns', async () => {
    const samples = await domSamplesDuring(fakePage(), () => document.body.childElementCount, async () => {
      document.body.append(document.createElement('div'))
      await observed()
      document.body.append(document.createElement('div'))
      await observed()
    })

    expect(samples).toEqual([0, 1, 2])
  })

  // Each call keeps its samples under a key of its own, so a sampler that starts
  // inside the action of another neither replaces nor stops the outer one.
  it('keeps the samples of a sampler that runs inside the action of another apart', async () => {
    let inner: number[] = []
    const outer = await domSamplesDuring(fakePage(), childCount, async () => {
      document.body.append(document.createElement('div'))
      await observed()
      inner = await domSamplesDuring(fakePage(), childCount, async () => {
        document.body.append(document.createElement('div'))
        await observed()
      })
      document.body.append(document.createElement('div'))
      await observed()
    })

    expect(inner).toEqual([1, 2])
    expect(outer).toEqual([0, 1, 2, 3])
    expect(samplerKeys()).toEqual([])
  })

  it('stops the sampler when the action fails, and reports the failure of the action', async () => {
    let keysDuringAction: string[] = []

    await expect(domSamplesDuring(fakePage(), () => 0, async () => {
      keysDuringAction = samplerKeys()
      throw new Error('the action failed')
    })).rejects.toThrow('the action failed')

    expect(keysDuringAction).toHaveLength(1)
    expect(samplerKeys()).toEqual([])
  })

  it('fails when the action replaced the document that the sampler watched', async () => {
    await expect(domSamplesDuring(fakePage(), () => 0, async () => {
      for (const key of samplerKeys())
        Reflect.deleteProperty(window, key)
    })).rejects.toThrow('The page holds no samples')
  })
})
