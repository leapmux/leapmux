package amp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/util/imageheader"
)

// Amp's limits on one stdin line. A line past any of them ENDS the session with
// an error rather than refusing the one message, so the worker checks each one
// before it writes.
const (
	// maxLineContentBytes caps the content of one line, measured as Amp
	// measures it: the UTF-8 bytes of its own content array as JSON. The
	// base64 of an image counts, so one image of about 760 KB fills it.
	maxLineContentBytes = 1 << 20
	// maxImagesPerLine caps the images of one line.
	maxImagesPerLine = 4
	// maxImageBytes caps one image's decoded size.
	maxImageBytes = 5138022
	// maxImageDimension caps each side of an image, in pixels.
	maxImageDimension = 8000
)

// longestImageSourcePath is the longest `sourcePath` that Amp gives an image of
// a stdin line (`stream-json://stdin/line-<n>/image-<m>`). Amp's size check
// counts it, and the line number grows with the process, so the estimate
// takes the longest one a line number can make.
const longestImageSourcePath = "stream-json://stdin/line-18446744073709551615/image-4"

// userLine is one line of Amp's stdin.
type userLine struct {
	Type    string      `json:"type"`
	Steer   bool        `json:"steer,omitempty"`
	Message userMessage `json:"message"`
}

type userMessage struct {
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

type textBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type imageBlock struct {
	Type   string      `json:"type"`
	Source imageSource `json:"source"`
}

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// ampContentEstimate is the internal shape Amp converts a block into before it
// measures the line. The field names differ from the stdin shape, so the
// estimate uses Amp's own.
type ampContentEstimate struct {
	Type       string             `json:"type"`
	Text       string             `json:"text,omitempty"`
	SourcePath string             `json:"sourcePath,omitempty"`
	Source     *ampSourceEstimate `json:"source,omitempty"`
}

type ampSourceEstimate struct {
	Type      string `json:"type"`
	MediaType string `json:"mediaType"`
	Data      string `json:"data"`
}

// buildUserLine encodes one message as a stdin line. It refuses a message that
// Amp would refuse, with a reason the user can act on.
//
// The text travels as one block and each text attachment as a block of its
// own, delimited with its file name. Each image travels as a base64 block of
// its detected type.
func buildUserLine(content string, attachments []*leapmuxv1.Attachment, steer bool) ([]byte, error) {
	blocks := make([]any, 0, len(attachments)+1)
	estimate := make([]ampContentEstimate, 0, len(attachments)+1)
	addText := func(text string) {
		blocks = append(blocks, textBlock{Type: contracts.AmpBlockTypeText, Text: text})
		estimate = append(estimate, ampContentEstimate{Type: contracts.AmpBlockTypeText, Text: text})
	}
	if content != "" || len(attachments) == 0 {
		addText(content)
	}
	images := 0
	for _, attachment := range agent.ClassifyAttachments(attachments) {
		switch attachment.Kind {
		case agent.AttachmentKindText:
			addText(providerkit.BuildInlineTextAttachmentBlock(attachment))
		case agent.AttachmentKindImage:
			mediaType, err := checkImage(attachment)
			if err != nil {
				return nil, err
			}
			images++
			if images > maxImagesPerLine {
				return nil, fmt.Errorf("the message holds %d images, and Amp takes at most %d in one message", countImages(attachments), maxImagesPerLine)
			}
			data := base64.StdEncoding.EncodeToString(attachment.Data)
			blocks = append(blocks, imageBlock{Type: blockTypeImage, Source: imageSource{Type: imageSourceBase64, MediaType: mediaType, Data: data}})
			estimate = append(estimate, ampContentEstimate{
				Type:       blockTypeImage,
				SourcePath: longestImageSourcePath,
				Source:     &ampSourceEstimate{Type: imageSourceBase64, MediaType: mediaType, Data: data},
			})
		default:
			// ValidateAttachment refused these before the queue accepted them.
			return nil, providerkit.RejectPDFAndBinaryAttachment("Amp", attachment)
		}
	}
	size, err := jsonSize(estimate)
	if err != nil {
		return nil, err
	}
	if size > maxLineContentBytes {
		return nil, fmt.Errorf("the message is too large for Amp: it holds %d bytes as JSON, and Amp takes at most %d. Remove an image or shorten the text", size, maxLineContentBytes)
	}
	line, err := json.Marshal(userLine{
		Type:    contracts.AmpLineTypeUser,
		Steer:   steer,
		Message: userMessage{Role: roleUser, Content: blocks},
	})
	if err != nil {
		return nil, fmt.Errorf("encode the message for Amp: %w", err)
	}
	return line, nil
}

// countImages counts the image attachments of one message.
func countImages(attachments []*leapmuxv1.Attachment) int {
	count := 0
	for _, attachment := range agent.ClassifyAttachments(attachments) {
		if attachment.Kind == agent.AttachmentKindImage {
			count++
		}
	}
	return count
}

// jsonSize measures a value as Amp measures a line, in UTF-8 bytes of its JSON.
// Go writes every non-ASCII character but U+2028 and U+2029 as itself, as
// JavaScript does, and escapes those two, which only overstates the size.
func jsonSize(value any) (int, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return 0, fmt.Errorf("measure the message for Amp: %w", err)
	}
	return len(bytes.TrimSuffix(buf.Bytes(), []byte("\n"))), nil
}

// validateAttachment is the policy that ValidateAttachment states: text and
// images, and no PDF or other binary file, because Amp's stdin line carries
// text and image blocks alone. An image must be one Amp can read and one that
// fits in a line by itself.
func validateAttachment(attachment agent.ClassifiedAttachment) error {
	switch attachment.Kind {
	case agent.AttachmentKindText:
		return nil
	case agent.AttachmentKindImage:
		_, err := checkImage(attachment)
		return err
	default:
		return providerkit.RejectPDFAndBinaryAttachment("Amp", attachment)
	}
}

// checkImage returns the media type of an image that Amp can take. Amp
// compares the declared type against the bytes and refuses a mismatch, so the
// type comes from the bytes and not from the file name.
func checkImage(attachment agent.ClassifiedAttachment) (string, error) {
	name := attachment.Filename
	if name == "" {
		name = "(unnamed)"
	}
	if len(attachment.Data) == 0 {
		return "", fmt.Errorf("the image %s is empty, so Amp cannot take it", name)
	}
	mediaType := imageheader.MIMEType(attachment.Data)
	if mediaType == "" {
		return "", fmt.Errorf("the image %s is not a PNG, JPEG, GIF or WebP image, so Amp cannot take it", name)
	}
	if len(attachment.Data) > maxImageBytes {
		return "", fmt.Errorf("the image %s is %d bytes, and Amp takes at most %d", name, len(attachment.Data), maxImageBytes)
	}
	if width, height, ok := imageheader.Dimensions(mediaType, attachment.Data); ok && (width > maxImageDimension || height > maxImageDimension) {
		return "", fmt.Errorf("the image %s is %dx%d pixels, and Amp takes at most %d on each side", name, width, height, maxImageDimension)
	}
	alone, err := jsonSize([]ampContentEstimate{{
		Type:       blockTypeImage,
		SourcePath: longestImageSourcePath,
		Source:     &ampSourceEstimate{Type: imageSourceBase64, MediaType: mediaType, Data: base64.StdEncoding.EncodeToString(attachment.Data)},
	}})
	if err != nil {
		return "", err
	}
	if alone > maxLineContentBytes {
		return "", fmt.Errorf("the image %s takes %d bytes once encoded, and one Amp message holds at most %d. Attach a smaller image", name, alone, maxLineContentBytes)
	}
	return mediaType, nil
}
