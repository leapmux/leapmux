package bootstrap

import (
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// mustMarshalFilePayload is the blob a raw UpsertWorkerTabPayload writes.
// These tests plant rows directly so they can exercise BuildTabSync without a
// whole Service, and a row the readers cannot decode would not be a valid
// fixture.
func mustMarshalFilePayload(filePath, workingDir string) []byte {
	blob, err := proto.Marshal(&leapmuxv1.TabPayload{
		WorkingDir: workingDir,
		Kind:       &leapmuxv1.TabPayload_File{File: &leapmuxv1.FileTabPayload{FilePath: filePath}},
	})
	if err != nil {
		panic(err)
	}
	return blob
}
