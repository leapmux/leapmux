package cline

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Attachments.
//
// session.send_input takes two lists beside the prompt: `userImages`, each an
// image as a data URL, and `userFiles`, each the PATH of a text file that the
// daemon reads into the message. Cline reads a text file of at most
// 20 * 1000 * 1024 bytes and refuses a file that holds a NUL byte, and it has no
// input for a PDF or another binary file. So the worker writes each text
// attachment into the agent's private directory, under its own name, and states
// the path; the model sees the file under that path, as Cline states each file
// it attaches.

// maxTextAttachmentBytes is the largest text file Cline reads into a message
// (MAX_USER_FILE_BYTES in Cline's user-files.ts).
const maxTextAttachmentBytes = 20 * 1000 * 1024

// validateAttachment accepts text and the four image formats that the model
// providers take, and refuses the rest with the reason.
func validateAttachment(attachment agent.ClassifiedAttachment) error {
	switch attachment.Kind {
	case agent.AttachmentKindText:
		if len(attachment.Data) > maxTextAttachmentBytes {
			return fmt.Errorf("the text file %s is larger than the %d bytes that Cline reads", attachment.Filename, maxTextAttachmentBytes)
		}
		return nil
	case agent.AttachmentKindImage:
		return nil
	case agent.AttachmentKindPDF:
		return fmt.Errorf("the attachment %s is a PDF, which Cline does not read", attachment.Filename)
	default:
		return fmt.Errorf("the attachment %s is a binary file, which Cline does not read", attachment.Filename)
	}
}

// clineInput is one message as session.send_input takes it.
type clineInput struct {
	prompt     string
	userImages []string
	userFiles  []string
}

// payload is the session.send_input payload of the input. delivery is empty for
// a message that starts a turn, and `steer` for one that joins it. mode states
// the session's mode, which Cline writes around the prompt.
func (in clineInput) payload(sessionID, mode, delivery string) map[string]any {
	payload := map[string]any{
		"sessionId": sessionID,
		"prompt":    in.prompt,
		"mode":      mode,
	}
	if len(in.userImages) > 0 || len(in.userFiles) > 0 {
		attachments := map[string]any{}
		if len(in.userImages) > 0 {
			attachments["userImages"] = in.userImages
		}
		if len(in.userFiles) > 0 {
			attachments["userFiles"] = in.userFiles
		}
		payload["attachments"] = attachments
	}
	if delivery != "" {
		payload["delivery"] = delivery
	}
	return payload
}

// buildInput turns one LeapMux message into Cline's input. It refuses a message
// with neither text nor an attachment, as the daemon would refuse it.
func (a *Agent) buildInput(content string, attachments []*leapmuxv1.Attachment) (clineInput, error) {
	in := clineInput{prompt: content}
	classified := agent.ClassifyAttachments(attachments)
	for _, attachment := range classified {
		if err := validateAttachment(attachment); err != nil {
			return clineInput{}, err
		}
	}
	var texts []agent.ClassifiedAttachment
	for _, attachment := range classified {
		switch attachment.Kind {
		case agent.AttachmentKindImage:
			in.userImages = append(in.userImages, providerkit.EncodeDataURI(attachment.MIMEType, attachment.Data))
		case agent.AttachmentKindText:
			texts = append(texts, attachment)
		case agent.AttachmentKindPDF, agent.AttachmentKindBinary:
			// validateAttachment refused both kinds above.
		}
	}
	if len(texts) > 0 {
		paths, err := a.writeTextAttachments(texts)
		if err != nil {
			return clineInput{}, err
		}
		in.userFiles = paths
	}
	if strings.TrimSpace(in.prompt) == "" && len(in.userImages) == 0 && len(in.userFiles) == 0 {
		return clineInput{}, fmt.Errorf("the message states no text and no attachment")
	}
	return in, nil
}

// writeTextAttachments writes each text attachment of one message into a
// directory of its own, under the file's own base name, and returns the paths.
// The files stay until the agent stops: a steer reaches the model only when the
// current step ends, and Cline reads the file then.
func (a *Agent) writeTextAttachments(texts []agent.ClassifiedAttachment) ([]string, error) {
	a.Mu.Lock()
	a.attachSeq++
	seq := a.attachSeq
	a.Mu.Unlock()
	dir := filepath.Join(a.dir.Path(), attachmentsDir, strconv.FormatUint(seq, 10))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("prepare the attachment directory: %w", err)
	}
	paths := make([]string, 0, len(texts))
	taken := make(map[string]bool)
	for _, text := range texts {
		name := attachmentFileName(text.Filename, taken)
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, text.Data, 0o600); err != nil {
			return nil, fmt.Errorf("write the attachment %s: %w", text.Filename, err)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// attachmentFileName is the file's base name, kept inside the directory, and
// made unique within one message: a second `notes.md` becomes `notes-2.md`, or
// the next free number when another file of the message has that name too.
func attachmentFileName(filename string, taken map[string]bool) string {
	base := filepath.Base(filepath.Clean("/" + strings.ReplaceAll(filename, `\`, "/")))
	if base == "" || base == "." || base == "/" || base == ".." {
		base = "attachment.txt"
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	name := base
	for n := 2; taken[name]; n++ {
		name = stem + "-" + strconv.Itoa(n) + ext
	}
	taken[name] = true
	return name
}
