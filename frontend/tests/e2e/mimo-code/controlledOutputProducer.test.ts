import type { MiMoProducerFile } from './controlledOutputProducer'
import { describe, expect, it, vi } from 'vitest'
import { observeMiMoNativeOutputSize } from './controlledOutputProducer'

describe('observeMiMoNativeOutputSize', () => {
  it('releases only after a new native file reaches the expected size', async () => {
    let changed: () => void = () => {
      throw new Error('The native watcher has no handler.')
    }
    let files = [{ path: '/native/tool_one', byteLength: 3, identity: 'native-file' }]
    const release = vi.fn()
    const stop = vi.fn()
    const control = observeMiMoNativeOutputSize('complete', new Set(), {
      files: () => files,
      release,
      subscribeFile: () => () => {},
      subscribe: (handler) => {
        changed = handler
        return stop
      },
    })
    expect(release).not.toHaveBeenCalled()
    files = [{ path: '/native/tool_one', byteLength: 8, identity: 'native-file' }]
    changed()
    await expect(control.observedPath).resolves.toBe('/native/tool_one')
    changed()
    control.close()
    expect(release).toHaveBeenCalledTimes(1)
  })

  it('does not release for a prior file with the expected size', () => {
    const release = vi.fn()
    const control = observeMiMoNativeOutputSize('complete', new Set(['/native/prior']), {
      files: () => [{ path: '/native/prior', byteLength: 8, identity: 'native-file' }],
      release,
      subscribeFile: () => () => {},
      subscribe: () => () => {},
    })
    const rejected = expect(control.observedPath).rejects.toThrow('ended before')
    expect(release).not.toHaveBeenCalled()
    control.close()
    return rejected
  })

  it('keeps the exact watcher failure and still releases the held producer in cleanup', async () => {
    const failure = new Error('The native file-size observer failed.')
    const release = vi.fn()
    const control = observeMiMoNativeOutputSize('complete', new Set(), {
      files: () => [],
      release,
      subscribeFile: () => () => {},
      subscribe: (_changed, failed) => {
        failed(failure)
        return () => {}
      },
    })
    await expect(control.observedPath).rejects.toBe(failure)
    control.close()
    expect(release).toHaveBeenCalledTimes(1)
  })

  it('keeps an exact metadata inspection failure', async () => {
    const failure = new Error('The native file metadata cannot be read.')
    const control = observeMiMoNativeOutputSize('complete', new Set(), {
      files: () => {
        throw failure
      },
      release: () => {},
      subscribeFile: () => () => {},
      subscribe: () => () => {},
    })
    await expect(control.observedPath).rejects.toBe(failure)
    control.close()
  })
})

describe('observeMiMoNativeOutputSize diagnostics', () => {
  it('records partial native state without releasing the held producer', async () => {
    const release = vi.fn()
    const control = observeMiMoNativeOutputSize('complete', new Set(['/native/prior']), {
      files: () => [{ path: '/native/prior', byteLength: 8, identity: 'native-file' }, { path: '/native/current', byteLength: 3, identity: 'native-file' }],
      release,
      subscribeFile: () => () => {},
      subscribe: () => () => {},
    })
    expect(control.diagnostic()).toMatchObject({ expectedBytes: 8, finished: false, released: false, inspections: 1, lastFiles: [{ path: '/native/prior', byteLength: 8, prior: true }, { path: '/native/current', byteLength: 3, prior: false }] })
    const copied = control.diagnostic()
    const prior = copied.lastFiles[0]
    if (!prior)
      throw new Error('The observer diagnostic requires the prior file entry.')
    prior.byteLength = 999
    expect(control.diagnostic().lastFiles[0]?.byteLength).toBe(8)
    expect(release).not.toHaveBeenCalled()
    const rejected = expect(control.observedPath).rejects.toThrow('ended before')
    control.close()
    await rejected
  })
})

describe('observeMiMoNativeOutputSize file notifications', () => {
  it('observes the expected size without a second directory notification', async () => {
    let fileChanged: (() => void) | undefined
    let bytes = 3
    const release = vi.fn()
    const stopFile = vi.fn()
    const subscribeFile = vi.fn((_path: string, changed: () => void) => {
      fileChanged = changed
      return stopFile
    })
    const control = observeMiMoNativeOutputSize('complete', new Set(), {
      files: () => [{ path: '/native/tool_partial', byteLength: bytes, identity: 'native-file' }],
      release,
      subscribe: () => () => {},
      subscribeFile,
    })
    const settled = control.observedPath.then(path => ({ path }), error => ({ error }))
    try {
      expect(subscribeFile).toHaveBeenCalledTimes(1)
      expect(subscribeFile.mock.calls[0]?.[0]).toBe('/native/tool_partial')
      expect(release).not.toHaveBeenCalled()
      bytes = 8
      if (!fileChanged)
        throw new Error('The native partial file requires its own change notification.')
      fileChanged()
      await expect(settled).resolves.toEqual({ path: '/native/tool_partial' })
      expect(release).toHaveBeenCalledTimes(1)
      expect(stopFile).toHaveBeenCalledTimes(1)
    }
    finally {
      control.close()
      await settled
    }
  })
})

