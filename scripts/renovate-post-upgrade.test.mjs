import { describe, expect, it } from 'bun:test'
import { postUpgrade, validateUpgrades } from './renovate-post-upgrade.mjs'
import { TEST_IMAGE_KEYS } from './reverse-proxy-examples.mjs'

function upgrade(extra = {}) {
  return {
    manager: 'gomod',
    packageFile: 'backend/go.mod',
    depName: 'example.com/library/v7',
    newValue: 'v8.1.0',
    depType: 'require',
    ...extra,
  }
}

function preparation({ failDownload = false, siteParticipates = false } = {}) {
  const files = {
    'backend/go.mod': 'module example.com/backend\ngo 1.27.1\n',
    'desktop/go/go.mod': 'module example.com/desktop\ngo 1.27.1\n',
    'site/go.mod': 'tool example.com/hugo/cmd/hugo\nrequire example.com/hugo v1.0.0\nrequire github.com/imfing/hextra v0.12.3 // indirect\n',
    'site/hugo.yaml': 'module:\n  imports:\n    - path: github.com/imfing/hextra\n',
  }
  const calls = []
  const downloads = new Set()
  let generated = false
  let imports = []
  return {
    calls,
    imports: () => imports,
    options: {
      root: '/fixture',
      trusted: '/trusted',
      read: path => files[path],
      write: (path, content) => { files[path] = content },
      reconcile: async () => {},
      run: (command, args, options = {}) => {
        calls.push({ command, args, options })
        if (/leapmux-dependency-imports(?:\.exe)?$/.test(command)) {
          imports = JSON.parse(options.input)
          expect(generated).toBe(false)
        }
        if (command === 'go' && args[1] === 'download') {
          if (failDownload)
            throw new Error('metadata download failed')
          downloads.add(options.cwd)
        }
        if (command === 'task' && args[0] === 'prepare-backend') {
        // A clean checkout has no generated packages or embedded assets. Generation must
        // start only after every module has metadata, including newly moved tool imports.
          expect([...downloads].sort()).toEqual(siteParticipates
            ? ['backend', 'desktop/go', 'site']
            : ['backend', 'desktop/go'])
          generated = true
        }
        if (command === 'go' && args[1] === 'tidy') {
          expect(generated).toBe(true)
          if (options.cwd === 'site')
            files['site/go.mod'] = files['site/go.mod'].replace(/^require github.com\/imfing\/hextra[^\n]*\n/gm, '')
        }
        if (command === 'go' && args.join(' ') === 'mod edit -json' && options.cwd === 'site') {
          const Require = [
            { Path: 'example.com/hugo', Version: 'v1.0.0' },
          ]
          if (files['site/go.mod'].includes('github.com/imfing/hextra'))
            Require.push({ Path: 'github.com/imfing/hextra', Version: 'v0.12.3', Indirect: true })
          return JSON.stringify({
            Require,
            Tool: [
              { Path: 'example.com/hugo/cmd/hugo' },
            ],
          })
        }
        return command === 'git'
          ? 'backend/go.mod\0backend/source.go\0'
          : ''
      },
    },
  }
}

