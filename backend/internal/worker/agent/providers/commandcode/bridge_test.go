package commandcode

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeCompactionBridgeChecksAuthenticationAndConcurrentCalls(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("The native mod test requires Node.js.")
	}
	directory := t.TempDir()
	module := filepath.Join(directory, "bridge.mjs")
	require.NoError(t, os.WriteFile(module, bridgeSource, 0o600))
	moduleJSON, err := json.Marshal(module)
	require.NoError(t, err)
	script := `
import assert from 'node:assert/strict';
import {pathToFileURL} from 'node:url';
const handlers = new Map();
let calls = 0;
let release;
let entered;
let fail = false;
const started = new Promise(resolve => { entered = resolve });
const held = new Promise(resolve => { release = resolve });
const cmd = {on(event, handler) {handlers.set(event, handler)}, sessions: {async compact() {calls++; if(fail) throw new Error('Native compaction failure.'); entered(); await held;}}};
const savedWrite = process.stdout.write.bind(process.stdout);
let ready;
process.stdout.write = text => {ready = JSON.parse(text); return true};
const mod = await import(pathToFileURL(` + string(moduleJSON) + `).href);
process.env.LEAPMUX_COMMANDCODE_BRIDGE_SECRET = 'a'.repeat(64);
await mod.default(cmd);
process.stdout.write = savedWrite;
const endpoint = 'http://127.0.0.1:' + ready.port + '/compact';
const headers = {authorization:'Bearer ' + 'a'.repeat(64)};
const rejected = await fetch(endpoint, {method:'POST'});
assert.equal(rejected.status,401);
assert.equal(calls,0);
const idle = await fetch(endpoint, {method:'POST', headers});
assert.equal(idle.status,409);
assert.equal(calls,0);
handlers.get('session_start')();
const first = fetch(endpoint, {method:'POST', headers});
await started;
const concurrent = await fetch(endpoint, {method:'POST', headers});
assert.equal(concurrent.status,409);
assert.equal(calls,1);
release();
const complete = await first;
assert.equal(complete.status,200);
assert.deepEqual(await complete.json(),{completed:true});
fail = true;
const failure = await fetch(endpoint, {method:'POST', headers});
assert.equal(failure.status,500);
assert.equal((await failure.json()).error,'Native compaction failure.');
assert.equal(calls,2);
const unknown = await fetch(endpoint+'?unknown', {method:'POST',headers});
assert.equal(unknown.status,404);
assert.equal(calls,2);
handlers.get('session_shutdown')();
console.log('Native compaction bridge checks passed.');
`
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "--input-type=module", "-e", script)
	command.Env = []string{"PATH=" + filepath.Dir(node), "HOME=" + directory, "TMPDIR=" + directory, "TEMP=" + directory, "TMP=" + directory}
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	assert.Contains(t, string(output), "Native compaction bridge checks passed.")
}

func TestRuntimeDirectoryContainsAPrivateNativeModAndCredential(t *testing.T) {
	directory, secret, err := createRuntimeDirectory()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(directory)) })
	assert.Len(t, secret, 64)
	info, err := os.Stat(filepath.Join(directory, "bridge.mjs"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	data, err := os.ReadFile(filepath.Join(directory, "bridge.mjs"))
	require.NoError(t, err)
	assert.Equal(t, bridgeSource, data)
}

func TestNativeBridgeReportsTheEffectiveLocalOnlyMode(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("The native mod test requires Node.js.")
	}
	for _, testCase := range []struct {
		name, environment, configName, config string
		localOnly                             bool
	}{
		{name: "native environment enables local only", environment: "CMD_LOCAL_ONLY=1", localOnly: true},
		{name: "native environment accepts true", environment: "CMD_LOCAL_ONLY=true", localOnly: true},
		{name: "native environment keeps case exact", environment: "CMD_LOCAL_ONLY=TRUE"},
		{name: "native production config enables local only", configName: "config.json", config: `{"localOnly":true}`, localOnly: true},
		{name: "native staging config enables local only", environment: "COMMANDCODE_API_ENV=staging", configName: "config.staging.json", config: `{"localOnly":true}`, localOnly: true},
		{name: "native local config enables local only", environment: "COMMANDCODE_API_ENV=local", configName: "config.local.json", config: `{"localOnly":true}`, localOnly: true},
		{name: "invalid native config keeps gateway mode", configName: "config.json", config: `{`},
		{name: "no native config keeps gateway mode"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			directory := t.TempDir()
			module := filepath.Join(directory, "bridge.mjs")
			require.NoError(t, os.WriteFile(module, bridgeSource, 0o600))
			if testCase.configName != "" {
				require.NoError(t, os.MkdirAll(filepath.Join(directory, ".commandcode"), 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(directory, ".commandcode", testCase.configName), []byte(testCase.config), 0o600))
			}
			moduleJSON, err := json.Marshal(module)
			require.NoError(t, err)
			expected, err := json.Marshal(testCase.localOnly)
			require.NoError(t, err)
			script := `import assert from 'node:assert/strict'; import {pathToFileURL} from 'node:url';
const callbacks = new Map(); let ready;
const write = process.stdout.write.bind(process.stdout);
process.stdout.write = text => {ready=JSON.parse(text);return true};
const mod = await import(pathToFileURL(` + string(moduleJSON) + `).href);
await mod.default({on(event,handler){callbacks.set(event,handler)},sessions:{compact(){throw new Error('Compaction must not run.')}}});
assert.equal(ready.localOnly,` + string(expected) + `);
callbacks.get('session_shutdown')(); process.stdout.write=write;
console.log('Native local-only mode matched.');`
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, node, "--input-type=module", "-e", script)
			command.Env = []string{"PATH=" + filepath.Dir(node), "HOME=" + directory, "TMPDIR=" + directory, "TEMP=" + directory, "TMP=" + directory, bridgeSecretEnv + "=" + strings.Repeat("a", 64)}
			if systemRoot := os.Getenv("SYSTEMROOT"); systemRoot != "" {
				command.Env = append(command.Env, "SYSTEMROOT="+systemRoot)
			}
			if testCase.environment != "" {
				command.Env = append(command.Env, testCase.environment)
			}
			output, err := command.CombinedOutput()
			require.NoError(t, err, string(output))
			assert.Contains(t, string(output), "Native local-only mode matched.")
		})
	}
}
