import type { Locator, Page } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { test } from '../fixtures'
import { createTestDirectory } from './runDirectory'
import { expectDecodedImageInBubble, expectMcpToolImage, expectToolRowImage, expectToolRowWithoutImage, imageInBubble, imagesForToolCall, isNamedToolResultFinished, mcpResultImage, toolResultImageForName, writeToolImage } from './toolImages'
import { toolRows } from './ui'

type ToolLayout = 'mcp' | 'merged' | 'split'

async function setToolImageContent(page: Page, html: string): Promise<void> {
  await page.setContent(html)
  await page.evaluate(() => {
    document.querySelectorAll('[data-testid="chat-container"]').forEach((chat, index) => {
      if (!chat.hasAttribute('data-chat-instance-id'))
        chat.setAttribute('data-chat-instance-id', `synthetic-chat-${index}`)
    })
  })
}

async function renderToolImages(page: Page, layout: ToolLayout, name: string, image: string, includeResultImage: boolean, hiddenNamedImage = false): Promise<void> {
  const picture = `<button aria-label="Open image"><img alt="Tool picture" width="10" height="10" src="data:image/png;base64,${image}"></button>`
  const unrelated = `<div data-testid="message-bubble" data-tool-call-id="other-call" data-tool-row-role="result" data-tool-status="completed" data-message-seq="1"><span>Unrelated result</span>${picture}</div>`
  const resultPicture = includeResultImage ? picture : ''
  const named = layout === 'mcp'
    ? `<div data-testid="message-bubble" data-tool-call-id="named-call" data-tool-row-role="result" data-tool-status="completed" data-message-seq="2"><span>MCP image ${name}</span>${resultPicture}</div>`
    : layout === 'merged'
      ? `<div data-testid="message-bubble" data-tool-call-id="named-call" data-tool-row-role="update" data-tool-status="completed" data-message-seq="2"><div data-tool-message>${name}</div>${resultPicture}</div>`
      : `<div data-testid="message-bubble" data-tool-call-id="named-call" data-tool-row-role="request" data-tool-status="completed" data-message-seq="2"><div data-tool-message>${name}</div></div><div data-testid="message-bubble" data-tool-call-id="later-call" data-tool-row-role="result" data-tool-status="completed" data-message-seq="3"><span>Interleaved result</span>${picture}</div><div data-testid="message-bubble" data-tool-call-id="named-call" data-tool-row-role="result" data-tool-status="completed" data-message-seq="4"><span>Read result</span>${resultPicture}</div>`
  const hidden = hiddenNamedImage
    ? `<div data-chat-premeasure-root="true" style="display:none"><div data-testid="message-bubble" data-tool-call-id="named-call" data-tool-row-role="update" data-tool-status="completed" data-message-seq="2">${picture}</div></div>`
    : ''
  await setToolImageContent(page, `<div data-testid="chat-container"><main data-chat-scroll-container="true">${unrelated}${named}</main>${hidden}</div>`)
}

async function setNamedToolStatus(page: Page, status: string): Promise<void> {
  await page.evaluate((value) => {
    const bubble = document.querySelector('[data-chat-scroll-container="true"] [data-tool-call-id="named-call"]')
    if (!bubble)
      throw new Error('the synthetic named tool bubble is absent')
    bubble.setAttribute('data-tool-status', value)
  }, status)
}

async function renderToolResultInAnotherTile(page: Page, name: string, image: string): Promise<void> {
  await setToolImageContent(page, `
    <div data-testid="chat-container" data-test-tile="request">
      <main data-chat-scroll-container="true">
        <div data-testid="message-bubble" data-tool-call-id="shared-call" data-tool-row-role="request" data-tool-status="completed" data-message-seq="1"><div data-tool-message>${name}</div></div>
      </main>
    </div>
    <div data-testid="chat-container" data-test-tile="unrelated">
      <main data-chat-scroll-container="true">
        <div data-testid="message-bubble" data-tool-call-id="shared-call" data-tool-row-role="result" data-tool-status="completed" data-message-seq="2"><span>Another agent's result</span><button aria-label="Open image"><img alt="Other tile's picture" width="10" height="10" src="data:image/png;base64,${image}"></button></div>
      </main>
    </div>
  `)
}

