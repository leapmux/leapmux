import { readdirSync, readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'bun:test'
import {
  activeRootsFor,
  CACHE_SNAPSHOT_VERSION,
  cacheKey,
  cacheKinds,
  cachePolicy,
  INSTALLER_OWNED_CACHES,
  PARTITION_PATTERN,
  partitionPrefix,
  restoreKeys,
  rootActivePath,
  rootPayloadPath,
  scanCacheMechanisms,
  snapshotPaths,
  taskArtifactRecordName,
  validateCacheKey,
  validatePolicy,
} from './cache-policy.mjs'

const linux = {
  RUNNER_OS: 'Linux',
  RUNNER_ARCH: 'X64',
  CACHE_IMAGE: 'ubuntu24',
  GOLANG_VERSION: '1.27.1',
  RUST_VERSION: '1.99.0',
  BUN_VERSION: '1.4.4',
  TASK_VERSION: '3.46.0',
  RUNNER_TEMP: '/runner/temp',
  HOME: '/home/fixture',
}
const hash = 'a'.repeat(64)

describe('cache snapshot core', () => {
  describe('cachePolicy', () => {
    it('rejects inherited object members as unknown cache kinds', () => {
      for (const kind of ['constructor', '__proto__', 'toString']) expect(() => cachePolicy(kind)).toThrow('Unknown cache kind')
    })
    it('defines one policy for every supported kind', () => {
      const kinds = cacheKinds()
      expect(kinds).toHaveLength(12)
      for (const kind of kinds) {
        const policy = cachePolicy(kind)
        expect(policy.kind).toBe(kind)
        expect(policy.description).toBeString()
        expect(policy.writers).toBeString()
        expect(policy.roots.length).toBeGreaterThan(0)
      }
      expect(cacheKinds().sort()).toEqual([
        'apt-packages',
        'bun-packages',
        'cargo-downloads',
        'docker-go',
        'go-modules',
        'native-go',
        'native-rust',
        'renovate-re2',
        'site-go',
        'site-go-modules',
        'spinners',
        'tauri-tools',
      ])
    })

    it('fails an unknown kind before any filesystem use', () => {
      expect(() => cachePolicy('rust-target')).toThrow('Unknown cache kind')
      expect(() => cachePolicy('')).toThrow('Unknown cache kind')
    })

    it('fails an incomplete definition before any filesystem use', () => {
      const complete = {
        ...cachePolicy('go-modules'),
      }
      for (const [field, value, message] of [
        ['kind', '', '(missing kind)'],
        ['description', '', 'go-modules'],
        ['population', '', 'go-modules'],
        ['category', 'registry', 'go-modules'],
        ['roots', [], 'go-modules'],
        ['keyedByWeek', 'yes', 'go-modules'],
        ['hashSegment', 'optional', 'go-modules'],
        ['fallback', 'prefix-and-hash', 'go-modules'],
      ]) {
        const broken = { ...complete, [field]: value }
        expect(() => validatePolicy(broken), field).toThrow(`The cache policy ${message} is invalid`)
      }
      expect(() => validatePolicy({
        ...complete,
        roots: [
          { ...complete.roots[0], name: 'gomod', active: 'not a resolver' },
        ],
      })).toThrow('active must be a resolver function')
      expect(() => validatePolicy({
        ...complete,
        roots: [
          complete.roots[0],
          { ...complete.roots[0], mode: 'merge' },
        ],
      })).toThrow('duplicate root name gomod')
    })
  })

  describe('partitionPrefix', () => {
    it('embeds the snapshot generation in every partition', () => {
      for (const kind of cacheKinds()) {
        const prefix = partitionPrefix(kind, linux)
        expect(prefix, kind).toContain(`-${CACHE_SNAPSHOT_VERSION}-`)
        expect(prefix, kind).toEndWith('-')
      }
    })

    it('composes the documented partitions from the runner environment', () => {
      expect(partitionPrefix('go-modules', linux)).toBe(`go-modules-${CACHE_SNAPSHOT_VERSION}-Linux-`)
      expect(partitionPrefix('bun-packages', linux)).toBe(`bun-packages-${CACHE_SNAPSHOT_VERSION}-Linux-X64-`)
      expect(partitionPrefix('native-go', linux)).toBe(`go-build-${CACHE_SNAPSHOT_VERSION}-Linux-X64-ubuntu24-go1.27.1-`)
      expect(partitionPrefix('native-rust', linux)).toBe(`rust-build-${CACHE_SNAPSHOT_VERSION}-Linux-X64-ubuntu24-rust1.99.0-`)
      expect(partitionPrefix('docker-go', linux)).toBe(`docker-go-build-${CACHE_SNAPSHOT_VERSION}-amd64-go1.27.1-`)
      expect(partitionPrefix('docker-go', { ...linux, RUNNER_ARCH: 'ARM64' })).toEndWith('-arm64-go1.27.1-')
      expect(partitionPrefix('renovate-re2', linux)).toBe(`renovate-re2-${CACHE_SNAPSHOT_VERSION}-Linux-X64-ubuntu24-node24-bun1.4.4-task3.46.0-`)
      expect(partitionPrefix('spinners', linux)).toBe(`spinners-${CACHE_SNAPSHOT_VERSION}-Linux-`)
    })

    it('refuses a partition whose environment inputs are missing', () => {
      for (const [kind, missing] of [
        ['native-go', 'CACHE_IMAGE'],
        ['native-rust', 'RUST_VERSION'],
        ['docker-go', 'GOLANG_VERSION'],
        ['go-modules', 'RUNNER_OS'],
        ['bun-packages', 'RUNNER_ARCH'],
      ]) {
        const env = { ...linux }
        delete env[missing]
        expect(() => partitionPrefix(kind, env), kind).toThrow(missing)
      }
      expect(() => partitionPrefix('docker-go', { ...linux, RUNNER_ARCH: 'arm' })).toThrow('RUNNER_ARCH')
    })

    it('refuses an unsupported runner architecture for Docker', () => {
      expect(() => partitionPrefix('docker-go', { ...linux, RUNNER_ARCH: 'ARM' })).toThrow('RUNNER_ARCH')
    })
  })

  describe('cacheKey', () => {
    it('composes dependency keys from the current content hash without a week', () => {
      expect(cacheKey('go-modules', { hash }, linux)).toBe(`go-modules-${CACHE_SNAPSHOT_VERSION}-Linux-${hash}`)
      expect(cacheKey('cargo-downloads', { hash }, linux)).toBe(`cargo-downloads-${CACHE_SNAPSHOT_VERSION}-Linux-${hash}`)
      expect(() => cacheKey('go-modules', { hash, week: '2027-W01' }, linux)).toThrow('carries no weekly epoch')
      expect(() => cacheKey('go-modules', {}, linux)).toThrow('sha256')
    })

    it('composes weekly keys from the hash and the ISO UTC week', () => {
      expect(cacheKey('native-go', { hash, week: '2027-W01' }, linux))
        .toBe(`go-build-${CACHE_SNAPSHOT_VERSION}-Linux-X64-ubuntu24-go1.27.1-${hash}-2027-W01`)
      expect(() => cacheKey('native-go', { hash, week: '2027-W54' }, linux)).toThrow('ISO UTC week')
      expect(() => cacheKey('native-go', { hash, week: '2027-W00' }, linux)).toThrow('ISO UTC week')
      // 2026 begins on a Thursday, so it has a week 53; 2027 begins on a
      // Friday and has none.
      expect(cacheKey('native-go', { hash, week: '2026-W53' }, linux)).toEndWith('-2026-W53')
      expect(() => cacheKey('native-go', { hash, week: '2027-W53' }, linux)).toThrow('ISO UTC week')
    })

    it('composes the spinner key from the week alone', () => {
      expect(cacheKey('spinners', { week: '2027-W01' }, linux)).toBe(`spinners-${CACHE_SNAPSHOT_VERSION}-Linux-2027-W01`)
      expect(() => cacheKey('spinners', { hash, week: '2027-W01' }, linux)).toThrow('carries no hash segment')
    })

    it('accepts the two hash segments of the Renovate RE2 key', () => {
      const recipeHash = 'b'.repeat(64)
      expect(cacheKey('renovate-re2', { hash: `${hash}-${recipeHash}`, week: '2027-W01' }, linux))
        .toEndWith(`-${hash}-${recipeHash}-2027-W01`)
      expect(() => cacheKey('renovate-re2', { hash: 'short', week: '2027-W01' }, linux)).toThrow('sha256')
    })
  })

  describe('validateCacheKey', () => {
    it('accepts the exact keys that cacheKey composes', () => {
      const cases = [
        [
          'go-modules',
          { hash },
        ],
        [
          'bun-packages',
          { hash },
        ],
        [
          'native-go',
          { hash, week: '2027-W01' },
        ],
        [
          'native-rust',
          { hash },
        ],
        [
          'docker-go',
          { hash, week: '2027-W01' },
        ],
        [
          'site-go',
          { hash, week: '2027-W01' },
        ],
        [
          'apt-packages',
          { hash, week: '2027-W01' },
        ],
        [
          'tauri-tools',
          { hash, week: '2027-W01' },
        ],
        [
          'spinners',
          { week: '2027-W01' },
        ],
        [
          'renovate-re2',
          { hash: `${hash}-${'b'.repeat(64)}`, week: '2027-W01' },
        ],
      ]
      for (const [kind, inputs] of cases) {
        const key = cacheKey(kind, inputs, linux)
        const validated = validateCacheKey(kind, key, linux)
        expect(validated.week, kind).toBe(inputs.week ?? null)
      }
    })

    it('rejects keys of another partition, generation, or shape', () => {
      const rejections = [
        // A previous generation prefix never validates.
        ['go-modules', `go-modules-1-Linux-${hash}`],
        ['native-go', `go-build-Linux-X64-ubuntu24-go1.27.1-${hash}-2027-W01`],
        ['docker-go', `docker-go-build-arm64-go1.27.1-${hash}-2027-W01`],
        // Another kind's partition.
        [
          'go-modules',
          cacheKey('native-go', { hash, week: '2027-W01' }, linux),
        ],
        [
          'native-go',
          cacheKey('go-modules', { hash }, linux),
        ],
        // A corrupted key inside the same partition.
        ['native-go', `${cacheKey('native-go', { hash, week: '2027-W01' }, linux)}x`],
        ['go-modules', `${partitionPrefix('go-modules', linux)}${hash.slice(0, 32)}`],
        ['native-go', `${partitionPrefix('native-go', linux)}${hash}-2027-W01-extra`],
        // Malformed content.
        ['go-modules', ''],
        ['go-modules', `${partitionPrefix('go-modules', linux)}\n${hash}`],
        ['go-modules', undefined],
      ]
      for (const [kind, key] of rejections)
        expect(() => validateCacheKey(kind, key, linux), `${kind}: ${String(key)}`).toThrow()
    })

    it('rejects the week 53 of a year that has none', () => {
      const prefix = partitionPrefix('native-go', linux)
      expect(() => validateCacheKey('native-go', `${prefix}${hash}-2027-W53`, linux)).toThrow('week')
      expect(() => validateCacheKey('native-go', `${prefix}${hash}-2026-W53`, linux)).not.toThrow()
    })
  })

  describe('restoreKeys', () => {
    it('permits no fallback for exact-only kinds', () => {
      for (const kind of [
        'go-modules',
        'site-go-modules',
        'bun-packages',
        'cargo-downloads',
        'spinners',
        'apt-packages',
        'tauri-tools',
      ])
        expect(restoreKeys(kind, { hash }, linux)).toBeNull()
    })

    it('permits the partition prefix as a reader fallback for the compiled kinds', () => {
      expect(restoreKeys('native-go', { hash }, linux)).toBe(`go-build-${CACHE_SNAPSHOT_VERSION}-Linux-X64-ubuntu24-go1.27.1-`)
      expect(restoreKeys('docker-go', { hash }, linux)).toBe(`docker-go-build-${CACHE_SNAPSHOT_VERSION}-amd64-go1.27.1-`)
      expect(restoreKeys('site-go', { hash }, linux)).toBe(`site-go-${CACHE_SNAPSHOT_VERSION}-Linux-go1.27.1-`)
      expect(restoreKeys('native-rust', { hash }, linux)).toBe(`rust-build-${CACHE_SNAPSHOT_VERSION}-Linux-X64-ubuntu24-rust1.99.0-`)
    })

    it('keeps the dependency hash in the Renovate RE2 fallback but not the recipe hash', () => {
      const recipeHash = 'b'.repeat(64)
      expect(restoreKeys('renovate-re2', { hash: `${hash}-${recipeHash}` }, linux))
        .toBe(`renovate-re2-${CACHE_SNAPSHOT_VERSION}-Linux-X64-ubuntu24-node24-bun1.4.4-task3.46.0-${hash}-`)
      expect(restoreKeys('renovate-re2', { hash: `${hash}-${recipeHash}` }, linux)).not.toContain(recipeHash)
      expect(() => restoreKeys('renovate-re2', {}, linux)).toThrow('dependency hash')
    })

    it('never produces a fallback that matches an older generation', () => {
      for (const kind of cacheKinds()) {
        const fallback = restoreKeys(kind, { hash }, linux)
        if (fallback === null)
          continue
        expect(fallback, kind).toContain(`-${CACHE_SNAPSHOT_VERSION}-`)
      }
    })
  })

  describe('PARTITION_PATTERN', () => {
    it('matches the compiled Go partitions of this generation', () => {
      for (const prefix of [
        `go-build-${CACHE_SNAPSHOT_VERSION}-Linux-X64-ubuntu24-go1.27.1-`,
        `go-build-${CACHE_SNAPSHOT_VERSION}-macOS-ARM64-macos26-go1.27.1-`,
        `go-build-${CACHE_SNAPSHOT_VERSION}-Windows-X64-win22-go1.27.1-`,
        `docker-go-build-${CACHE_SNAPSHOT_VERSION}-amd64-go1.27.1-`,
        `docker-go-build-${CACHE_SNAPSHOT_VERSION}-arm64-go1.27.1-`,
      ])
        expect(PARTITION_PATTERN.test(prefix), prefix).toBe(true)
    })

    it('never matches an older generation or an unrelated partition', () => {
      for (const prefix of [
        `go-build-Linux-X64-ubuntu24-go1.27.1-`,
        `docker-go-build-amd64-go1.27.1-`,
        `go-build-${CACHE_SNAPSHOT_VERSION - 1}-Linux-X64-ubuntu24-go1.27.1-`,
        `docker-go-build-${CACHE_SNAPSHOT_VERSION}-`,
        `bun-packages-${CACHE_SNAPSHOT_VERSION}-Linux-X64-`,
        '',
      ])
        expect(PARTITION_PATTERN.test(prefix), prefix).toBe(false)
    })
  })

  describe('snapshotPaths', () => {
    it('stages every kind beneath the workspace .tmp directory', () => {
      const paths = snapshotPaths('native-go', '/work/leapmux')
      expect(paths.stagingRoot).toBe('/work/leapmux/.tmp/ci-cache/snapshots/native-go')
      expect(paths.payloadRoot).toBe('/work/leapmux/.tmp/ci-cache/snapshots/native-go/payload')
    })

    it('refuses an empty workspace', () => {
      expect(() => snapshotPaths('native-go', '')).toThrow('workspace')
    })
  })

  describe('activeRootsFor', () => {
    it('resolves the native Go roots from the environment', () => {
      const roots = activeRootsFor('native-go', { ...linux, GOCACHE: '/home/fixture/.cache/go-build', GOLANGCI_LINT_CACHE: '/home/fixture/.cache/golangci-lint' }, '/work/leapmux')
      expect(roots.map(root => root.name)).toEqual(['gocache', 'lint-cache'])
      expect(roots[0].active).toBe('/home/fixture/.cache/go-build')
      expect(roots[0].payload).toBe('/work/leapmux/.tmp/ci-cache/snapshots/native-go/payload/gocache')
      expect(roots[0].clearOnWriterMiss).toBe(true)
    })

    it('skips an optional root whose environment input is absent', () => {
      const roots = activeRootsFor('native-go', { ...linux, GOCACHE: '/home/fixture/.cache/go-build' }, '/work/leapmux')
      expect(roots.map(root => root.name)).toEqual(['gocache'])
    })

    it('requires the primary root and fails when its input is absent', () => {
      expect(() => activeRootsFor('native-go', linux, '/work/leapmux')).toThrow('gocache')
      // The site build cache requires the Go build directory of the job.
      expect(() => activeRootsFor('site-go', { ...linux, GOCACHE: '' }, '/work/leapmux')).toThrow('does not apply')
    })

    it('keeps the Rust active target inside the workspace and the sources under the Cargo home', () => {
      const roots = activeRootsFor('native-rust', linux, '/work/leapmux')
      expect(roots.map(root => root.name)).toEqual(['target', 'registry-src', 'git-checkouts'])
      expect(roots[0].active).toBe('/work/leapmux/desktop/rust/target')
      expect(roots[1].active).toBe('/home/fixture/.cargo/registry/src')
      expect(roots[2].active).toBe('/home/fixture/.cargo/git/checkouts')
    })

    it('resolves dependency roots from the tool defaults', () => {
      expect(activeRootsFor('go-modules', linux, '/work').map(root => root.active)).toEqual(['/home/fixture/go/pkg/mod'])
      expect(activeRootsFor('bun-packages', linux, '/work').map(root => root.active)).toEqual(['/home/fixture/.bun/install/cache'])
      expect(activeRootsFor('cargo-downloads', linux, '/work').map(root => root.active)).toEqual(['/home/fixture/.cargo'])
      expect(activeRootsFor('go-modules', { ...linux, GOMODCACHE: '/custom/mod' }, '/work').map(root => root.active)).toEqual(['/custom/mod'])
    })

    it('applies the APT and Renovate roots to their runner shapes', () => {
      expect(activeRootsFor('apt-packages', linux, '/work').map(root => root.active)).toEqual(['/var/cache/apt/archives'])
      expect(activeRootsFor('apt-packages', { ...linux, RUNNER_OS: 'Windows' }, '/work').map(root => root.active)).toEqual(['/var/cache/apt/archives'])
      const re2 = activeRootsFor('renovate-re2', linux, '/work/leapmux')
      expect(re2.map(root => root.name)).toEqual(['re2-node', 'task-checksum', 'task-record'])
      expect(re2.every(root => root.type === 'file')).toBe(true)
      expect(re2[2].active).toEndWith(`/.task/artifacts/${taskArtifactRecordName('build-renovate-re2')}`)
    })

    it('skips the Tauri root on macOS and refuses a required root that the runner cannot provide', () => {
      const macos = { ...linux, RUNNER_OS: 'macOS' }
      expect(() => activeRootsFor('tauri-tools', macos, '/work')).toThrow('does not apply')
      const windows = { ...linux, RUNNER_OS: 'Windows', USERPROFILE: '/c/fixture' }
      expect(activeRootsFor('tauri-tools', windows, '/work').map(root => root.active)).toEqual(['/c/fixture/AppData/Local/tauri'])
      expect(activeRootsFor('tauri-tools', linux, '/work').map(root => root.active)).toEqual(['/home/fixture/.cache/tauri'])
    })

    it('resolves the spinner subtree inside the clone that Task owns', () => {
      const roots = activeRootsFor('spinners', linux, '/work')
      expect(roots[0].active).toBe('/home/fixture/.cache/awesome-claude-spinners/spinners')
      expect(roots[0].payload).toEndWith('/spinners')
    })

    it('resolves a root payload and active path by name', () => {
      expect(rootPayloadPath('go-modules', 'gomod', linux, '/work')).toBe('/work/.tmp/ci-cache/snapshots/go-modules/payload/gomod')
      expect(rootActivePath('docker-go', 'go-build', linux, '/work')).toBe('/runner/temp/docker-go-build')
      expect(() => rootPayloadPath('go-modules', 'unknown', linux, '/work')).toThrow('no applicable root')
    })
  })

  describe('taskArtifactRecordName', () => {
    it('matches the artifact record that the CI workflow caches', () => {
      expect(taskArtifactRecordName('build-renovate-re2')).toBe('041ee1d9a6f1e20e6f63880ffd41e7502068f85f0c919c572e79d28cd36b3b68.sha256')
    })
  })

  describe('INSTALLER_OWNED_CACHES', () => {
    it('classifies the installer caches that no project snapshot may collect', () => {
      const mechanisms = INSTALLER_OWNED_CACHES.map(entry => entry.mechanism)
      expect(mechanisms).toContain('oven-sh/setup-bun')
      expect(mechanisms).toContain('rustup (RUSTUP_HOME)')
      for (const entry of INSTALLER_OWNED_CACHES) {
        expect(entry.owner).toBeString()
        expect(entry.contents).toBeString()
      }
    })
  })
})

describe('cache inventory guard', () => {
  const repository = resolve(import.meta.dirname, '..')

  function allSources() {
    const entries = []
    const workflowDir = join(repository, '.github', 'workflows')
    for (const name of readdirSync(workflowDir).filter(name => name.endsWith('.yaml'))) {
      const doc = Bun.YAML.parse(readFileSync(join(workflowDir, name), 'utf8'))
      const steps = Object.entries(doc.jobs ?? {}).flatMap(([jobName, job]) => (job.steps ?? []).map(step => ({ jobName, step })))
      entries.push({ source: `.github/workflows/${name}`, steps })
    }
    const actionDir = join(repository, '.github', 'actions')
    for (const name of readdirSync(actionDir)) {
      const path = join(actionDir, name, 'action.yaml')
      let doc
      try {
        doc = Bun.YAML.parse(readFileSync(path, 'utf8'))
      }
      catch {
        continue
      }
      if (!doc?.runs?.steps)
        continue
      entries.push({
        source: `.github/actions/${name}/action.yaml`,
        steps: doc.runs.steps.map(step => ({ jobName: '', step })),
      })
    }
    return entries
  }

  it('classifies every cache mechanism that the current workflows and actions use', () => {
    expect(scanCacheMechanisms(allSources())).toEqual([])
  })

  it('fails a deliberately unclassified cache step', () => {
    const steps = [
      {
        jobName: 'probe',
        step: {
          name: 'Cache the world',
          uses: 'actions/cache@v4',
          with: { path: 'x' },
        },
      },
    ]
    expect(scanCacheMechanisms([
      { source: '.github/workflows/probe.yaml', steps },
    ])).toHaveLength(1)
    const rustCache = [
      {
        jobName: '',
        step: { name: 'Rust', uses: 'Swatinem/rust-cache@v2' },
      },
    ]
    expect(scanCacheMechanisms([
      { source: 'probe', steps: rustCache },
    ])).toHaveLength(1)
    const goCache = [
      {
        jobName: '',
        step: {
          name: 'Go',
          uses: 'actions/setup-go@v5',
          with: { 'go-version': '1.27' },
        },
      },
    ]
    expect(scanCacheMechanisms([
      { source: 'probe', steps: goCache },
    ])).toHaveLength(1)
    const goDisabled = [
      {
        jobName: '',
        step: {
          name: 'Go',
          uses: 'actions/setup-go@v5',
          with: { 'go-version': '1.27', 'cache': false },
        },
      },
    ]
    expect(scanCacheMechanisms([
      { source: 'probe', steps: goDisabled },
    ])).toEqual([])
    const nodeCache = [
      {
        jobName: '',
        step: {
          name: 'Node',
          uses: 'actions/setup-node@v4',
          with: { cache: 'bun' },
        },
      },
    ]
    expect(scanCacheMechanisms([
      { source: 'probe', steps: nodeCache },
    ])).toHaveLength(1)
    // The transport action files may use the pinned transport steps themselves.
    const transport = [
      {
        jobName: '',
        step: {
          name: 'Restore',
          uses: 'actions/cache/restore@v4',
          with: { path: 'p', key: 'k' },
        },
      },
    ]
    expect(scanCacheMechanisms([
      { source: '.github/actions/build-output-cache/action.yaml', steps: transport },
    ])).toEqual([])
    expect(scanCacheMechanisms([
      { source: '.github/actions/save-cache-snapshot/action.yaml', steps: transport },
    ])).toEqual([])
  })
})
