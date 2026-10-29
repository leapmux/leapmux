import { describe, expect, it, vi } from 'vitest'
import { deferred } from '~/test-support/async'
import { createClipboardPageFixture } from '~/test-support/clipboardPage'
import { captureClipboardText } from './clipboardCapture'

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  return {
    ...actual,
    expect: actual.expect.configure({ timeout: 200 }),
  }
})

describe('captureClipboardText', () => {
  it.each([
    { scenario: 'ordinary text', text: 'copied text' },
    { scenario: 'empty text', text: '' },
    {
      scenario: 'large Unicode text',
      text: '雪\n'.repeat(10000),
    },
  ])('observes the successful native write and restores its method for $scenario', async ({ text }) => {
    const f = createClipboardPageFixture()
    const originalExec = f.document.execCommand
    expect(await captureClipboardText(f.page, () => f.copy(text))).toBe(text)
    expect(f.text()).toBe(text)
    expect(f.nativeWrites).toEqual([text])
    expect(f.clipboard.writeText).toBe(f.originalWriteText)
    expect(f.document.execCommand).toBe(originalExec)
  })

  it('keeps its own successful copy when another page overwrites the shared clipboard', async () => {
    const f = createClipboardPageFixture()
    const copied = await captureClipboardText(f.page, async () => {
      await f.copy('this page')
      await f.overwriteFromAnotherPage('another page')
    })
    expect(copied).toBe('this page')
    expect(f.text()).toBe('another page')
    expect(f.nativeWrites).toEqual(['this page', 'another page'])
  })

  it('waits for the native write after the click handler returns', async () => {
    const f = createClipboardPageFixture()
    const release = deferred<void>()
    const clicked = deferred<void>()
    const nativeWrite = async (text: string) => {
      await release.promise
      await f.originalWriteText(text)
    }
    f.clipboard.writeText = nativeWrite
    let pendingWrite: Promise<void> | undefined
    const captured = captureClipboardText(f.page, async () => {
      pendingWrite = f.copy('delayed native write')
      clicked.resolve()
    })
    await clicked.promise
    expect(f.nativeWrites).toEqual([])
    release.resolve()
    await pendingWrite
    expect(await captured).toBe('delayed native write')
    expect(f.clipboard.writeText).toBe(nativeWrite)
  })

  it('records only the successful legacy fallback after the native API rejects', async () => {
    const f = createClipboardPageFixture()
    f.failWrites(new Error('Native API denied access.'))
    f.selectLegacyText('fallback text')
    const copied = await captureClipboardText(f.page, async () => {
      await expect(f.copy('fallback text')).rejects.toThrow('denied access')
      expect(f.document.execCommand('copy')).toBe(true)
    })
    expect(copied).toBe('fallback text')
    expect(f.nativeWrites).toEqual(['fallback text'])
  })

  it('propagates native write failure and restores observers', async () => {
    const f = createClipboardPageFixture()
    const failure = new Error('Native clipboard refused the write.')
    f.failWrites(failure)
    await expect(captureClipboardText(f.page, () => f.copy('not copied'))).rejects.toBe(failure)
    expect(f.nativeWrites).toEqual([])
    expect(f.clipboard.writeText).toBe(f.originalWriteText)
  })

  it('rejects an action that makes no successful copy even if old clipboard text exists', async () => {
    const f = createClipboardPageFixture()
    await f.overwriteFromAnotherPage('old value')
    await expect(captureClipboardText(f.page, async () => {})).rejects.toThrow(/toEqual/)
    expect(f.clipboard.writeText).toBe(f.originalWriteText)
  })

  it('observes a real legacy copy before the selection changes', async () => {
    const f = createClipboardPageFixture()
    f.navigator.clipboard = undefined
    f.selectLegacyText('before selection after', 7, 16)
    const originalExec = f.document.execCommand
    const copied = await captureClipboardText(f.page, async () => {
      expect(f.document.execCommand('copy')).toBe(true)
      f.selectLegacyText('restored selection')
      await f.overwriteFromAnotherPage('another page')
    })
    expect(copied).toBe('selection')
    expect(f.nativeWrites).toEqual(['selection', 'another page'])
    expect(f.document.execCommand).toBe(originalExec)
  })

  it('does not report a failed legacy copy as successful', async () => {
    const f = createClipboardPageFixture()
    f.navigator.clipboard = undefined
    f.allowLegacyCopy(false)
    f.selectLegacyText('not copied')
    await expect(captureClipboardText(f.page, async () => f.document.execCommand('copy'))).rejects.toThrow(/toEqual/)
    expect(f.nativeWrites).toEqual([])
  })

  it('refuses multiple successful writes instead of choosing one', async () => {
    const f = createClipboardPageFixture()
    await expect(captureClipboardText(f.page, async () => {
      await f.copy('first')
      await f.copy('second')
    })).rejects.toThrow('completed 2 writes')
    expect(f.clipboard.writeText).toBe(f.originalWriteText)
  })

  it('restores observers when the UI action throws', async () => {
    const f = createClipboardPageFixture()
    const failure = new Error('Copy button detached.')
    await expect(captureClipboardText(f.page, async () => {
      throw failure
    })).rejects.toBe(failure)
    expect(f.clipboard.writeText).toBe(f.originalWriteText)
  })

  it('rejects overlapping captures and preserves cleanup of the owner', async () => {
    const f = createClipboardPageFixture()
    await expect(captureClipboardText(f.page, () => captureClipboardText(f.page, () => f.copy('nested')))).rejects.toThrow('already active')
    expect(f.clipboard.writeText).toBe(f.originalWriteText)
  })

  it('restores a partially installed observer when another method cannot be observed', async () => {
    const f = createClipboardPageFixture()
    Object.defineProperty(f.document, 'execCommand', { configurable: false })
    await expect(captureClipboardText(f.page, () => f.copy('unreached'))).rejects.toThrow('Cannot redefine property: execCommand')
    expect(f.clipboard.writeText).toBe(f.originalWriteText)
    expect(f.nativeWrites).toEqual([])
  })

  it('retains the original action failure when the page closes', async () => {
    const f = createClipboardPageFixture()
    const failure = new Error('Page closed during the click.')
    await expect(captureClipboardText(f.page, async () => {
      f.close()
      throw failure
    })).rejects.toBe(failure)
  })
})