describe('postUpgrade', () => {
  it('keeps generator and Tauri processing between Go preparation and completion', async () => {
    const fixture = preparation()
    let reconciled = false
    await postUpgrade([
      upgrade(),
      upgrade({
        manager: 'bun',
        packageFile: 'frontend/package.json',
        depName: 'fixture',
        newValue: '2.0.0',
      }),
    ], {
      ...fixture.options,
      run: (command, args, options = {}) => {
        if (command === 'task' && args[0] === 'prepare-backend')
          expect(reconciled).toBe(true)
        return fixture.options.run(command, args, options)
      },
      reconcile: async () => {
        fixture.calls.push({ command: 'reconcile', args: [], options: {} })
        await Promise.resolve()
        reconciled = true
      },
    })
    const phases = fixture.calls.flatMap(({ command, args, options }) => {
      if (command === 'go' && ['download', 'tidy'].includes(args[1]))
        return [`${args[1]}:${options.cwd}`]
      return command === 'task'
        ? [args[0]]
        : command === 'reconcile'
          ? ['reconcile']
          : []
    })
    expect(phases).toEqual([
      'download:backend',
      'download:desktop/go',
      'sync-protobuf-generators',
      'reconcile',
      'prepare-backend',
      'tidy:backend',
      'tidy:desktop/go',
      'generate-notice',
    ])
  })
  it('does not complete Go processing or generate NOTICE when Tauri reconciliation fails', async () => {
    const fixture = preparation()
    await expect(postUpgrade([
      upgrade(),
      upgrade({
        manager: 'cargo',
        packageFile: 'desktop/rust/Cargo.toml',
        depName: 'fixture',
        newValue: '2.0.0',
      }),
    ], {
      ...fixture.options,
      reconcile: async () => {
        throw new Error('Tauri reconciliation failed.')
      },
    })).rejects.toThrow('Tauri reconciliation failed.')
    expect(fixture.calls.filter(call => call.command === 'task').map(call => call.args[0])).toEqual(['sync-protobuf-generators'])
    expect(fixture.calls.some(call => call.command === 'go' && call.args[1] === 'tidy')).toBe(false)
  })
  it('keeps workspace updates independent of the site metadata and commands', async () => {
    for (const packageFile of ['backend/go.mod', 'desktop/go/go.mod']) {
      const fixture = preparation()
      const original = fixture.options
      await postUpgrade([
        upgrade({ packageFile }),
      ], {
        ...original,
        read: (path) => {
          if (path.startsWith('site/'))
            throw new Error('Unrelated site read.')
          return original.read(path)
        },
        run: (command, args, options = {}) => {
          if (options.cwd === 'site')
            throw new Error('Unrelated site command.')
          return original.run(command, args, options)
        },
      })
    }
  })
  it('refreshes a site-only update without requiring workspace metadata, synchronization, or generation', async () => {
    const files = { 'site/go.mod': 'tool example.com/hugo/cmd/hugo\nrequire example.com/hugo v1.1.0\nrequire github.com/imfing/hextra v0.12.3 // indirect\n', 'site/hugo.yaml': 'module:\n  imports:\n    - path: github.com/imfing/hextra\n' }
    const calls = []
    await postUpgrade([
      upgrade({ packageFile: 'site/go.mod', depName: 'example.com/hugo', newValue: 'v1.1.0' }),
    ], {
      root: '/fixture',
      trusted: '/trusted',
      read: (path) => {
        if (!path.startsWith('site/'))
          throw new Error('Unrelated workspace read.')
        return files[path]
      },
      write: (path, content) => { files[path] = content },
      run: (command, args, options = {}) => {
        calls.push([command, args, options])
        if (command === 'task' || args[0] === 'work' || (command === 'go' && options.cwd !== 'site'))
          throw new Error('Unrelated workspace processing.')
        if (command === 'go' && args.join(' ') === 'mod edit -json') {
          return JSON.stringify({
            Require: [
              { Path: 'example.com/hugo', Version: 'v1.1.0' },
              { Path: 'github.com/imfing/hextra', Version: 'v0.12.3', Indirect: true },
            ],
            Tool: [
              { Path: 'example.com/hugo/cmd/hugo' },
            ],
          })
        }
        return command === 'git'
          ? 'site/go.mod\0site/go.sum\0'
          : ''
      },
    })
    expect(calls.some(([command, args]) => command === 'go' && args[1] === 'tidy')).toBe(true)
  })
  it('prepares a clean checkout and all download metadata before generation or tidy', async () => {
    const fixture = preparation()
    await postUpgrade([
      upgrade(),
    ], fixture.options)
    expect(fixture.imports()).toEqual([
      {
        old: 'example.com/library/v7',
        new: 'example.com/library/v8',
        version: 'v8.1.0',
        directories: ['backend', 'desktop/go'],
      },
    ])
    expect(fixture.calls.some(call => call.command === 'task' && call.args[0] === 'generate-notice')).toBe(true)
    expect(fixture.calls.filter(call => call.command === 'go' && call.args[0] === 'work')).toHaveLength(2)
    expect(fixture.calls.filter(call => call.command === 'go' && call.args[0] === 'mod' && call.args[1] === 'tidy').map(call => call.options.cwd)).toEqual(['backend', 'desktop/go'])
  })
  it('keeps independent site and workspace module migrations in their own graphs', async () => {
    const fixture = preparation({ siteParticipates: true })
    await postUpgrade([
      upgrade(),
      upgrade({ packageFile: 'site/go.mod', newValue: 'v9.0.0' }),
    ], fixture.options)
    expect(fixture.imports()).toEqual([
      {
        old: 'example.com/library/v7',
        new: 'example.com/library/v8',
        version: 'v8.1.0',
        directories: ['backend', 'desktop/go'],
      },
      {
        old: 'example.com/library/v7',
        new: 'example.com/library/v9',
        version: 'v9.0.0',
        directories: ['site'],
      },
    ])
    expect(fixture.calls.filter(call => call.options.cwd === 'site').every(call => call.options.env.GOWORK === 'off')).toBe(true)
    expect(fixture.options.read('site/go.mod')).toContain('github.com/imfing/hextra v0.12.3 // indirect')
  })
  it('stops on failed downloads before updated tools execute', async () => {
    const fixture = preparation({ failDownload: true })
    await expect(postUpgrade([
      upgrade(),
    ], fixture.options)).rejects.toThrow('metadata download failed')
    expect(fixture.calls.some(call => call.command === 'task')).toBe(false)
  })
  it('runs only version synchronization on each toolchain branch', async () => {
    for (const depType of [
      'GOLANG_VERSION',
      'BUN_VERSION',
      'RUST_VERSION',
      'NODE_VERSION',
    ]) {
      const fixture = preparation()
      await postUpgrade([
        upgrade({
          manager: 'regex',
          packageFile: 'versions.env',
          depName: 'tool',
          depType,
          newValue: '9.0.0',
        }),
      ], fixture.options)
      expect(fixture.calls).toEqual([
        {
          command: 'task',
          args: ['sync-versions'],
          options: {},
        },
      ])
    }
  })
  it('also pins the asset digests of a CLI tool on its toolchain branch', async () => {
    for (const depType of [
      'BUF_VERSION',
      'PROTOC_VERSION',
      'TASK_VERSION',
    ]) {
      const fixture = preparation()
      await postUpgrade([
        upgrade({
          manager: 'regex',
          packageFile: 'versions.env',
          depName: 'tool',
          depType,
          newValue: '9.0.0',
        }),
      ], fixture.options)
      expect(fixture.calls).toEqual([
        {
          command: 'task',
          args: ['sync-versions'],
          options: {},
        },
        {
          command: 'task',
          args: ['sync-cli-tool-digests'],
          options: {},
        },
      ])
    }
  })
  it('does not generate unrelated artifacts for Actions, container, or Renovate updates', async () => {
    const cases = [
      [
        upgrade({
          manager: 'github-actions',
          packageFile: '.github/workflows/ci.yaml',
          depName: 'actions/checkout',
          newValue: 'v7.0.1',
        }),
        '.github/workflows/ci.yaml',
      ],
      [
        upgrade({
          manager: 'bun',
          packageFile: '.github/renovate/package.json',
          depName: 'renovate',
          newValue: '44.181.0',
        }),
        '.github/renovate/package.json',
      ],
      [
        upgrade({
          manager: 'regex',
          packageFile: 'versions.env',
          depType: 'ALPINE_IMAGE',
          depName: 'alpine',
          newValue: '3.25',
        }),
        'versions.env',
      ],
    ]
    for (const [input, path] of cases) {
      await postUpgrade([input], {
        trusted: '/trusted',
        run: (command) => {
          expect(command).toBe('git')
          return `${path}\0`
        },
        reconcile: async () => { throw new Error('Unrelated update requested Tauri processing.') },
      })
    }
  })
})

