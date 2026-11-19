package ohmypi

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The expectations below are what the browser's reader of the same markup
// computed before the Worker took it over, case by case, quirks included.
func TestReadChoiceMarkup(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		markup, fallback string
		want             choicePresentation
	}{
		"no markup":   {"", "Fallback", choicePresentation{label: "Fallback"}},
		"no label":    {"<span>No label</span>", "Fallback", choicePresentation{label: "Fallback"}},
		"plain label": {"<label>  spaced \t\n  out  </label>", "spaced out", choicePresentation{label: "spaced out"}},
		"a strong heading and a description": {
			"<label><strong>Alpha</strong> <span>Fast</span></label>", "Alpha Fast",
			choicePresentation{label: "Alpha", description: "Fast"},
		},
		"a bold heading": {"<label><b>Bold</b> rest of it</label>", "Bold rest of it", choicePresentation{label: "Bold", description: "rest of it"}},
		"a heading after text is no heading": {
			"<label>Lead <strong>Alpha</strong> tail</label>", "Lead Alpha tail",
			choicePresentation{label: "Lead Alpha tail"},
		},
		"a comment and white space before the heading": {
			"<label><!-- note --> \n <strong>Alpha</strong> after</label>", "Alpha after",
			choicePresentation{label: "Alpha", description: "after"},
		},
		"a control before the heading": {
			"<label><input type=radio name=s value=a><strong>Alpha</strong> after</label>", "Alpha after",
			choicePresentation{label: "Alpha", description: "after"},
		},
		"an empty element before the heading is no heading": {
			"<label><span></span><strong>Alpha</strong> after</label>", "Alpha after",
			choicePresentation{label: "Alpha after"},
		},
		"a byte order mark before the heading is white space": {
			"<label>\uFEFF<strong>BOM</strong> d</label>", "BOM d",
			choicePresentation{label: "BOM", description: "d"},
		},
		"a no-break space before the heading is text": {
			"<label>\u00A0nbsp\u00A0 <strong>x</strong></label>", "nbsp x",
			choicePresentation{label: "nbsp x"},
		},
		"an empty heading shows the fallback": {
			"<label><strong></strong> only desc</label>", "only desc",
			choicePresentation{label: "only desc", description: "only desc"},
		},
		"a second heading is description": {"<label><strong>A</strong><strong>B</strong></label>", "A B", choicePresentation{label: "A", description: "B"}},
		"code is a preview": {
			"<label><strong>PostgreSQL</strong><pre>SELECT 1</pre></label>", "PostgreSQL SELECT 1",
			choicePresentation{label: "PostgreSQL", preview: "```\nSELECT 1\n```"},
		},
		"blank code is no preview": {"<label>x<pre> \n </pre></label>", "x", choicePresentation{label: "x"}},
		"two code blocks": {
			"<label>Two<pre>a</pre><pre>b</pre></label>", "Two a b",
			choicePresentation{label: "Two", preview: "```\na\n```\n\n```\nb\n```"},
		},
		"code that holds backticks": {
			"<label>Ticks<pre>use ``` and ````x</pre></label>", "Ticks use ``` and ````x",
			choicePresentation{label: "Ticks", preview: "`````\nuse ``` and ````x\n`````"},
		},
		"the newline that opens code": {
			"<label>Lead<pre>\nfirst line\nsecond</pre></label>", "Lead first line second",
			choicePresentation{label: "Lead", preview: "```\nfirst line\nsecond\n```"},
		},
		"code inside the heading stays in the title": {
			"<label><strong>Head<pre>in head</pre></strong> rest</label>", "Head in head rest",
			choicePresentation{label: "Headin head", description: "rest", preview: "```\nin head\n```"},
		},
		"code alone shows the fallback": {
			"<label><pre>only code</pre></label>", "only code",
			choicePresentation{label: "only code", preview: "```\nonly code\n```"},
		},
		"a script, a style and the controls show nothing": {
			"<label><style>p{}</style><select><option>opt</option></select><textarea>t</textarea><button>b</button><svg><text>svg</text></svg><strong>S</strong> d<script>DROP</script></label>", "S d",
			choicePresentation{label: "S", description: "d"},
		},
		"a label with no text shows the fallback": {"<label><input type=checkbox></label>", "Fallback", choicePresentation{label: "Fallback"}},
		"an image shows its alternative text": {
			"<label>Pic <img alt=\"a cat\" src=x.png> end</label>", "Pic a cat end",
			choicePresentation{label: "Pic a cat end"},
		},
		"a line break and a block open with a space": {
			"<label>Line<br>break<div>block</div>after</label>", "Line break block after",
			choicePresentation{label: "Line break blockafter"},
		},
		"entities": {"<label>&lt;b&gt; &amp; text</label>", "<b> & text", choicePresentation{label: "<b> & text"}},
		"a label inside a template is hidden": {
			"<template><label>Hidden</label></template><label>Shown</label>", "Shown",
			choicePresentation{label: "Shown"},
		},
		"a label deep in the markup": {
			"<div><p><label><strong>Deep</strong> desc</label></p></div>", "Deep desc",
			choicePresentation{label: "Deep", description: "desc"},
		},
		"the first label alone": {"<label>One</label><label>Two</label>", "One", choicePresentation{label: "One"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, readChoiceMarkup(tc.markup, tc.fallback))
		})
	}
}

// The bridge tells two options with the same text apart by a suffix of
// their labels (askHTMLField.nativeQuestion), and the title keeps it.
func TestReadChoiceMarkup_KeepsTheSuffixThatTellsTwoOptionsApart(t *testing.T) {
	t.Parallel()
	for fallback, want := range map[string]string{
		"Shared":      "Shared",
		"Shared (2)":  "Shared (2)",
		"Shared (10)": "Shared (10)",
		// The bridge starts at 2 and writes no leading zero.
		"Shared (1)":  "Shared",
		"Shared (02)": "Shared",
		"Shared(2)":   "Shared",
		// A label that does not continue the markup's text keeps no suffix.
		"Other (2)": "Shared",
	} {
		assert.Equal(t, want, readChoiceMarkup("<label>Shared<input type=checkbox></label>", fallback).label, fallback)
	}
	assert.Equal(t, choicePresentation{label: "Alpha (2)", description: "Fast"},
		readChoiceMarkup("<label><strong>Alpha</strong> Fast</label>", "Alpha Fast (2)"))
}

// The HTML parser refuses markup that holds more than 512 open elements. The
// bridge reads its form with the same parser and keeps no form that deep
// (readAskChoiceQuestions), so only a request that the bridge did not write
// reaches this, and its option shows its own label.
func TestReadChoiceMarkup_ShowsTheFallbackForMarkupThatTheParserRefuses(t *testing.T) {
	t.Parallel()
	nested := func(depth int) string {
		return strings.Repeat("<span>", depth) + "<label><strong>Deep</strong> desc</label>" + strings.Repeat("</span>", depth)
	}
	assert.Equal(t, choicePresentation{label: "Deep", description: "desc"}, readChoiceMarkup(nested(400), "Deep desc"))
	assert.Equal(t, choicePresentation{label: "Deep desc"}, readChoiceMarkup(nested(600), "Deep desc"))
}

func TestLiteralCodeBlock(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "```\n\n```", literalCodeBlock(""))
	assert.Equal(t, "```\nplain\n```", literalCodeBlock("plain"))
	assert.Equal(t, "```\na `b` c\n```", literalCodeBlock("a `b` c"))
	assert.Equal(t, "````\n```go\nx\n```\n````", literalCodeBlock("```go\nx\n```"))
}
