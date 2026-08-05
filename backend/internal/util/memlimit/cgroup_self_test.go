package memlimit

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSelfCgroupPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		wantV2  string
		wantV1  string
	}{
		{
			name:    "pure v2",
			content: "0::/system.slice/leapmux.service\n",
			wantV2:  "/system.slice/leapmux.service",
			wantV1:  "/",
		},
		{
			name: "hybrid picks the memory controller out of v1's line",
			content: "12:memory:/docker/abc123\n" +
				"11:cpu,cpuacct:/docker/abc123\n" +
				"0::/\n",
			wantV2: "/",
			wantV1: "/docker/abc123",
		},
		{
			name:    "a controller list merely containing the substring is not the memory one",
			content: "5:memory+swap:/nope\n",
			wantV2:  "/",
			wantV1:  "/",
		},
		{
			name:    "unreadable or empty falls back to the root, which is right inside a namespace",
			content: "",
			wantV2:  "/",
			wantV1:  "/",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v2, v1 := selfCgroupPaths(tt.content)
			assert.Equal(t, tt.wantV2, v2)
			assert.Equal(t, tt.wantV1, v1)
		})
	}
}

func TestAncestorLimitFilesWalksUpToTheRoot(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{
		"/sys/fs/cgroup/system.slice/leapmux.service/memory.max",
		"/sys/fs/cgroup/system.slice/memory.max",
		"/sys/fs/cgroup/memory.max",
	}, ancestorLimitFiles("/sys/fs/cgroup", "/system.slice/leapmux.service", "memory.max"))

	// The root itself must still yield exactly one candidate, not loop.
	assert.Equal(t, []string{"/sys/fs/cgroup/memory.max"},
		ancestorLimitFiles("/sys/fs/cgroup", "/", "memory.max"))
}
