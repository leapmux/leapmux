import { spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import process from 'node:process'
import { describe, expect, it } from 'bun:test'
import { code, configureSkippedDependencies, renderSkippedDependencies } from './renovate-dashboard.mjs'
import { renovateHandlebars } from './renovate-engine.mjs'

const root = resolve(import.meta.dirname, '..')
const ageMs = 3 * 86400000
const options = { ageMs, timeZone: 'Asia/Seoul' }

function alert(name, ecosystem, manifest, { range = '>= 0.15.0, < 0.20.0', fixed = '0.20.0', ghsa = 'GHSA-wrw7-89jp-8q8g', state = 'open' } = {}) {
  return {
    state,
    dependency: { manifest_path: manifest },
    security_advisory: { ghsa_id: ghsa },
    security_vulnerability: {
      package: { name, ecosystem },
      vulnerable_version_range: range,
      first_patched_version: fixed === null
        ? null
        : { identifier: fixed },
    },
  }
}

function data(packageFiles) {
  return new Map([
    ['main', packageFiles],
  ])
}
function blocks(section) {
  return Object.fromEntries([
    ...section.matchAll(/<details><summary>(.+?) \((\d+)\)<\/summary>\n\n([\s\S]*?)\n\n<\/details>/g),
  ].map(match => [
    match[1],
    {
      count: Number(match[2]),
      items: match[3].split('\n'),
    },
  ]))
}

describe('renderSkippedDependencies', () => {
  it('lists an alert for a transitive dependency and omits an alert that Renovate covers', () => {
    const section = renderSkippedDependencies(data({
      cargo: [
        {
          packageFile: 'desktop/rust/Cargo.toml',
          deps: [
            {
              depName: 'tauri',
              updates: [
                { newVersion: '2.12.0', updateType: 'minor' },
              ],
            },
          ],
        },
      ],
    }), {
      ...options,
      alerts: [
        alert('glib', 'rust', 'desktop/rust/Cargo.lock'),
        alert('tauri', 'rust', 'desktop/rust/Cargo.lock', { range: '< 2.12.0', fixed: '2.12.0', ghsa: 'GHSA-2222-3333-4444' }),
      ],
    })
    expect(blocks(section)['Security alerts without a proposed fix']).toEqual({
      count: 1,
      items: ['- `glib` in `desktop/rust/Cargo.lock`: [GHSA-wrw7-89jp-8q8g](https://github.com/advisories/GHSA-wrw7-89jp-8q8g) affects `>= 0.15.0, < 0.20.0`, fixed in `0.20.0`. Transitive dependency. Renovate updates only dependencies that a manifest declares.'],
    })
  })
  it('gives each reason that an alert has no proposed fix', () => {
    const section = renderSkippedDependencies(data({
      bun: [
        {
          packageFile: 'frontend/package.json',
          deps: [
            { depName: 'skipped', skipReason: 'ignored' },
            { depName: 'stuck', updates: [] },
            {
              depName: 'young',
              updates: [
                { newVersion: '2.0.0', pendingChecks: true },
              ],
            },
          ],
        },
      ],
    }), {
      ...options,
      alerts: [
        alert('unfixed', 'npm', 'frontend/package.json', { fixed: null, ghsa: 'GHSA-2345-6789-cfgh' }),
        alert('skipped', 'npm', 'frontend/package.json'),
        alert('stuck', 'npm', 'frontend/package.json'),
        alert('young', 'npm', 'frontend/package.json'),
        alert('wheel', 'pip', 'requirements.txt'),
        alert('closed', 'npm', 'frontend/package.json', { state: 'fixed' }),
      ],
    })
    const items = blocks(section)['Security alerts without a proposed fix'].items
    expect(items).toHaveLength(5)
    expect(items.find(item => item.includes('`unfixed`'))).toEndWith('[GHSA-2345-6789-cfgh](https://github.com/advisories/GHSA-2345-6789-cfgh) affects `>= 0.15.0, < 0.20.0`. No patched version exists.')
    expect(items.find(item => item.includes('`skipped`'))).toEndWith('Renovate skips it: Ignored by configuration.')
    expect(items.find(item => item.includes('`stuck`'))).toEndWith('A patched version exists, but Renovate found no update.')
    expect(items.find(item => item.includes('`young`'))).toEndWith('Every update that fixes it waits for the minimum release age.')
    expect(items.find(item => item.includes('`wheel`'))).toEndWith('Renovate manages no dependency of the `pip` ecosystem.')
    expect(section).not.toContain('closed')
  })
  it('lists an alert whose updates stay in the vulnerable range, and judges the updates that fix it', () => {
    const dep = (depName, updates) => ({ depName, updates })
    const section = renderSkippedDependencies(data({
      bun: [
        {
          packageFile: 'frontend/package.json',
          deps: [
            dep('below', [
              { newVersion: '0.19.5', updateType: 'patch' },
            ]),
            dep('mixed', [
              { newVersion: '0.19.5', updateType: 'patch' },
              { newVersion: '0.20.1', updateType: 'minor', pendingChecks: true },
            ]),
            dep('fixed', [
              { newVersion: '0.19.5', updateType: 'patch', pendingChecks: true },
              { newVersion: '0.20.0', updateType: 'minor' },
            ]),
            dep('digest', [
              { updateType: 'digest' },
            ]),
            dep('go-style', [
              { newVersion: 'v0.20.0', updateType: 'minor' },
            ]),
          ],
        },
      ],
    }), {
      ...options,
      alerts: [
        'below',
        'mixed',
        'fixed',
        'digest',
        'go-style',
      ].map(name => alert(name, 'npm', 'frontend/package.json')),
    })
    const items = blocks(section)['Security alerts without a proposed fix'].items
    expect(items).toHaveLength(2)
    expect(items.find(item => item.includes('`below`'))).toEndWith('Every update that Renovate found stays in the vulnerable range.')
    expect(items.find(item => item.includes('`mixed`'))).toEndWith('Every update that fixes it waits for the minimum release age.')
  })
  it('lists an alert once with each manifest, and a pending update of two files once', () => {
    const pending = {
      depName: 'shared',
      updates: [
        { newVersion: '2.0.0', pendingChecks: true, releaseTimestamp: '2026-10-29T12:00:00Z' },
      ],
    }
    const section = renderSkippedDependencies(data({
      bun: [
        {
          packageFile: 'frontend/package.json',
          deps: [pending],
        },
        {
          packageFile: '.github/renovate/package.json',
          deps: [pending],
        },
      ],
    }), {
      ...options,
      alerts: [
        alert('glib', 'rust', 'desktop/rust/Cargo.lock'),
        alert('glib', 'rust', 'desktop/rust/other/Cargo.lock'),
      ],
    })
    expect(blocks(section)['Security alerts without a proposed fix'].items).toEqual([
      '- `glib` in `desktop/rust/Cargo.lock`, `desktop/rust/other/Cargo.lock`: [GHSA-wrw7-89jp-8q8g](https://github.com/advisories/GHSA-wrw7-89jp-8q8g) affects `>= 0.15.0, < 0.20.0`, fixed in `0.20.0`. Transitive dependency. Renovate updates only dependencies that a manifest declares.',
    ])
    expect(blocks(section)['Waiting for the minimum release age (3 days, dates in Asia/Seoul)'].items).toEqual([
      '- `shared` (`.github/renovate/package.json`, `frontend/package.json`): `2.0.0` waits until 2026-11-01',
    ])
  })
  it('renders an advisory identifier outside the GHSA alphabet as code, not as a link', () => {
    const section = renderSkippedDependencies(new Map(), {
      ...options,
      alerts: [
        alert('glib', 'rust', 'desktop/rust/Cargo.lock', { ghsa: 'GHSA-aaaa-bbbb-cccc](javascript:alert(1))' }),
      ],
    })
    expect(section).toContain('`GHSA-aaaa-bbbb-cccc](javascript:alert(1))` affects')
    expect(section).not.toContain('](https://github.com/advisories/')
  })
  it('shows when a pending update stops waiting, or that it has no release timestamp, and the versions that wait', () => {
    const section = renderSkippedDependencies(data({
      'custom.regex': [
        {
          packageFile: 'versions.env',
          deps: [
            {
              depName: 'ubuntu',
              updates: [
                { newVersion: '26.04', pendingChecks: true, releaseTimestamp: '2026-10-29T12:00:00Z' },
              ],
            },
          ],
        },
      ],
      'gomod': [
        {
          packageFile: 'backend/go.mod',
          deps: [
            {
              depName: 'github.com/realclientip/realclientip-go',
              updates: [
                { updateType: 'digest', pendingChecks: true },
              ],
            },
          ],
        },
      ],
      'bun': [
        {
          packageFile: '.github/renovate/package.json',
          deps: [
            {
              depName: 'renovate',
              updates: [
                {
                  newVersion: '44.190.0',
                  pendingVersions: [
                    '44.190.1',
                    '44.190.2',
                    '44.190.3',
                    '44.190.4',
                  ],
                },
                { updateType: 'lockFileMaintenance', pendingChecks: true },
              ],
            },
          ],
        },
      ],
    }), options)
    expect(blocks(section)['Waiting for the minimum release age (3 days, dates in Asia/Seoul)']).toEqual({
      count: 3,
      items: [
        '- `ubuntu` (`versions.env`): `26.04` waits until 2026-11-01',
        '- `github.com/realclientip/realclientip-go` (`backend/go.mod`): digest update has no release timestamp',
        '- `renovate` (`.github/renovate/package.json`): proposes `44.190.0`; `44.190.1`, `44.190.2`, `44.190.3`, … wait',
      ],
    })
  })
  it('dates the end of a wait on the calendar of the schedule zone, and states the age with its buffer', () => {
    const packageFiles = data({
      bun: [
        {
          packageFile: 'frontend/package.json',
          deps: [
            {
              depName: 'late',
              // The wait ends at 2026-11-01T20:00Z, which is 05:00 on November 2 in Seoul.
              updates: [
                { newVersion: '1.0.0', pendingChecks: true, releaseTimestamp: '2026-10-29T20:00:00Z' },
              ],
            },
          ],
        },
      ],
    })
    expect(blocks(renderSkippedDependencies(packageFiles, options))['Waiting for the minimum release age (3 days, dates in Asia/Seoul)'].items).toEqual([
      '- `late` (`frontend/package.json`): `1.0.0` waits until 2026-11-02',
    ])
    expect(blocks(renderSkippedDependencies(packageFiles, { ageMs, timeZone: 'UTC' }))['Waiting for the minimum release age (3 days, dates in UTC)'].items).toEqual([
      '- `late` (`frontend/package.json`): `1.0.0` waits until 2026-11-01',
    ])
    const buffered = renderSkippedDependencies(packageFiles, { ageMs: ageMs + 1800000, timeZone: 'UTC' })
    expect(blocks(buffered)['Waiting for the minimum release age (3 days 30 minutes, dates in UTC)'].items).toEqual([
      '- `late` (`frontend/package.json`): `1.0.0` waits until 2026-11-01',
    ])
  })
  it('groups and counts the skip reasons, and lists a dependency of two files once', () => {
    const section = renderSkippedDependencies(new Map([
      [
        'main',
        {
          'bun': [
            {
              packageFile: 'frontend/package.json',
              deps: [
                { depName: 'a', skipReason: 'disabled' },
                { depName: 'b', skipReason: 'disabled' },
              ],
            },
            {
              packageFile: '.github/renovate/package.json',
              deps: [
                { depName: 'a', skipReason: 'disabled' },
              ],
            },
          ],
          'custom.regex': [
            {
              packageFile: 'versions.env',
              deps: [
                { depName: 'c', skipReason: 'contains-variable' },
                { depName: 'd', skipReason: 'unsupported-version' },
              ],
            },
          ],
        },
      ],
      ['next', null],
    ]), options)
    expect(blocks(section)).toEqual({
      '<code>unsupported-version</code>': {
        count: 1,
        items: ['- `d` (`versions.env`)'],
      },
      'Disabled by configuration': {
        count: 2,
        items: ['- `a` (`.github/renovate/package.json`, `frontend/package.json`)', '- `b` (`frontend/package.json`)'],
      },
      'Version comes from a variable': {
        count: 1,
        items: ['- `c` (`versions.env`)'],
      },
    })
  })
  it('reports none for empty data', () => {
    expect(renderSkippedDependencies(new Map(), options)).toBe('## Skipped Dependencies\n\nNone.\n')
    expect(renderSkippedDependencies(data({
      bun: [
        {
          packageFile: 'frontend/package.json',
          deps: [
            {
              depName: 'a',
              updates: [
                { newVersion: '1.0.0' },
              ],
            },
          ],
        },
      ],
    }), options)).toBe('## Skipped Dependencies\n\nNone.\n')
  })
  it('shortens every list until the section fits the limit', () => {
    const deps = Array.from({ length: 400 }, (_, index) => ({ depName: `package-${index}`, skipReason: 'disabled' }))
    const full = renderSkippedDependencies(data({
      bun: [
        { packageFile: 'frontend/package.json', deps },
      ],
    }), options)
    const short = renderSkippedDependencies(data({
      bun: [
        { packageFile: 'frontend/package.json', deps },
      ],
    }), { ...options, maxLength: 2000 })
    expect(full.length).toBeGreaterThan(2000)
    expect(short.length).toBeLessThanOrEqual(2000)
    const block = blocks(short)['Disabled by configuration']
    expect(block.count).toBe(400)
    expect(block.items.at(-1)).toMatch(/^- …and \d+ more$/)
    expect(Number(/\d+/.exec(block.items.at(-1))[0]) + block.items.length - 1).toBe(400)
  })
  it('never spells a dashboard marker, even for an identifier that contains one', () => {
    const section = renderSkippedDependencies(data({
      bun: [
        {
          packageFile: 'frontend/package.json',
          deps: [
            { depName: 'x-branch=renovate/x', skipReason: 'disabled' },
            { depName: 'config-migration-pr-info', skipReason: 'disabled' },
          ],
        },
      ],
    }), options)
    expect(section).not.toContain('-branch=')
    expect(section).not.toContain('config-migration-pr-info')
  })
})

describe('code', () => {
  it('keeps a backtick inside a code span and joins lines', () => {
    expect(code('plain')).toBe('`plain`')
    expect(code('a`b')).toBe('``a`b``')
    expect(code('`edge')).toBe('`` `edge ``')
    expect(code('two\nlines')).toBe('`two lines`')
  })
})

describe('configureSkippedDependencies', () => {
  const policy = JSON.parse(readFileSync(resolve(root, 'renovate.json'), 'utf8'))
  it('registers the footer helper, and restores the earlier helper when it is disposed', async () => {
    const handlebars = renovateHandlebars()
    const earlier = () => 'earlier'
    handlebars.registerHelper('leapmuxSkippedDependencies', earlier)
    try {
      const configured = await configureSkippedDependencies({ policy, alerts: [] })
      expect(configured.dependencyDashboardFooter).toBe('{{{leapmuxSkippedDependencies}}}')
      expect(handlebars.helpers.leapmuxSkippedDependencies).not.toBe(earlier)
      configured.dispose()
      expect(handlebars.helpers.leapmuxSkippedDependencies).toBe(earlier)
    }
    finally {
      handlebars.unregisterHelper('leapmuxSkippedDependencies')
    }
    const fresh = await configureSkippedDependencies({ policy, alerts: [] })
    expect(handlebars.helpers.leapmuxSkippedDependencies).toBeFunction()
    fresh.dispose()
    expect(handlebars.helpers.leapmuxSkippedDependencies).toBeUndefined()
  })
  it('fails before it registers a helper when the policy has no positive release age', async () => {
    await expect(configureSkippedDependencies({
      policy: { minimumReleaseAge: '0 days' },
      alerts: [],
    })).rejects.toThrow('positive duration')
    expect(renovateHandlebars().helpers.leapmuxSkippedDependencies).toBeUndefined()
  })
  it('fails before it registers a helper when the policy sets an unknown time zone', async () => {
    await expect(configureSkippedDependencies({
      policy: { ...policy, timezone: 'Mars/Olympus_Mons' },
      alerts: [],
    })).rejects.toThrow(RangeError)
    expect(renovateHandlebars().helpers.leapmuxSkippedDependencies).toBeUndefined()
  })
})

describe('the Skipped Dependencies helper in Renovate under Node', () => {
  it('renders the dashboard footer, and refuses an upgrade context and missing package files', () => {
    const policy = JSON.parse(readFileSync(resolve(root, 'renovate.json'), 'utf8'))
    const packageFiles = {
      cargo: [
        {
          packageFile: 'desktop/rust/Cargo.toml',
          deps: [
            { depName: 'serde', skipReason: 'disabled' },
          ],
        },
      ],
    }
    const source = `
      const root=process.argv[1];
      const {pathToFileURL}=await import('node:url'); const {resolve}=await import('node:path');
      const engine=p=>import(pathToFileURL(resolve(root,'.github/renovate/node_modules/renovate/dist',p)).href);
      await (await engine('logger/index.js')).init();
      const {configureSkippedDependencies}=await import(pathToFileURL(resolve(root,'scripts/renovate-dashboard.mjs')).href);
      const {PackageFiles}=await engine('workers/repository/package-files.js');
      const {compile}=await engine('util/template/index.js');
      const {getConfig}=await engine('config/defaults.js');
      const {mergeChildConfig}=await engine('config/utils.js');
      const [policy,packageFiles,alerts]=process.argv.slice(2).map(value=>JSON.parse(value));
      const {dependencyDashboardFooter:footer}=await configureSkippedDependencies({policy,alerts});
      const config=mergeChildConfig(getConfig(),policy);
      const attempt=input=>{ try { return compile(footer,input) } catch (error) { return 'error: '+error.message } };
      const empty=attempt(config);
      PackageFiles.add('main',packageFiles);
      let nested;
      try { nested=compile('{{#with nested}}{{{leapmuxSkippedDependencies}}}{{/with}}',{...config,nested:{kind:'nested'}},false) } catch (error) { nested='error: '+error.message }
      console.log(JSON.stringify({runtime:process.versions.bun??'node',footer,empty,section:attempt(config),upgrade:attempt({...config,manager:'cargo',upgrades:[{depName:'serde'}]}),nested}));
    `
    const result = spawnSync('node', [
      '--input-type=module',
      '-e',
      source,
      root,
      JSON.stringify(policy),
      JSON.stringify(packageFiles),
      JSON.stringify([
        alert('glib', 'rust', 'desktop/rust/Cargo.lock'),
      ]),
    ], {
      encoding: 'utf8',
      env: { ...process.env, LOG_LEVEL: 'fatal' },
    })
    expect(result.status, result.stderr).toBe(0)
    const rendered = JSON.parse(result.stdout)
    expect(rendered.runtime).toBe('node')
    expect(rendered.footer).toBe('{{{leapmuxSkippedDependencies}}}')
    expect(rendered.empty).toBe('error: Skipped dependencies require the package files that Renovate extracted.')
    expect(rendered.section).toStartWith('## Skipped Dependencies\n\n<details><summary>Security alerts without a proposed fix (1)</summary>')
    expect(rendered.section).toContain('- `serde` (`desktop/rust/Cargo.toml`)')
    expect(rendered.upgrade).toBe('error: Skipped dependencies can only be rendered in the Dependency Dashboard footer.')
    // A nested context lacks every upgrade field, so only the root check refuses it.
    expect(rendered.nested).toBe('error: Skipped dependencies can only be rendered in the Dependency Dashboard footer.')
  })
})