describe('validateUpgrades', () => {
  it('processes nested composite actions without invoking unrelated artifact commands', async () => {
    for (const packageFile of ['.github/actions/build/linux/action.yaml', '.github/actions/build/linux/action.yml']) {
      const calls = []
      await postUpgrade([
        upgrade({
          manager: 'github-actions',
          packageFile,
          depName: 'actions/checkout',
          newValue: 'v7.0.1',
        }),
      ], {
        trusted: '/trusted',
        run: (command, args) => {
          calls.push([command, args])
          return `${packageFile}\0`
        },
      })
      expect(calls.map(([command]) => command)).toEqual(['git'])
    }
  })
  it('rejects missing data, unsafe paths, invalid versions, and mixed toolchain branches', () => {
    for (const input of [
      null,
      [],
      [null],
      [
        upgrade({ packageFile: '../go.mod' }),
      ],
      [
        upgrade({ newValue: '-exec=command' }),
      ],
      [
        upgrade({ depName: 'module\nname' }),
      ],
      [
        upgrade({ manager: 'unknown' }),
      ],
      [
        upgrade({
          manager: 'regex',
          packageFile: 'versions.env',
          depType: 'BUN_VERSION',
          newValue: '1.4.0$(id)',
        }),
      ],
    ])
      expect(() => validateUpgrades(input)).toThrow()
    expect(() => validateUpgrades([
      upgrade({
        manager: 'regex',
        packageFile: 'versions.env',
        depType: 'GOLANG_VERSION',
        newValue: '1.28.0',
      }),
      upgrade(),
    ])).toThrow('own branch')
    expect(() => validateUpgrades([
      { manager: 'bun', packageFile: 'frontend/package.json', updateType: 'lockFileMaintenance' },
    ])).not.toThrow()
  })
  it('accepts the -alpine variant only for a reverse proxy test image', () => {
    const pin = (depType, newValue) => upgrade({
      manager: 'regex',
      packageFile: 'versions.env',
      depName: 'nginx',
      depType,
      newValue,
    })
    for (const newValue of ['1.30.0-alpine', '1.30-alpine', '1.30.0']) {
      expect(() => validateUpgrades([
        pin('NGINX_IMAGE', newValue),
      ])).not.toThrow()
    }
    for (const newValue of [
      '1.30.0-alpine-slim',
      '1.30.0-perl',
      '1.30.0-alpine$(id)',
      'latest',
    ]) {
      expect(() => validateUpgrades([
        pin('NGINX_IMAGE', newValue),
      ])).toThrow('Invalid tool or image pin metadata.')
    }
    // A value that starts with a dash fails the earlier check that guards every manager.
    expect(() => validateUpgrades([
      pin('NGINX_IMAGE', '-alpine'),
    ])).toThrow('Invalid target version for nginx.')
    expect(() => validateUpgrades([
      pin('BUN_VERSION', '1.4.5-alpine'),
    ])).toThrow('Invalid tool or image pin metadata.')
    expect(() => validateUpgrades([
      pin('ALPINE_IMAGE', '3.25-alpine'),
    ])).toThrow('Invalid tool or image pin metadata.')
  })
  it('lets the reverse proxy test images share one branch, which is no toolchain branch', () => {
    const grouped = TEST_IMAGE_KEYS.map(depType => upgrade({
      manager: 'regex',
      packageFile: 'versions.env',
      depName: depType.toLowerCase(),
      depType,
      newValue: '9.0.0',
    }))
    expect(validateUpgrades(grouped)).toEqual({ toolchain: false })
    expect(() => validateUpgrades([
      ...grouped,
      upgrade({
        manager: 'regex',
        packageFile: 'versions.env',
        depType: 'GOLANG_VERSION',
        newValue: '1.28.0',
      }),
    ])).toThrow('own branch')
  })
})
