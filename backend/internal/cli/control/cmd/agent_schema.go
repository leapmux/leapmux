package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/spf13/pflag"

	"github.com/leapmux/leapmux/internal/cli"
)

const (
	outputSchemaFlag     = "output-schema"
	schemaRetriesFlag    = "schema-retries"
	defaultSchemaRetries = 2
	maxSchemaRetries     = 10
)

func addOutputSchemaFlags(fs *pflag.FlagSet) {
	fs.String(outputSchemaFlag, "", `validate the reply against a JSON Schema (file path, inline JSON object, or "-" for stdin); requires --wait`)
	fs.Int(schemaRetriesFlag, defaultSchemaRetries, fmt.Sprintf("with --output-schema, maximum corrective follow-up turns on validation failure (0-%d)", maxSchemaRetries))
}

type outputSchemaSpec struct {
	schema      *jsonschema.Schema
	compactJSON string
	maxRetries  int
}

// readOutputSchemaSpec parses and compiles --output-schema and --schema-retries
// before dialing the Hub or Worker, failing fast on invalid flag combinations
// or malformed schemas.
func readOutputSchemaSpec(inv *cli.Invocation, wait, hasMessage, stdinConsumed bool) (*outputSchemaSpec, error) {
	hasSchema := inv.Changed(outputSchemaFlag)
	hasRetries := inv.Changed(schemaRetriesFlag)
	if !hasSchema {
		if hasRetries {
			return nil, cli.Usagef("--%s needs --%s", schemaRetriesFlag, outputSchemaFlag)
		}
		return nil, nil
	}
	if !wait {
		return nil, cli.Usagef("--%s needs --wait", outputSchemaFlag)
	}
	if !hasMessage {
		return nil, cli.Usagef("--%s needs a prompt (--message or --file)", outputSchemaFlag)
	}
	retries := defaultSchemaRetries
	if hasRetries {
		var err error
		retries, err = inv.Command.Flags().GetInt(schemaRetriesFlag)
		if err != nil {
			return nil, cli.Usagef("invalid --%s: %v", schemaRetriesFlag, err)
		}
		if retries < 0 || retries > maxSchemaRetries {
			return nil, cli.Usagef("--%s must be between 0 and %d; got %d", schemaRetriesFlag, maxSchemaRetries, retries)
		}
	}
	rawArg := strings.TrimSpace(stringFlag(inv, outputSchemaFlag))
	if rawArg == "" {
		return nil, cli.Usagef("--%s is empty", outputSchemaFlag)
	}
	var rawBytes []byte
	var err error
	switch {
	case rawArg == "-":
		if stdinConsumed {
			return nil, cli.Usagef("--%s cannot read stdin because the prompt already reads stdin", outputSchemaFlag)
		}
		rawBytes, err = readStdinGuarded(inv.Stdin)
		if err != nil {
			return nil, err
		}
	case strings.HasPrefix(rawArg, "{") || strings.HasPrefix(rawArg, "["):
		rawBytes = []byte(rawArg)
	default:
		rawBytes, err = os.ReadFile(rawArg)
		if err != nil {
			return nil, cli.Wrap(cli.CodeUsage, err, "read --"+outputSchemaFlag)
		}
	}
	spec, err := compileOutputSchema(rawBytes, retries)
	if err != nil {
		return nil, err
	}
	return spec, nil
}

func compileOutputSchema(raw []byte, maxRetries int) (*outputSchemaSpec, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, cli.Usagef("--%s is empty", outputSchemaFlag)
	}
	// Require a JSON object schema (boolean or primitive schemas are almost
	// certainly mistakes on the command line).
	if trimmed[0] != '{' {
		return nil, cli.Usagef("--%s must be a JSON Schema object", outputSchemaFlag)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, trimmed); err != nil {
		return nil, cli.Usagef("--%s is not valid JSON: %v", outputSchemaFlag, err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(compact.Bytes()))
	if err != nil {
		return nil, cli.Usagef("--%s is not valid JSON: %v", outputSchemaFlag, err)
	}
	const schemaURL = "leapmux://output-schema.json"
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemaURL, doc); err != nil {
		return nil, cli.Usagef("--%s is not a valid JSON Schema: %v", outputSchemaFlag, err)
	}
	compiled, err := c.Compile(schemaURL)
	if err != nil {
		return nil, cli.Usagef("--%s is not a valid JSON Schema: %v", outputSchemaFlag, err)
	}
	return &outputSchemaSpec{
		schema:      compiled,
		compactJSON: compact.String(),
		maxRetries:  maxRetries,
	}, nil
}

func augmentPromptWithSchema(prompt string, spec *outputSchemaSpec) string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(prompt, "\r\n"))
	b.WriteString("\n\nRespond with ONLY valid JSON that conforms to this JSON Schema. Do not include prose or markdown fences outside the JSON value:\n")
	b.WriteString(spec.compactJSON)
	return b.String()
}

func schemaCorrectionPrompt(validationErr error, spec *outputSchemaSpec) string {
	var b strings.Builder
	b.WriteString("Your previous reply did not match the required JSON Schema:\n")
	b.WriteString(validationErr.Error())
	b.WriteString("\n\nReply again with ONLY valid JSON matching this JSON Schema (no surrounding prose or markdown fences):\n")
	b.WriteString(spec.compactJSON)
	return b.String()
}

