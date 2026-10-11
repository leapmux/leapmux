import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { installControllableResizeObserver, triggerResizeObserverForSync, triggerResizeObserversSync } from './resizeObserverStub'

describe('installControllableResizeObserver', () => {
  let originalResizeObserver: typeof ResizeObserver | undefined

  beforeEach(() => {
    originalResizeObserver = globalThis.ResizeObserver
    installControllableResizeObserver()
  })

  afterEach(() => {
    vi.restoreAllMocks()
    if (originalResizeObserver)
      globalThis.ResizeObserver = originalResizeObserver
    else
      Reflect.deleteProperty(globalThis, 'ResizeObserver')
  })

  it('emits a complete entry for an observed target', () => {
    const target = document.createElement('div')
    const rectangle = new DOMRect(7, 13, 41, 29)
    vi.spyOn(target, 'getBoundingClientRect').mockReturnValue(rectangle)
    const callback = vi.fn<ResizeObserverCallback>()
    const observer = new ResizeObserver(callback)
    observer.observe(target)

    triggerResizeObserverForSync(target)

    expect(callback).toHaveBeenCalledTimes(1)
    expect(callback).toHaveBeenCalledWith([{
      target,
      contentRect: rectangle,
      borderBoxSize: [{ inlineSize: 41, blockSize: 29 }],
      contentBoxSize: [{ inlineSize: 41, blockSize: 29 }],
      devicePixelContentBoxSize: [{ inlineSize: 41, blockSize: 29 }],
    }], observer)
  })

  it('preserves zero width and height in every size array', () => {
    const target = document.createElement('div')
    const rectangle = new DOMRect(0, 0, 0, 0)
    vi.spyOn(target, 'getBoundingClientRect').mockReturnValue(rectangle)
    const callback = vi.fn<ResizeObserverCallback>()
    const observer = new ResizeObserver(callback)
    observer.observe(target)

    triggerResizeObserverForSync(target)

    expect(callback).toHaveBeenCalledWith([{
      target,
      contentRect: rectangle,
      borderBoxSize: [{ inlineSize: 0, blockSize: 0 }],
      contentBoxSize: [{ inlineSize: 0, blockSize: 0 }],
      devicePixelContentBoxSize: [{ inlineSize: 0, blockSize: 0 }],
    }], observer)
  })

  it('reads the current rectangle when the trigger runs', () => {
    const target = document.createElement('div')
    const firstRectangle = new DOMRect(0, 0, 20, 10)
    const currentRectangle = new DOMRect(0, 0, 43, 57)
    const readRectangle = vi.spyOn(target, 'getBoundingClientRect').mockReturnValue(firstRectangle)
    const callback = vi.fn<ResizeObserverCallback>()
    const observer = new ResizeObserver(callback)
    observer.observe(target)
    expect(readRectangle).not.toHaveBeenCalled()

    readRectangle.mockReturnValue(currentRectangle)
    triggerResizeObserverForSync(target)

    expect(readRectangle).toHaveBeenCalledTimes(1)
    expect(callback.mock.calls[0]?.[0][0]?.contentRect).toBe(currentRectangle)
  })

  it('passes the actual observer as the callback receiver', () => {
    const target = document.createElement('div')
    const callback = vi.fn<ResizeObserverCallback>()
    const observer = new ResizeObserver(callback)
    observer.observe(target)

    triggerResizeObserverForSync(target)

    expect(callback.mock.calls[0]?.[1]).toBe(observer)
    expect(callback.mock.calls[0]?.[1]).toBeInstanceOf(ResizeObserver)
  })

  it('emits one entry when the same target is observed twice', () => {
    const target = document.createElement('div')
    const callback = vi.fn<ResizeObserverCallback>()
    const observer = new ResizeObserver(callback)
    observer.observe(target)
    observer.observe(target)

    triggerResizeObserversSync()

    expect(callback).toHaveBeenCalledTimes(1)
    expect(callback.mock.calls[0]?.[0]).toHaveLength(1)
    expect(callback.mock.calls[0]?.[0][0]?.target).toBe(target)
  })

  it('triggers only observers that own the selected target', () => {
    const selectedTarget = document.createElement('div')
    const otherTarget = document.createElement('div')
    const selectedCallback = vi.fn<ResizeObserverCallback>()
    const otherCallback = vi.fn<ResizeObserverCallback>()
    const selectedObserver = new ResizeObserver(selectedCallback)
    const otherObserver = new ResizeObserver(otherCallback)
    selectedObserver.observe(selectedTarget)
    otherObserver.observe(otherTarget)

    triggerResizeObserverForSync(selectedTarget)

    expect(selectedCallback).toHaveBeenCalledTimes(1)
    expect(selectedCallback.mock.calls[0]?.[0][0]?.target).toBe(selectedTarget)
    expect(otherCallback).not.toHaveBeenCalled()
  })

  it('stops targeted callbacks after unobserve and ignores an absent target', () => {
    const target = document.createElement('div')
    const absentTarget = document.createElement('div')
    const callback = vi.fn<ResizeObserverCallback>()
    const observer = new ResizeObserver(callback)
    observer.observe(target)
    observer.unobserve(absentTarget)
    triggerResizeObserverForSync(target)
    expect(callback).toHaveBeenCalledTimes(1)
    callback.mockClear()

    observer.unobserve(target)
    triggerResizeObserverForSync(target)
    triggerResizeObserverForSync(absentTarget)

    expect(callback).not.toHaveBeenCalled()
    triggerResizeObserversSync()
    expect(callback).toHaveBeenCalledWith([], observer)
  })

  it('stops all callbacks after repeated disconnect', () => {
    const target = document.createElement('div')
    const callback = vi.fn<ResizeObserverCallback>()
    const observer = new ResizeObserver(callback)
    observer.observe(target)
    triggerResizeObserverForSync(target)
    expect(callback).toHaveBeenCalledTimes(1)
    callback.mockClear()

    observer.disconnect()
    observer.disconnect()
    triggerResizeObserverForSync(target)
    triggerResizeObserversSync()

    expect(callback).not.toHaveBeenCalled()
  })

  it('replaces the previous observer registry when installed again', () => {
    const target = document.createElement('div')
    const previousCallback = vi.fn<ResizeObserverCallback>()
    const previousObserver = new ResizeObserver(previousCallback)
    previousObserver.observe(target)

    installControllableResizeObserver()
    const currentCallback = vi.fn<ResizeObserverCallback>()
    const currentObserver = new ResizeObserver(currentCallback)
    currentObserver.observe(target)
    triggerResizeObserverForSync(target)

    expect(previousCallback).not.toHaveBeenCalled()
    expect(currentCallback).toHaveBeenCalledTimes(1)
    expect(currentCallback.mock.calls[0]?.[1]).toBe(currentObserver)
  })

  it('continues dispatch when the first callback disconnects itself', () => {
    const target = document.createElement('div')
    const firstCallback = vi.fn<ResizeObserverCallback>((_entries, observer) => observer.disconnect())
    const secondCallback = vi.fn<ResizeObserverCallback>()
    const firstObserver = new ResizeObserver(firstCallback)
    const secondObserver = new ResizeObserver(secondCallback)
    firstObserver.observe(target)
    secondObserver.observe(target)

    triggerResizeObserverForSync(target)

    expect(firstCallback).toHaveBeenCalledTimes(1)
    expect(secondCallback).toHaveBeenCalledTimes(1)
    triggerResizeObserverForSync(target)
    expect(firstCallback).toHaveBeenCalledTimes(1)
    expect(secondCallback).toHaveBeenCalledTimes(2)
  })

  it('propagates callback errors and allows explicit cleanup afterward', () => {
    const target = document.createElement('div')
    const callbackError = new Error('Controlled resize callback failed.')
    const callback = vi.fn<ResizeObserverCallback>(() => {
      throw callbackError
    })
    const observer = new ResizeObserver(callback)
    observer.observe(target)

    expect(() => triggerResizeObserverForSync(target)).toThrow(callbackError)
    expect(callback).toHaveBeenCalledTimes(1)
    observer.disconnect()
    expect(() => triggerResizeObserverForSync(target)).not.toThrow()
    expect(callback).toHaveBeenCalledTimes(1)
  })
})
