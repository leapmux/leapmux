package kiro

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/url"
	"os"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

func TestKiroPromptParamsCarryImageBytesWithoutURI(t *testing.T) {
	t.Parallel()
	imageBytes := []byte{0x89, 'P', 'N', 'G', 0x00, 0xFE}
	blocks := acp.BuildPromptBlocks("Inspect the attached files.", []agent.ClassifiedAttachment{
		{Filename: "shot.png", MIMEType: "image/png", Kind: contracts.AttachmentMediaKindImage, Data: imageBytes},
		{Filename: "notes.txt", MIMEType: "text/plain", Kind: contracts.AttachmentMediaKindText, Data: []byte("unique-note-42")},
		{Filename: "document.pdf", MIMEType: "application/pdf", Kind: contracts.AttachmentMediaKindPdf, Data: []byte("%PDF-1.1")},
	})
	params := map[string]any{"prompt": blocks}
	hooks := (&Agent{}).configure(agent.Options{})
	require.NotNil(t, hooks.PromptParams)
	hooks.PromptParams(params)

	got, ok := params["prompt"].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, got, 4)
	assert.Equal(t, map[string]interface{}{"type": "text", "text": "Inspect the attached files."}, got[0])
	assert.Equal(t, "image", got[1]["type"])
	assert.Equal(t, "image/png", got[1]["mimeType"])
	assert.Equal(t, base64.StdEncoding.EncodeToString(imageBytes), got[1]["data"])
	assert.NotContains(t, got[1], "uri", "Kiro v3 drops an image block that also states a URI")
	assert.Equal(t, map[string]interface{}{
		"type": "resource",
		"resource": map[string]interface{}{
			"uri": "notes.txt", "mimeType": "text/plain", "text": "unique-note-42",
		},
	}, got[2])
	assert.Equal(t, "resource", got[3]["type"])
	resource, ok := got[3]["resource"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "application/pdf", resource["mimeType"])
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("%PDF-1.1")), resource["blob"])
	assert.NotEqual(t, "document.pdf", resource["uri"], "a PDF also needs a payload identity to avoid native history deduplication")
}

func TestKiroPromptParamsAcceptAbsentPromptAndImageURI(t *testing.T) {
	t.Parallel()
	hooks := (&Agent{}).configure(agent.Options{})
	require.NotNil(t, hooks.PromptParams)
	for _, params := range []map[string]any{
		{},
		{"prompt": "not blocks"},
		{"prompt": []map[string]interface{}{{"type": "image", "mimeType": "image/png", "data": "aW1hZ2U="}}},
	} {
		assert.NotPanics(t, func() { hooks.PromptParams(params) })
	}
}

func TestKiroPromptParamsPreserveMalformedAndUnrelatedResources(t *testing.T) {
	t.Parallel()
	for _, resource := range []any{
		nil,
		"not a resource",
		map[string]any{},
		map[string]any{"mimeType": 42, "blob": "UEsDBA==", "uri": "f.docx"},
		map[string]any{"mimeType": "application/pdf", "blob": 42, "uri": "f.pdf"},
		map[string]any{"mimeType": "application/pdf", "blob": "JVBERg==", "uri": 42},
		map[string]any{"mimeType": "application/pdf", "text": "not a blob", "uri": "f.pdf"},
		map[string]any{"mimeType": "application/zip", "blob": "UEsDBA==", "uri": "f.zip"},
		map[string]any{"mimeType": "text/plain", "text": "notes", "uri": "f.txt"},
	} {
		block := map[string]any{"type": "resource", "resource": resource}
		before, err := json.Marshal(block)
		require.NoError(t, err)
		params := map[string]any{"prompt": []map[string]interface{}{nil, block}}
		assert.NotPanics(t, func() { kiroPromptParams(params) })
		after, err := json.Marshal(block)
		require.NoError(t, err)
		assert.JSONEq(t, string(before), string(after))
	}
}

type officeDocumentFixture struct {
	Filename       string `json:"filename"`
	MIMEType       string `json:"mimeType"`
	Format         string `json:"format"`
	OriginalBase64 string `json:"originalBase64"`
	UpdatedBase64  string `json:"updatedBase64"`
}

func loadOfficeDocuments(t *testing.T) map[string]officeDocumentFixture {
	t.Helper()
	data, err := os.ReadFile(testutil.RepoPath(t, "testdata", "office-documents.json"))
	require.NoError(t, err)
	var fixtures struct {
		Documents map[string]officeDocumentFixture `json:"documents"`
	}
	require.NoError(t, json.Unmarshal(data, &fixtures))
	require.Len(t, fixtures.Documents, 2)
	return fixtures.Documents
}

