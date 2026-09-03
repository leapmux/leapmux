package service

import (
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// fileTabPayload builds the FILE arm of a TabPayload, which is what almost
// every test in this package registers. Kept here rather than repeated at each
// call site so a change to the payload shape is one edit.
func fileTabPayload(filePath, workingDir string) *leapmuxv1.TabPayload {
	return &leapmuxv1.TabPayload{
		WorkingDir: workingDir,
		Kind:       &leapmuxv1.TabPayload_File{File: &leapmuxv1.FileTabPayload{FilePath: filePath}},
	}
}

// mustMarshalFilePayload is the blob a raw UpsertWorkerTabPayload writes.
// Tests that go around the store (to plant a row the store would refuse, or to
// avoid its worktree probing) still have to write a payload the readers can
// decode.
func mustMarshalFilePayload(filePath, workingDir string) []byte {
	blob, err := proto.Marshal(fileTabPayload(filePath, workingDir))
	if err != nil {
		panic(err)
	}
	return blob
}
