package kiro

// kiroPromptParams removes the file URI from image blocks before Kiro v3 reads
// them. With both data and URI, Kiro v3 drops the image from its model request.
// Text and PDF resources keep their URI because Kiro reads those blocks.
func kiroPromptParams(params map[string]any) {
	blocks, ok := params["prompt"].([]map[string]interface{})
	if !ok {
		return
	}
	for _, block := range blocks {
		if block["type"] == "image" {
			delete(block, "uri")
		}
	}
}