var fencedJSONBlockPattern = regexp.MustCompile("(?s)```(?:json|JSON)?[ \\t]*\\r?\\n(.*?)\\r?\\n```")

// extractCandidateJSON returns a trimmed JSON candidate from an agent reply.
// It tries the raw reply first, then falls back to a single fenced code block
// if the agent wrapped its JSON despite instructions.
func extractCandidateJSON(reply string) (string, error) {
	trimmed := strings.TrimSpace(reply)
	if trimmed == "" {
		return "", errors.New("agent reply was empty")
	}
	if json.Valid([]byte(trimmed)) {
		return trimmed, nil
	}
	matches := fencedJSONBlockPattern.FindAllStringSubmatch(trimmed, -1)
	if len(matches) == 1 {
		inner := strings.TrimSpace(matches[0][1])
		if json.Valid([]byte(inner)) {
			return inner, nil
		}
		return "", fmt.Errorf("fenced block is not valid JSON")
	}
	return "", errors.New("reply is not valid JSON")
}

// extractAndValidateJSON extracts a JSON value from reply and validates it
// against spec.schema. On success it returns the compact validated JSON.
func extractAndValidateJSON(reply string, spec *outputSchemaSpec) (json.RawMessage, error) {
	candidate, err := extractCandidateJSON(reply)
	if err != nil {
		return nil, err
	}
	val, err := jsonschema.UnmarshalJSON(strings.NewReader(candidate))
	if err != nil {
		return nil, fmt.Errorf("parse JSON reply: %w", err)
	}
	if err := spec.schema.Validate(val); err != nil {
		return nil, fmt.Errorf("JSON Schema validation failed: %s", formatSchemaValidationError(err))
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(candidate)); err != nil {
		return nil, fmt.Errorf("normalize JSON reply: %w", err)
	}
	return json.RawMessage(compact.Bytes()), nil
}

func formatSchemaValidationError(err error) string {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return err.Error()
	}
	out := ve.BasicOutput()
	var lines []string
	for _, unit := range out.Errors {
		if unit.Error == nil {
			continue
		}
		msg := unit.Error.String()
		if msg == "" {
			continue
		}
		loc := unit.InstanceLocation
		if loc == "" {
			loc = "/"
		}
		lines = append(lines, fmt.Sprintf("%s: %s", loc, msg))
		if len(lines) >= 8 {
			break
		}
	}
	if len(lines) == 0 {
		return ve.Error()
	}
	return strings.Join(lines, "; ")
}

// sendAndWaitWithSchema sends an augmented prompt, waits for the reply,
// validates it against spec, and performs up to spec.maxRetries corrective
// follow-up turns within the shared timeout budget.
func sendAndWaitWithSchema(
	inv *cli.Invocation,
	s *session,
	t tabTarget,
	message userMessage,
	requestID, inputID string,
	delivery sendDeliveryMode,
	all bool,
	timeout time.Duration,
	spec *outputSchemaSpec,
) (agentReplyResult, replyWaitOutcome, retryStage, error) {
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	remainingTimeout := func() (time.Duration, bool) {
		if timeout <= 0 {
			return 0, true
		}
		rem := time.Until(deadline)
		if rem <= 0 {
			return 0, false
		}
		return rem, true
	}

	firstMsg := message
	firstMsg.text = augmentPromptWithSchema(message.text, spec)
	curMsg := firstMsg
	curInputID := inputID
	curDelivery := delivery

	var lastResult agentReplyResult
	var lastValidationErr error
	for attempt := 0; attempt <= spec.maxRetries; attempt++ {
		rem, ok := remainingTimeout()
		if !ok {
			lastResult.SchemaAttempts = attempt
			return lastResult, replyWaitTimedOut, retryStageDuringWait, nil
		}
		res, outcome, stage, err := sendAndWaitWithDelivery(s, t, curMsg, curInputID, curDelivery, all, rem)
		if err != nil {
			return res, outcome, stage, err
		}
		lastResult = res
		lastResult.SchemaAttempts = attempt + 1
		if outcome != replyWaitSettled {
			return lastResult, outcome, retryStageDuringWait, nil
		}
		replyText := ""
		if len(res.Replies) > 0 {
			replyText = res.Replies[len(res.Replies)-1]
		}
		structured, valErr := extractAndValidateJSON(replyText, spec)
		if valErr == nil {
			lastResult.Structured = structured
			return lastResult, replyWaitSettled, retryStageDuringWait, nil
		}
		lastValidationErr = valErr
		if attempt == spec.maxRetries {
			break
		}
		inv.Printer.Notice("reply failed --output-schema validation (attempt %d/%d); requesting correction", attempt+1, spec.maxRetries+1)
		curMsg = userMessage{text: schemaCorrectionPrompt(valErr, spec)}
		curInputID = requestScopedID(requestID, requestScopeAgentSend, t.ID, "schema-retry", strconv.Itoa(attempt+1))
		curDelivery = sendDeliveryNormal
	}
	return lastResult, 0, retryStageDuringWait, cli.Newf(
		cli.CodeConflict,
		"agent %s did not return output matching --%s after %d attempt(s): %v",
		cli.ShortID(t.ID), outputSchemaFlag, spec.maxRetries+1, lastValidationErr,
	).WithHint("inspect the transcript with `leapmux agent messages %s` or increase --%s", cli.ShortID(t.ID), schemaRetriesFlag).
		WithExitCode(cli.ExitWaitEnded)
}
