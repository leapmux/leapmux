package ohmypi

import (
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
)

// Each field appears once, at its first control. Later radio/checkbox options
// extend optionsEnd past any context blocks between that field's options.
type askHTMLFormBlock struct {
	field      *askHTMLField
	text       string
	isText     bool
	optionsEnd int
}

type askHTMLSection struct {
	title  string
	blocks []*askHTMLFormBlock
}

type askHTMLGroup struct {
	field   *askHTMLField
	section *askHTMLSection
	block   *askHTMLFormBlock
}

// Repeated labels and markup can exceed the input size. Stop retaining them
// before compiling duplicate labels or encoding the complete control request.
func (reader *askHTMLReader) retainMetadata(parts ...string) bool {
	for _, part := range parts {
		if len(part) > reader.metadataLeft {
			reader.err = fmt.Errorf("the ask form metadata exceeds the Worker message limit")
			return false
		}
		reader.metadataLeft -= len(part)
	}
	return true
}

func (reader *askHTMLReader) startSection(title string) {
	if reader.section != nil && reader.section.title == "" && len(reader.section.blocks) == 0 {
		reader.section.title = title
		return
	}
	reader.section = &askHTMLSection{title: title}
	reader.sections = append(reader.sections, reader.section)
}

func (reader *askHTMLReader) addField(field *askHTMLField) *askHTMLFormBlock {
	block := &askHTMLFormBlock{field: field}
	reader.section.blocks = append(reader.section.blocks, block)
	return block
}

// fieldQuestions and fieldContexts in the native form data module derive both
// values from the same parsed blocks. Captions affect questions; section titles
// and surrounding context retain their native paragraph boundaries.
func (reader *askHTMLReader) questions() ([]contracts.OhMyPiAskQuestion, error) {
	var questions []contracts.OhMyPiAskQuestion
	indices := make(map[*askHTMLField]int)
	seen := make(map[string]struct{})
	for _, section := range reader.sections {
		caption := ""
		for _, block := range section.blocks {
			if block.field == nil {
				caption = ""
				if block.isText {
					caption = block.text
				}
				continue
			}
			field := block.field
			if field.name != "" {
				if _, duplicate := seen[field.name]; duplicate {
					return nil, fmt.Errorf("the ask form repeats a field name across independent controls")
				}
				seen[field.name] = struct{}{}
				question, err := field.nativeQuestion(caption, section.title)
				if err != nil {
					return nil, err
				}
				if !reader.retainMetadata(question.ID, question.Question, question.Header) {
					return nil, reader.err
				}
				indices[field] = len(questions)
				questions = append(questions, question)
			}
			caption = ""
		}
	}

	contexts := make([][]string, len(questions))
	appendContext := func(index int, text string) bool {
		if len(contexts[index]) > 0 && !reader.retainMetadata("\n\n") {
			return false
		}
		if !reader.retainMetadata(text) {
			return false
		}
		contexts[index] = append(contexts[index], text)
		return true
	}
	var pending []string
	last := -1
	for _, section := range reader.sections {
		if title := strings.TrimFunc(section.title, askHTMLSpace); title != "" {
			pending = append(pending, title)
		}
		owner, ownerEnd := -1, 0
		for index, block := range section.blocks {
			if block.field != nil {
				if block.field.name == "" {
					continue
				}
				fieldIndex := indices[block.field]
				for _, part := range pending {
					if askOneLine(part) != questions[fieldIndex].Question {
						if !appendContext(fieldIndex, part) {
							return nil, reader.err
						}
					}
				}
				pending = nil
				last, owner, ownerEnd = fieldIndex, fieldIndex, block.optionsEnd
				for later := index + 1; later < ownerEnd; later++ {
					if field := section.blocks[later].field; field != nil && field.name != "" {
						ownerEnd = 0
						break
					}
				}
				continue
			}
			if strings.TrimFunc(block.text, askHTMLSpace) == "" {
				continue
			}
			if owner >= 0 && index < ownerEnd {
				if !appendContext(owner, block.text) {
					return nil, reader.err
				}
			} else {
				pending = append(pending, block.text)
			}
		}
	}
	if last >= 0 {
		for _, part := range pending {
			if !appendContext(last, part) {
				return nil, reader.err
			}
		}
	}
	for index := range questions {
		questions[index].Context = strings.Join(contexts[index], "\n\n")
	}
	return questions, nil
}