function fileNotificationFixture(initial: MiMoProducerFile[]) {
  let files = initial
  let directoryChanged: (() => void) | undefined
  const fileChanged = new Map<string, () => void>()
  const fileStops: ReturnType<typeof vi.fn>[] = []
  const release = vi.fn()
  const directoryStop = vi.fn()
  const control = observeMiMoNativeOutputSize('complete', new Set(), {
    files: () => files,
    release,
    subscribe: (changed) => {
      directoryChanged = changed
      return directoryStop
    },
    subscribeFile: (path, changed) => {
      fileChanged.set(path, changed)
      const stop = vi.fn(() => fileChanged.delete(path))
      fileStops.push(stop)
      return stop
    },
  })
  return {
    control,
    release,
    directoryStop,
    fileStops,
    replaceFiles: (next: MiMoProducerFile[]) => { files = next },
    notifyDirectory: () => {
      if (!directoryChanged)
        throw new Error('The native directory has no observer.')
      directoryChanged()
    },
    notifyFile: (path: string) => {
      const handler = fileChanged.get(path)
      if (!handler)
        throw new Error('The native file has no observer.')
      handler()
    },
  }
}

describe('observeMiMoNativeOutputSize observer lifecycle', () => {
  it('observes a final write that occurs while the file observer installs', async () => {
    let bytes = 3
    const release = vi.fn()
    const control = observeMiMoNativeOutputSize('complete', new Set(), {
      files: () => [{ path: '/native/tool_current', byteLength: bytes, identity: 'first' }],
      release,
      subscribe: () => () => {},
      subscribeFile: () => {
        bytes = 8
        return () => {}
      },
    })
    await expect(control.observedPath).resolves.toBe('/native/tool_current')
    expect(release).toHaveBeenCalledTimes(1)
    control.close()
  })

  it('replaces the old inode observer before observing the new file', async () => {
    const native = fileNotificationFixture([{ path: '/native/tool_current', byteLength: 3, identity: 'first' }])
    native.replaceFiles([{ path: '/native/tool_current', byteLength: 3, identity: 'second' }])
    native.notifyFile('/native/tool_current')
    expect(native.fileStops).toHaveLength(2)
    expect(native.fileStops[0]).toHaveBeenCalledTimes(1)
    expect(native.release).not.toHaveBeenCalled()
    native.replaceFiles([{ path: '/native/tool_current', byteLength: 8, identity: 'second' }])
    native.notifyFile('/native/tool_current')
    await expect(native.control.observedPath).resolves.toBe('/native/tool_current')
    expect(native.fileStops[1]).toHaveBeenCalledTimes(1)
    expect(native.directoryStop).toHaveBeenCalledTimes(1)
    native.control.close()
  })

  it('closes a removed file observer and discovers its replacement path', async () => {
    const native = fileNotificationFixture([{ path: '/native/tool_removed', byteLength: 3, identity: 'first' }])
    native.replaceFiles([])
    native.notifyDirectory()
    expect(native.fileStops[0]).toHaveBeenCalledTimes(1)
    native.replaceFiles([{ path: '/native/tool_current', byteLength: 8, identity: 'second' }])
    native.notifyDirectory()
    await expect(native.control.observedPath).resolves.toBe('/native/tool_current')
    expect(native.fileStops[1]).toHaveBeenCalledTimes(1)
    native.control.close()
  })

  it('keeps concurrent files with different sizes separate and closes both observers', async () => {
    const native = fileNotificationFixture([
      { path: '/native/tool_foreign', byteLength: 3, identity: 'first' },
      { path: '/native/tool_current', byteLength: 3, identity: 'second' },
    ])
    native.replaceFiles([
      { path: '/native/tool_foreign', byteLength: 7, identity: 'first' },
      { path: '/native/tool_current', byteLength: 3, identity: 'second' },
    ])
    native.notifyFile('/native/tool_foreign')
    expect(native.release).not.toHaveBeenCalled()
    native.replaceFiles([
      { path: '/native/tool_foreign', byteLength: 7, identity: 'first' },
      { path: '/native/tool_current', byteLength: 8, identity: 'second' },
    ])
    native.notifyFile('/native/tool_current')
    await expect(native.control.observedPath).resolves.toBe('/native/tool_current')
    expect(native.release).toHaveBeenCalledTimes(1)
    for (const stop of native.fileStops)
      expect(stop).toHaveBeenCalledTimes(1)
    native.control.close()
  })

  it('preserves a file subscription error and closes the directory observer', async () => {
    const failure = new Error('The native file observer failed to install.')
    const directoryStop = vi.fn()
    const release = vi.fn()
    const control = observeMiMoNativeOutputSize('complete', new Set(), {
      files: () => [{ path: '/native/tool_current', byteLength: 3, identity: 'first' }],
      release,
      subscribe: () => directoryStop,
      subscribeFile: () => { throw failure },
    })
    await expect(control.observedPath).rejects.toBe(failure)
    expect(directoryStop).toHaveBeenCalledTimes(1)
    expect(release).not.toHaveBeenCalled()
    control.close()
    expect(release).toHaveBeenCalledTimes(1)
  })

  it('refuses an inode replacement during observer installation', async () => {
    let identity = 'first'
    const release = vi.fn()
    const stop = vi.fn()
    const control = observeMiMoNativeOutputSize('complete', new Set(), {
      files: () => [{ path: '/native/tool_current', byteLength: 3, identity }],
      release,
      subscribe: () => () => {},
      subscribeFile: () => {
        identity = 'second'
        return stop
      },
    })
    await expect(control.observedPath).rejects.toThrow('changed during observer installation')
    expect(stop).toHaveBeenCalledTimes(1)
    expect(release).not.toHaveBeenCalled()
    control.close()
  })

  it('preserves close errors and closes every other observer', async () => {
    const failure = new Error('The native file observer failed to close.')
    const directoryStop = vi.fn()
    const release = vi.fn()
    const control = observeMiMoNativeOutputSize('complete', new Set(), {
      files: () => [{ path: '/native/tool_current', byteLength: 8, identity: 'first' }],
      release,
      subscribe: () => directoryStop,
      subscribeFile: () => () => { throw failure },
    })
    await expect(control.observedPath).rejects.toMatchObject({ errors: [failure] })
    expect(directoryStop).toHaveBeenCalledTimes(1)
    expect(release).not.toHaveBeenCalled()
    control.close()
    expect(release).toHaveBeenCalledTimes(1)
  })
})