test.describe('tool image result locators', () => {
  test('binds an MCP image to its named result bubble', async ({ page }) => {
    const directory = createTestDirectory('mcp-image-scope-')
    const name = writeToolImage(directory, 'mcp-scope')
    const image = readFileSync(join(directory, name)).toString('base64')

    await renderToolImages(page, 'mcp', name, image, false)
    await expect(page.getByText(`MCP image ${name}`, { exact: true })).toBeVisible()
    expect(await (await mcpResultImage(page, name, 'named-call')).count()).toBe(0)

    await renderToolImages(page, 'mcp', name, image, true)
    expect(await (await mcpResultImage(page, name, 'named-call')).count()).toBe(1)
    await expectMcpToolImage(page, name, 'named-call')
  })

  test('binds a merged tool image to its named call', async ({ page }) => {
    const directory = createTestDirectory('merged-image-scope-')
    const name = writeToolImage(directory, 'merged-scope')
    const image = readFileSync(join(directory, name)).toString('base64')

    await renderToolImages(page, 'merged', name, image, false)
    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: name })).toHaveCount(1)
    expect(await (await toolResultImageForName(page, name)).count()).toBe(0)

    await renderToolImages(page, 'merged', name, image, true)
    expect(await (await toolResultImageForName(page, name)).count()).toBe(1)
    await expectToolRowImage(page, name)
  })

  test('binds a split tool image to its paired result bubble', async ({ page }) => {
    const directory = createTestDirectory('split-image-scope-')
    const name = writeToolImage(directory, 'split-scope')
    const image = readFileSync(join(directory, name)).toString('base64')

    await renderToolImages(page, 'split', name, image, false)
    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: name })).toHaveCount(1)
    expect(await (await toolResultImageForName(page, name)).count()).toBe(0)

    await renderToolImages(page, 'split', name, image, true)
    expect(await (await toolResultImageForName(page, name)).count()).toBe(1)
    await expectToolRowImage(page, name)
  })

  test('ignores another call image but checks a hidden copy of the named call', async ({ page }) => {
    const directory = createTestDirectory('negative-image-scope-')
    const name = writeToolImage(directory, 'negative-scope')
    const image = readFileSync(join(directory, name)).toString('base64')

    await renderToolImages(page, 'merged', name, image, false)
    expect(await (await imagesForToolCall(page, name)).count()).toBe(0)
    await expectToolRowWithoutImage(page, name)

    await renderToolImages(page, 'merged', name, image, false, true)
    expect(await (await imagesForToolCall(page, name)).count()).toBe(1)
  })

  test('does not claim no image before the named tool finishes', async ({ page }) => {
    const directory = createTestDirectory('active-negative-image-')
    const name = writeToolImage(directory, 'active-scope')
    const image = readFileSync(join(directory, name)).toString('base64')
    await renderToolImages(page, 'merged', name, image, false)
    await setNamedToolStatus(page, 'in_progress')

    expect(await isNamedToolResultFinished(page, name)).toBe(false)
    await setNamedToolStatus(page, 'completed')
    expect(await isNamedToolResultFinished(page, name)).toBe(true)
    await expectToolRowWithoutImage(page, name)
  })

  test('requires a paired result even when its request reports a finished status', async ({ page }) => {
    const directory = createTestDirectory('split-finished-image-')
    const name = writeToolImage(directory, 'split-finished')
    await setToolImageContent(page, `<div data-testid="chat-container"><main data-chat-scroll-container="true"><div data-testid="message-bubble" data-tool-call-id="split-call" data-tool-row-role="request" data-tool-status="completed" data-message-seq="1"><div data-tool-message>${name}</div></div></main></div>`)

    expect(await isNamedToolResultFinished(page, name)).toBe(false)
    await page.evaluate(() => {
      const container = document.querySelector('[data-chat-scroll-container="true"]')
      if (!container)
        throw new Error('the synthetic chat container is absent')
      container.insertAdjacentHTML('beforeend', '<div data-testid="message-bubble" data-tool-call-id="split-call" data-tool-row-role="result" data-tool-status="completed" data-message-seq="2"><span>Text-only result</span></div>')
    })
    expect(await isNamedToolResultFinished(page, name)).toBe(true)
    await expectToolRowWithoutImage(page, name)
  })

  test('does not pair a reused call ID with an older image when its new result is absent', async ({ page }) => {
    const directory = createTestDirectory('reused-positive-image-')
    const oldName = writeToolImage(directory, 'old-call')
    const newName = writeToolImage(directory, 'new-call')
    const image = readFileSync(join(directory, oldName)).toString('base64')
    await setToolImageContent(page, `
      <div data-testid="chat-container"><main data-chat-scroll-container="true">
        <div data-testid="message-bubble" data-tool-call-id="reused-call" data-tool-row-role="request" data-message-seq="1"><div data-tool-message>${oldName}</div></div>
        <div data-testid="message-bubble" data-tool-call-id="reused-call" data-tool-row-role="result" data-message-seq="2"><button aria-label="Open image"><img alt="Older picture" width="10" height="10" src="data:image/png;base64,${image}"></button></div>
        <div data-testid="message-bubble" data-tool-call-id="reused-call" data-tool-row-role="request" data-message-seq="3"><div data-tool-message>${newName}</div></div>
      </main></div>
    `)

    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: newName })).toHaveCount(1)
    expect(await (await toolResultImageForName(page, newName)).count()).toBe(0)
  })

  test('does not count an older same-ID image in a new text-only call', async ({ page }) => {
    const directory = createTestDirectory('reused-negative-image-')
    const oldName = writeToolImage(directory, 'old-call')
    const newName = writeToolImage(directory, 'new-call')
    const image = readFileSync(join(directory, oldName)).toString('base64')
    await setToolImageContent(page, `
      <div data-testid="chat-container"><main data-chat-scroll-container="true">
        <div data-testid="message-bubble" data-tool-call-id="reused-call" data-tool-row-role="request" data-message-seq="1"><div data-tool-message>${oldName}</div></div>
        <div data-testid="message-bubble" data-tool-call-id="reused-call" data-tool-row-role="result" data-message-seq="2"><button aria-label="Open image"><img alt="Older picture" width="10" height="10" src="data:image/png;base64,${image}"></button></div>
        <div data-testid="message-bubble" data-tool-call-id="reused-call" data-tool-row-role="request" data-message-seq="3"><div data-tool-message>${newName}</div></div>
        <div data-testid="message-bubble" data-tool-call-id="reused-call" data-tool-row-role="result" data-message-seq="4"><span>Text-only result</span></div>
      </main></div>
    `)

    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: newName })).toHaveCount(1)
    expect(await (await imagesForToolCall(page, newName)).count()).toBe(0)
  })

  test('does not pair a split image with a result in another visible chat tile', async ({ page }) => {
    const directory = createTestDirectory('other-tile-image-')
    const name = writeToolImage(directory, 'other-tile')
    const image = readFileSync(join(directory, name)).toString('base64')
    await renderToolResultInAnotherTile(page, name, image)

    await expect(page.locator('[data-testid="chat-container"]:visible')).toHaveCount(2)
    expect(await (await toolResultImageForName(page, name)).count()).toBe(0)
    await page.evaluate((data) => {
      const scroller = document.querySelector('[data-test-tile="request"] [data-chat-scroll-container="true"]')
      if (!scroller)
        throw new Error('the request tile has no chat scroller')
      scroller.insertAdjacentHTML('beforeend', `<div data-testid="message-bubble" data-tool-call-id="shared-call" data-tool-row-role="result" data-tool-status="completed" data-message-seq="2"><button aria-label="Open image"><img alt="Request tile picture" width="10" height="10" src="data:image/png;base64,${data}"></button></div>`)
    }, image)
    expect(await (await toolResultImageForName(page, name)).count()).toBe(1)
  })

  test('does not finish a split call with a result in another visible chat tile', async ({ page }) => {
    const directory = createTestDirectory('other-tile-status-')
    const name = writeToolImage(directory, 'other-tile-status')
    const image = readFileSync(join(directory, name)).toString('base64')
    await renderToolResultInAnotherTile(page, name, image)

    await expect(page.locator('[data-testid="chat-container"]:visible')).toHaveCount(2)
    expect(await isNamedToolResultFinished(page, name)).toBe(false)
    await page.evaluate(() => {
      const scroller = document.querySelector('[data-test-tile="request"] [data-chat-scroll-container="true"]')
      if (!scroller)
        throw new Error('the request tile has no chat scroller')
      scroller.insertAdjacentHTML('beforeend', '<div data-testid="message-bubble" data-tool-call-id="shared-call" data-tool-row-role="result" data-tool-status="completed" data-message-seq="2"><span>Request tile result</span></div>')
    })
    expect(await isNamedToolResultFinished(page, name)).toBe(true)
  })

  test('does not use an MCP image result from another visible chat tile', async ({ page }) => {
    const directory = createTestDirectory('other-tile-mcp-')
    const name = writeToolImage(directory, 'other-tile-mcp')
    const image = readFileSync(join(directory, name)).toString('base64')
    await setToolImageContent(page, `
      <div data-testid="chat-container" data-test-tile="request">
        <main data-chat-scroll-container="true">
          <div data-testid="message-bubble" data-tool-call-id="shared-mcp-call" data-tool-row-role="request" data-message-seq="1"><div data-tool-message>Call image_probe show</div></div>
        </main>
      </div>
      <div data-testid="chat-container" data-test-tile="unrelated">
        <main data-chat-scroll-container="true">
          <div data-testid="message-bubble" data-tool-call-id="shared-mcp-call" data-tool-row-role="result" data-message-seq="2"><span>MCP image ${name}</span><button aria-label="Open image"><img alt="Other tile's picture" width="10" height="10" src="data:image/png;base64,${image}"></button></div>
        </main>
      </div>
    `)

    await expect(page.locator('[data-testid="chat-container"]:visible')).toHaveCount(2)
    expect(await (await mcpResultImage(page, name, 'shared-mcp-call')).count()).toBe(0)
    await page.evaluate(([fileName, data]) => {
      const scroller = document.querySelector('[data-test-tile="request"] [data-chat-scroll-container="true"]')
      if (!scroller)
        throw new Error('the request tile has no chat scroller')
      scroller.insertAdjacentHTML('beforeend', `<div data-testid="message-bubble" data-tool-call-id="shared-mcp-call" data-tool-row-role="result" data-message-seq="2"><span>MCP image ${fileName}</span><button aria-label="Open image"><img alt="Request tile picture" width="10" height="10" src="data:image/png;base64,${data}"></button></div>`)
    }, [name, image])
    expect(await (await mcpResultImage(page, name, 'shared-mcp-call')).count()).toBe(1)
  })

  test('rejects an MCP call ID that matches requests in two visible chats', async ({ page }) => {
    const directory = createTestDirectory('ambiguous-tile-mcp-')
    const name = writeToolImage(directory, 'ambiguous-tile')
    await setToolImageContent(page, `
      <div data-testid="chat-container"><main data-chat-scroll-container="true"><div data-testid="message-bubble" data-tool-call-id="ambiguous-mcp-call" data-tool-row-role="request" data-message-seq="1"><div data-tool-message>Call image_probe show</div></div></main></div>
      <div data-testid="chat-container"><main data-chat-scroll-container="true"><div data-testid="message-bubble" data-tool-call-id="ambiguous-mcp-call" data-tool-row-role="request" data-message-seq="1"><div data-tool-message>Call image_probe show</div></div><div data-testid="message-bubble" data-tool-call-id="ambiguous-mcp-call" data-tool-row-role="result" data-message-seq="2"><span>MCP image ${name}</span></div></main></div>
    `)

    await expect(page.locator('[data-testid="chat-container"]:visible')).toHaveCount(2)
    await expect(mcpResultImage(page, name, 'ambiguous-mcp-call')).rejects.toThrow('the MCP call ID matches rows in multiple visible chat tiles')
  })

  test('does not reuse an older MCP image when the same file is requested again', async ({ page }) => {
    const directory = createTestDirectory('reused-mcp-image-')
    const name = writeToolImage(directory, 'same-file')
    const image = readFileSync(join(directory, name)).toString('base64')
    await setToolImageContent(page, `
      <div data-testid="chat-container"><main data-chat-scroll-container="true">
        <div data-testid="message-bubble" data-tool-call-id="old-mcp-call" data-tool-row-role="result" data-message-seq="1"><span>MCP image ${name}</span><button aria-label="Open image"><img alt="Older picture" width="10" height="10" src="data:image/png;base64,${image}"></button></div>
        <div data-testid="message-bubble" data-tool-call-id="new-mcp-call" data-tool-row-role="request" data-message-seq="2"><div data-tool-message>Call image_probe show for ${name}</div></div>
      </main></div>
    `)

    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: name })).toHaveCount(1)
    expect(await (await mcpResultImage(page, name, 'new-mcp-call')).count()).toBe(0)
  })

  test('does not reuse an older MCP image when the call ID also repeats', async ({ page }) => {
    const directory = createTestDirectory('reused-mcp-id-image-')
    const name = writeToolImage(directory, 'reused-id')
    const image = readFileSync(join(directory, name)).toString('base64')
    await setToolImageContent(page, `
      <div data-testid="chat-container"><main data-chat-scroll-container="true">
        <div data-testid="message-bubble" data-tool-call-id="reused-mcp-call" data-tool-row-role="request" data-message-seq="1"><div data-tool-message>Call image_probe show for ${name}</div></div>
        <div data-testid="message-bubble" data-tool-call-id="reused-mcp-call" data-tool-row-role="result" data-message-seq="2"><span>MCP image ${name}</span><button aria-label="Open image"><img alt="Older picture" width="10" height="10" src="data:image/png;base64,${image}"></button></div>
        <div data-testid="message-bubble" data-tool-call-id="reused-mcp-call" data-tool-row-role="request" data-message-seq="3"><div data-tool-message>Call image_probe show for ${name}</div></div>
      </main></div>
    `)

    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: name })).toHaveCount(2)
    expect(await (await mcpResultImage(page, name, 'reused-mcp-call')).count()).toBe(0)
  })

  test('does not mix a call ID with a replacement row sequence', async ({ page }) => {
    const directory = createTestDirectory('replaced-tool-identity-')
    const name = writeToolImage(directory, 'replaced-row')
    const image = readFileSync(join(directory, name)).toString('base64')
    await setToolImageContent(page, `
      <div data-testid="chat-container"><main data-chat-scroll-container="true">
        <div data-testid="message-bubble" data-tool-call-id="old-call" data-tool-row-role="request" data-message-seq="1"><div data-tool-message>${name}</div></div>
        <div data-testid="message-bubble" data-tool-call-id="old-call" data-tool-row-role="result" data-message-seq="2"><button aria-label="Open image"><img alt="Original result" width="10" height="10" src="data:image/png;base64,${image}"></button></div>
      </main></div>
    `)

    let prototype = Object.getPrototypeOf(page.locator('body'))
    while (prototype && !Object.hasOwn(prototype, 'getAttribute'))
      prototype = Object.getPrototypeOf(prototype)
    const original = prototype && Object.getOwnPropertyDescriptor(prototype, 'getAttribute')
    if (!original || typeof original.value !== 'function')
      throw new Error('Playwright Locator has no replaceable getAttribute method')
    let replaced = false
    Object.defineProperty(prototype, 'getAttribute', {
      ...original,
      async value(this: Locator, attributeName: string, ...args: unknown[]) {
        const value = await Reflect.apply(original.value, this, [attributeName, ...args])
        if (attributeName === 'data-tool-call-id' && !replaced) {
          replaced = true
          await page.evaluate((fileName) => {
            const row = document.querySelector('[data-tool-call-id="old-call"][data-tool-row-role="request"]')
            if (!row)
              throw new Error('the original named tool row is absent')
            const next = document.createElement('div')
            next.setAttribute('data-testid', 'message-bubble')
            next.setAttribute('data-tool-call-id', 'new-call')
            next.setAttribute('data-tool-row-role', 'request')
            next.setAttribute('data-message-seq', '3')
            const label = document.createElement('div')
            label.setAttribute('data-tool-message', '')
            label.textContent = fileName
            next.append(label)
            row.replaceWith(next)
          }, name)
        }
        return value
      },
    })
    try {
      expect(await (await toolResultImageForName(page, name)).count()).toBe(1)
    }
    finally {
      Object.defineProperty(prototype, 'getAttribute', original)
    }
  })

  test('keeps the selected chat when its named row disappears before result pairing', async ({ page }) => {
    const directory = createTestDirectory('replaced-chat-tile-')
    const name = writeToolImage(directory, 'replaced-chat-tile')
    const image = readFileSync(join(directory, name)).toString('base64')
    await setToolImageContent(page, `
      <div data-testid="chat-container" data-chat-instance-id="other-chat" data-test-tile="unrelated">
        <main data-chat-scroll-container="true">
          <div data-testid="message-bubble" data-tool-call-id="shared-tile-call" data-tool-row-role="request" data-message-seq="1"><div data-tool-message>${name}</div></div>
          <div data-testid="message-bubble" data-tool-call-id="shared-tile-call" data-tool-row-role="result" data-message-seq="2"><span>Other tile has no image</span></div>
        </main>
      </div>
      <div data-testid="chat-container" data-chat-instance-id="selected-chat" data-test-tile="request">
        <main data-chat-scroll-container="true">
          <div data-testid="message-bubble" data-tool-call-id="shared-tile-call" data-tool-row-role="request" data-message-seq="1"><div data-tool-message>${name}</div></div>
          <div data-testid="message-bubble" data-tool-call-id="shared-tile-call" data-tool-row-role="result" data-message-seq="2"><button aria-label="Open image"><img alt="Selected tile picture" width="10" height="10" src="data:image/png;base64,${image}"></button></div>
        </main>
      </div>
    `)
    await page.evaluate(() => {
      const original = CSS.escape
      CSS.escape = (value) => {
        CSS.escape = original
        const request = document.querySelector('[data-test-tile="request"] [data-tool-row-role="request"]')
        if (!request)
          throw new Error('the selected chat request is absent')
        request.remove()
        return original(value)
      }
    })

    try {
      expect(await (await toolResultImageForName(page, name)).count()).toBe(1)
    }
    finally {
      await page.evaluate(() => CSS.escape('restore-once'))
    }
  })

  test('does not accept another picture for a Goose result that names the file', async ({ page }) => {
    const directory = createTestDirectory('goose-image-scope-')
    const name = writeToolImage(directory, 'goose-scope')
    const image = readFileSync(join(directory, name)).toString('base64')
    await setToolImageContent(page, `
      <div data-testid="chat-container"><main data-chat-scroll-container="true">
        <div data-testid="message-bubble" data-tool-call-id="other-call" data-tool-row-role="result" data-message-seq="1"><button aria-label="Open image"><img alt="Other picture" width="10" height="10" src="data:image/png;base64,${image}"></button></div>
        <div data-testid="message-bubble" data-tool-call-id="goose-call" data-tool-row-role="result" data-message-seq="2"><span>Loaded image from ${name}</span></div>
      </main></div>
    `)
    const namedResult = page.locator('[data-testid="message-bubble"]:visible')
      .filter({ hasText: 'Loaded image from' })
      .filter({ hasText: name })
    await expect(namedResult.first()).toBeVisible()
    expect(await imageInBubble(namedResult.first()).count()).toBe(0)
    await page.evaluate((data) => {
      const result = document.querySelector('[data-tool-call-id="goose-call"]')
      if (!result)
        throw new Error('the synthetic Goose result is absent')
      result.insertAdjacentHTML('beforeend', `<button aria-label="Open image"><img alt="Goose result picture" width="10" height="10" src="data:image/png;base64,${data}"></button>`)
    }, image)

    expect(await imageInBubble(namedResult.first()).count()).toBe(1)
    await expectDecodedImageInBubble(namedResult.first())
  })

  test('does not accept another picture for a Cursor GenerateImage result', async ({ page }) => {
    const directory = createTestDirectory('cursor-image-scope-')
    const name = writeToolImage(directory, 'cursor-scope')
    const image = readFileSync(join(directory, name)).toString('base64')
    await setToolImageContent(page, `
      <div data-testid="chat-container"><main data-chat-scroll-container="true">
        <div data-testid="message-bubble" data-tool-call-id="other-call" data-tool-row-role="result" data-message-seq="1"><button aria-label="Open image"><img alt="A teal square" width="10" height="10" src="data:image/png;base64,${image}"></button></div>
        <div data-testid="message-bubble" data-tool-call-id="cursor-call" data-tool-row-role="request" data-message-seq="2"><div data-tool-message>A teal square</div></div>
        <div data-testid="message-bubble" data-tool-call-id="cursor-call" data-tool-row-role="result" data-message-seq="3"><span>Image request finished without a picture: ${name}</span></div>
      </main></div>
    `)
    await expect(toolRows(page).filter({ hasText: 'A teal square' }).first()).toBeVisible()
    expect(await (await toolResultImageForName(page, 'A teal square')).count()).toBe(0)
    await page.evaluate((data) => {
      const result = document.querySelector('[data-tool-call-id="cursor-call"][data-tool-row-role="result"]')
      if (!result)
        throw new Error('the synthetic Cursor result is absent')
      result.insertAdjacentHTML('beforeend', `<button aria-label="Open image"><img alt="A teal square" width="10" height="10" src="data:image/png;base64,${data}"></button>`)
    }, image)

    const resultImage = await toolResultImageForName(page, 'A teal square')
    expect(await resultImage.count()).toBe(1)
    await expectToolRowImage(page, 'A teal square')
    await expect(resultImage).toHaveAttribute('alt', 'A teal square')
  })
})