func TestKiroOfficeFixturesAreCompleteOOXMLPackages(t *testing.T) {
	t.Parallel()
	for kind, document := range loadOfficeDocuments(t) {
		for version, encoded := range map[string]string{"original": document.OriginalBase64, "updated": document.UpdatedBase64} {
			t.Run(kind+"/"+version, func(t *testing.T) {
				t.Parallel()
				data, err := base64.StdEncoding.DecodeString(encoded)
				require.NoError(t, err)
				archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
				require.NoError(t, err)
				parts := make(map[string]string)
				for _, file := range archive.File {
					reader, err := file.Open()
					require.NoError(t, err)
					contents, readErr := io.ReadAll(reader)
					closeErr := reader.Close()
					require.NoError(t, readErr, "the ZIP reader checks each part's CRC")
					require.NoError(t, closeErr)
					decoder := xml.NewDecoder(bytes.NewReader(contents))
					for {
						_, err := decoder.Token()
						if err == io.EOF {
							break
						}
						require.NoError(t, err)
					}
					parts[file.Name] = string(contents)
				}
				require.Contains(t, parts, "[Content_Types].xml")
				require.Contains(t, parts, "_rels/.rels")
				part, marker := "word/document.xml", "KIRO_NATIVE_WORD"
				if kind == "spreadsheet" {
					part, marker = "xl/worksheets/sheet1.xml", "KIRO_NATIVE_SHEET"
					require.Contains(t, parts, "xl/workbook.xml")
					require.Contains(t, parts, "xl/_rels/workbook.xml.rels")
				}
				if version == "updated" {
					marker += "_UPDATED"
				}
				assert.Contains(t, parts[part], marker+"_MARKER")
			})
		}
	}
}

func TestKiroOfficeAttachmentPolicy(t *testing.T) {
	t.Parallel()
	for _, document := range loadOfficeDocuments(t) {
		data, err := base64.StdEncoding.DecodeString(document.OriginalBase64)
		require.NoError(t, err)
		attachment := agent.ClassifiedAttachment{Filename: document.Filename, MIMEType: document.MIMEType, Kind: contracts.AttachmentMediaKindBinary, Data: data}
		assert.NoError(t, (kiroProvider{}).ValidateAttachment(attachment))
		for _, malformed := range [][]byte{nil, {'P', 'K'}, []byte("not an Office header")} {
			attachment.Data = malformed
			assert.Error(t, (kiroProvider{}).ValidateAttachment(attachment))
		}
	}
	for _, mime := range []string{"application/msword", "application/vnd.ms-excel"} {
		attachment := agent.ClassifiedAttachment{Filename: "legacy", MIMEType: mime, Kind: contracts.AttachmentMediaKindBinary, Data: []byte{0xd0, 0xcf, 0x11, 0xe0, 0}}
		assert.NoError(t, (kiroProvider{}).ValidateAttachment(attachment), "the native legacy header is supported")
		attachment.Data = []byte{'P', 'K', 3, 4}
		assert.Error(t, (kiroProvider{}).ValidateAttachment(attachment))
	}
	for _, mime := range []string{"application/zip", "application/octet-stream", "application/vnd.ms-powerpoint"} {
		assert.Error(t, (kiroProvider{}).ValidateAttachment(agent.ClassifiedAttachment{Filename: "refused", MIMEType: mime, Kind: contracts.AttachmentMediaKindBinary, Data: []byte{'P', 'K', 3, 4}}))
	}
}

func TestKiroPromptParamsGiveDocumentPayloadsStableIdentity(t *testing.T) {
	t.Parallel()
	const mime = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	prepare := func(mime, blob string) (map[string]any, map[string]any) {
		resource := map[string]any{"uri": "report #%?.docx", "mimeType": mime, "blob": blob}
		params := map[string]any{"prompt": []map[string]interface{}{{"type": "resource", "resource": resource}}}
		kiroPromptParams(params)
		return params, resource
	}
	params, first := prepare(mime, "UEsDBA==")
	_, same := prepare(mime, "UEsDBA==")
	_, changed := prepare(mime, "UEsDBAA=")
	_, otherType := prepare("application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "UEsDBA==")
	uri, ok := first["uri"].(string)
	require.True(t, ok)
	parsed, err := url.Parse(uri)
	require.NoError(t, err)
	assert.Regexp(t, `^[0-9a-f]{64}-`, parsed.Path)
	assert.Contains(t, parsed.Path, "report #%?.docx")
	assert.Empty(t, parsed.RawQuery)
	assert.Empty(t, parsed.Fragment)
	assert.Equal(t, same["uri"], uri)
	assert.NotEqual(t, changed["uri"], uri, "updated bytes must not inherit the previous native name")
	assert.NotEqual(t, otherType["uri"], uri, "a different native format also needs its own identity")
	kiroPromptParams(params)
	assert.Equal(t, uri, first["uri"], "preparing the same packet twice is stable")
	assert.Equal(t, "UEsDBA==", first["blob"])
}