describe('observeMiMoNativeOutputSize synchronous subscription failures', () => {
  it.each(['directory', 'file'] as const)('preserves a closer failure after a synchronous %s subscription error', async (source) => {
    const failure = new Error('The native subscription failed immediately.')
    const closeFailure = new Error('The native late observer failed to close.')
    const close = () => {
      throw closeFailure
    }
    const release = vi.fn()
    const control = observeMiMoNativeOutputSize('complete', new Set(), {
      files: () => [{ path: '/native/tool_current', byteLength: 3, identity: 'first' }],
      release,
      subscribe: (_changed, failed) => {
        if (source === 'directory') {
          failed(failure)
          return close
        }
        return () => {}
      },
      subscribeFile: (_path, _changed, failed) => {
        failed(failure)
        return close
      },
    })
    await expect(control.observedPath).rejects.toBe(failure)
    expect(() => control.close()).toThrow('late native MiMo observers failed to close')
    expect(release).toHaveBeenCalledTimes(1)
  })
})

describe('observeMiMoNativeOutputSize metadata limits', () => {
  it.each([-1, 0, 3, 9, Number.NaN, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1])('does not release for an invalid or different size %s', async (byteLength) => {
    const release = vi.fn()
    const control = observeMiMoNativeOutputSize('complete', new Set(), {
      files: () => [{ path: '/native/tool_current', byteLength, identity: 'current' }],
      release,
      subscribe: () => () => {},
      subscribeFile: () => () => {},
    })
    const rejection = expect(control.observedPath).rejects.toThrow('ended before')
    expect(release).not.toHaveBeenCalled()
    control.close()
    await rejection
    expect(release).toHaveBeenCalledTimes(1)
  })

  it('does not release for an absent file identity', async () => {
    const release = vi.fn()
    const control = observeMiMoNativeOutputSize('complete', new Set(), {
      files: () => [{ path: '/native/tool_current', byteLength: 8, identity: '' }],
      release,
      subscribe: () => () => {},
      subscribeFile: () => () => {},
    })
    const rejection = expect(control.observedPath).rejects.toThrow('ended before')
    expect(release).not.toHaveBeenCalled()
    control.close()
    await rejection
  })

  it('uses UTF-8 size for the independently computed producer output', async () => {
    const control = observeMiMoNativeOutputSize('結果', new Set(), {
      files: () => [{ path: '/native/tool_current', byteLength: 6, identity: 'current' }],
      release: () => {},
      subscribe: () => () => {},
      subscribeFile: () => () => {},
    })
    await expect(control.observedPath).resolves.toBe('/native/tool_current')
    expect(control.diagnostic().expectedBytes).toBe(6)
    control.close()
  })

  it.each(['', 'x'.repeat(4 * 1024 * 1024 + 1)])('rejects an empty or excessive expected output', (expected) => {
    expect(() => observeMiMoNativeOutputSize(expected, new Set(), {
      files: () => [],
      release: () => {},
      subscribe: () => () => {},
      subscribeFile: () => () => {},
    })).toThrow('requires nonempty expected output within four MiB')
  })

  it('preserves a producer release failure after it closes subscriptions', async () => {
    const failure = new Error('The producer release failed.')
    const stop = vi.fn()
    const release = vi.fn(() => {
      throw failure
    })
    const control = observeMiMoNativeOutputSize('complete', new Set(), {
      files: () => [{ path: '/native/tool_current', byteLength: 8, identity: 'current' }],
      release,
      subscribe: () => stop,
      subscribeFile: () => stop,
    })
    await expect(control.observedPath).rejects.toBe(failure)
    expect(stop).toHaveBeenCalledTimes(2)
    expect(() => control.close()).toThrow(failure)
    expect(release).toHaveBeenCalledTimes(2)
  })
})
