package service_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// filePayload builds the FILE branch of a TabPayload for the reconciler tests,
// which care about which rows survive rather than what is in them.
func filePayload(filePath string) *leapmuxv1.TabPayload {
	return &leapmuxv1.TabPayload{
		Kind: &leapmuxv1.TabPayload_File{File: &leapmuxv1.FileTabPayload{FilePath: filePath}},
	}
}

// filePathOf reads the path back out of a stored row, for the tests that assert
// one owner's row survived and the other's did not.
func filePathOf(t *testing.T, row db.WorkerTabPayload) string {
	t.Helper()
	payload := &leapmuxv1.TabPayload{}
	require.NoError(t, proto.Unmarshal(row.Payload, payload))
	return payload.GetFile().GetFilePath()
}

// filePayload with an explicit working dir, for the store tests that exercise
// the working-dir normalizer.
func filePayloadIn(filePath, workingDir string) *leapmuxv1.TabPayload {
	return &leapmuxv1.TabPayload{
		WorkingDir: workingDir,
		Kind:       &leapmuxv1.TabPayload_File{File: &leapmuxv1.FileTabPayload{FilePath: filePath}},
	}
}

// mustMarshalFilePayload is the blob a raw UpsertWorkerTabPayload writes, for
// the tests that plant a row the store itself would refuse.
func mustMarshalFilePayload(filePath, workingDir string) []byte {
	blob, err := proto.Marshal(filePayloadIn(filePath, workingDir))
	if err != nil {
		panic(err)
	}
	return blob
}
