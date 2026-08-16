package orchestration

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// isNilValue reports whether v is nil, including typed nils such as a nil
// *time.Time stored in an interface{}. Frontmatter writers use it so that a
// nil update value means "remove this key" — formatting a nil through
// fmt.Sprint would otherwise emit the literal string "<nil>" into YAML,
// which corrupts job files (time fields then fail to parse on load).
func isNilValue(v interface{}) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return rv.IsNil()
	}
	return false
}

// ParseFrontmatter extracts YAML frontmatter from markdown content.
// Returns the parsed YAML as a map, the remaining content, and any error.
func ParseFrontmatter(content []byte) (map[string]interface{}, []byte, error) {
	// Convert to string for easier manipulation
	contentStr := string(content)

	// Check if the file starts with frontmatter delimiter
	if !strings.HasPrefix(contentStr, "---\n") && !strings.HasPrefix(contentStr, "---\r\n") {
		// No frontmatter, return empty map and full content
		return make(map[string]interface{}), content, nil
	}

	// Find the closing delimiter
	// Start after the opening "---"
	startIdx := strings.Index(contentStr, "\n") + 1
	if startIdx == 0 {
		return nil, nil, fmt.Errorf("invalid frontmatter: no newline after opening delimiter")
	}

	// Look for the closing "---" on its own line
	var endIdx int
	// Handle empty frontmatter case where closing delimiter comes immediately
	if strings.HasPrefix(contentStr[startIdx:], "---\n") {
		endIdx = startIdx
	} else {
		tmpIdx := strings.Index(contentStr[startIdx:], "\n---\n")
		if tmpIdx == -1 {
			// Try with Windows line endings
			tmpIdx = strings.Index(contentStr[startIdx:], "\r\n---\r\n")
			if tmpIdx == -1 {
				// Also accept a closing delimiter at EOF with no trailing
				// newline (a file ending exactly with "---"). This only adds
				// the EOF case; the normal "\n---\n" path above is unchanged.
				rest := contentStr[startIdx:]
				switch {
				case strings.HasSuffix(rest, "\n---"):
					tmpIdx = len(rest) - len("\n---")
				case strings.HasSuffix(rest, "\r\n---"):
					tmpIdx = len(rest) - len("\r\n---")
				default:
					return nil, nil, fmt.Errorf("invalid frontmatter: no closing delimiter found")
				}
			}
		}
		endIdx = startIdx + tmpIdx
	}

	// Extract the YAML content
	yamlContent := contentStr[startIdx:endIdx]

	// Parse the YAML
	var frontmatter map[string]interface{}
	if yamlContent == "" {
		// Empty frontmatter is valid
		frontmatter = make(map[string]interface{})
	} else if err := yaml.Unmarshal([]byte(yamlContent), &frontmatter); err != nil {
		return nil, nil, fmt.Errorf("parsing frontmatter: %w", err)
	}

	// Self-heal historical corruption: writers used to stringify nil
	// pointers, leaving the literal "<nil>" in fields like completed_at.
	// Treat such values as unset so any parse→rebuild cycle scrubs them.
	for k, v := range frontmatter {
		if s, ok := v.(string); ok && s == "<nil>" {
			delete(frontmatter, k)
		}
	}

	// Find where the body content starts (after closing delimiter and newline)
	var bodyStart int
	if endIdx == startIdx {
		// Empty frontmatter case
		bodyStart = startIdx + 4 // Skip "---\n"
	} else {
		bodyStart = endIdx + 5 // length of "\n---\n"
	}
	if bodyStart > len(contentStr) {
		bodyStart = len(contentStr)
	}

	remainingContent := []byte(contentStr[bodyStart:])

	return frontmatter, remainingContent, nil
}

// UpdateFrontmatter updates specific fields in the frontmatter while preserving formatting.
func UpdateFrontmatter(content []byte, updates map[string]interface{}) ([]byte, error) {
	// Extract raw frontmatter string
	frontmatterStr, body, err := ExtractFrontmatterString(content)
	if err != nil {
		return nil, err
	}

	// If no frontmatter exists, create new one
	if frontmatterStr == "" {
		newFrontmatter := make(map[string]interface{})
		for k, v := range updates {
			// A nil value means "unset"; there is no existing key to
			// remove, so simply don't add it.
			if isNilValue(v) {
				continue
			}
			newFrontmatter[k] = v
		}

		yamlBytes, err := yaml.Marshal(newFrontmatter)
		if err != nil {
			return nil, fmt.Errorf("marshaling new frontmatter: %w", err)
		}

		// Combine with body
		var result bytes.Buffer
		result.WriteString("---\n")
		result.Write(yamlBytes)
		result.WriteString("---\n")
		result.Write(body)

		return result.Bytes(), nil
	}

	// Update existing frontmatter using Node API
	updatedYAML, err := UpdateFrontmatterNode([]byte(frontmatterStr), updates)
	if err != nil {
		return nil, err
	}

	// Reconstruct the file
	return ReplaceFrontmatter(content, string(updatedYAML)), nil
}

// UpdateFrontmatterNode updates YAML using the Node API to preserve formatting.
func UpdateFrontmatterNode(yamlData []byte, updates map[string]interface{}) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(yamlData, &root); err != nil {
		return nil, fmt.Errorf("unmarshaling YAML: %w", err)
	}

	// Navigate to the document content
	if len(root.Content) == 0 {
		return nil, fmt.Errorf("no YAML document found")
	}
	doc := root.Content[0]

	// Update fields in the document. A nil value (including typed nils like
	// a nil *time.Time) removes the key: it must never be stringified, or
	// the literal "<nil>" ends up in the file and time fields become
	// unparseable on load.
	for key, value := range updates {
		if isNilValue(value) {
			removeNodeKey(doc, key)
			continue
		}
		updateNodeValue(doc, key, value)
	}

	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(&root); err != nil {
		return nil, fmt.Errorf("encoding YAML: %w", err)
	}

	return buf.Bytes(), nil
}

// removeNodeKey deletes a key/value pair from a YAML mapping node. Removing
// a key that isn't present is a no-op.
func removeNodeKey(node *yaml.Node, key string) {
	if node.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i < len(node.Content)-1; i += 2 {
		if node.Content[i].Value == key {
			node.Content = append(node.Content[:i], node.Content[i+2:]...)
			return
		}
	}
}

// updateNodeValue updates a specific field in a YAML node.
func updateNodeValue(node *yaml.Node, key string, value interface{}) {
	if node.Kind != yaml.MappingNode {
		return
	}

	// Iterate through key-value pairs
	for i := 0; i < len(node.Content)-1; i += 2 {
		keyNode := node.Content[i]
		if keyNode.Value == key {
			// Update the value node
			valueNode := node.Content[i+1]

			// Handle different types of values
			switch v := value.(type) {
			case []string:
				// Handle string arrays
				valueNode.Kind = yaml.SequenceNode
				valueNode.Tag = "!!seq"
				valueNode.Value = ""
				valueNode.Content = make([]*yaml.Node, len(v))
				for j, item := range v {
					valueNode.Content[j] = &yaml.Node{
						Kind:  yaml.ScalarNode,
						Value: item,
						Tag:   "!!str",
					}
				}
			case []interface{}:
				// Handle generic arrays
				valueNode.Kind = yaml.SequenceNode
				valueNode.Tag = "!!seq"
				valueNode.Value = ""
				valueNode.Content = make([]*yaml.Node, len(v))
				for j, item := range v {
					valueNode.Content[j] = &yaml.Node{
						Kind:  yaml.ScalarNode,
						Value: fmt.Sprint(item),
						Tag:   resolveYAMLTag(item),
					}
				}
			default:
				// Handle scalar values
				valueNode.Kind = yaml.ScalarNode
				valueNode.Value = fmt.Sprint(value)
				valueNode.Tag = resolveYAMLTag(value)
			}
			return
		}
	}

	// Key not found, add it
	keyNode := &yaml.Node{
		Kind:  yaml.ScalarNode,
		Value: key,
		Tag:   "!!str",
	}

	var valueNode *yaml.Node

	// Handle different types of values for new keys
	switch v := value.(type) {
	case []string:
		valueNode = &yaml.Node{
			Kind:    yaml.SequenceNode,
			Tag:     "!!seq",
			Content: make([]*yaml.Node, len(v)),
		}
		for j, item := range v {
			valueNode.Content[j] = &yaml.Node{
				Kind:  yaml.ScalarNode,
				Value: item,
				Tag:   "!!str",
			}
		}
	case []interface{}:
		valueNode = &yaml.Node{
			Kind:    yaml.SequenceNode,
			Tag:     "!!seq",
			Content: make([]*yaml.Node, len(v)),
		}
		for j, item := range v {
			valueNode.Content[j] = &yaml.Node{
				Kind:  yaml.ScalarNode,
				Value: fmt.Sprint(item),
				Tag:   resolveYAMLTag(item),
			}
		}
	default:
		valueNode = &yaml.Node{
			Kind:  yaml.ScalarNode,
			Value: fmt.Sprint(value),
			Tag:   resolveYAMLTag(value),
		}
	}

	node.Content = append(node.Content, keyNode, valueNode)
}

// resolveYAMLTag determines the appropriate YAML tag for a value.
func resolveYAMLTag(value interface{}) string {
	switch value.(type) {
	case string:
		return "!!str"
	case int, int64, int32:
		return "!!int"
	case float64, float32:
		return "!!float"
	case bool:
		return "!!bool"
	default:
		return "!!str"
	}
}

// ExtractFrontmatterString extracts the raw YAML string between delimiters.
func ExtractFrontmatterString(content []byte) (string, []byte, error) {
	contentStr := string(content)

	// Check if the file starts with frontmatter delimiter
	if !strings.HasPrefix(contentStr, "---\n") && !strings.HasPrefix(contentStr, "---\r\n") {
		// No frontmatter
		return "", content, nil
	}

	// Find the closing delimiter
	startIdx := strings.Index(contentStr, "\n") + 1
	if startIdx == 0 {
		return "", nil, fmt.Errorf("invalid frontmatter: no newline after opening delimiter")
	}

	endIdx := strings.Index(contentStr[startIdx:], "\n---\n")
	if endIdx == -1 {
		endIdx = strings.Index(contentStr[startIdx:], "\r\n---\r\n")
		if endIdx == -1 {
			return "", nil, fmt.Errorf("invalid frontmatter: no closing delimiter found")
		}
	}
	endIdx += startIdx

	// Extract the YAML content
	yamlContent := contentStr[startIdx:endIdx]

	// Find where the body content starts
	bodyStart := endIdx + 5 // length of "\n---\n"
	if bodyStart > len(contentStr) {
		bodyStart = len(contentStr)
	}

	remainingContent := []byte(contentStr[bodyStart:])

	return yamlContent, remainingContent, nil
}

// ReplaceFrontmatter replaces existing frontmatter with new YAML string.
func ReplaceFrontmatter(content []byte, newFrontmatter string) []byte {
	_, body, _ := ExtractFrontmatterString(content)

	var result bytes.Buffer
	result.WriteString("---\n")
	result.WriteString(strings.TrimSpace(newFrontmatter))
	result.WriteString("\n---\n")
	result.Write(body)

	return result.Bytes()
}

// SanitizeFrontmatter removes empty values from frontmatter fields that would
// otherwise serialize as empty arrays (e.g., `branch: []`) due to template
// variables evaluating to empty strings during recipe rendering.
func SanitizeFrontmatter(fm map[string]interface{}) {
	for _, key := range []string{"branch", "worktree", "repository"} {
		val, ok := fm[key]
		if !ok {
			continue
		}
		switch v := val.(type) {
		case string:
			if v == "" {
				delete(fm, key)
			}
		case []interface{}:
			if len(v) == 0 {
				delete(fm, key)
			}
		case nil:
			delete(fm, key)
		}
	}
}

// RebuildMarkdownWithFrontmatter rebuilds a markdown file with new frontmatter data
func RebuildMarkdownWithFrontmatter(frontmatter map[string]interface{}, body []byte) ([]byte, error) {
	// Sanitize before marshaling to prevent empty arrays in YAML output
	SanitizeFrontmatter(frontmatter)

	// Nil values (including typed nils) mean "unset": drop the key rather
	// than serializing it. yaml.Marshal would write `key: null`, and a
	// stringifying writer would write the corrupting literal "<nil>".
	for k, v := range frontmatter {
		if isNilValue(v) {
			delete(frontmatter, k)
		}
	}

	// Marshal the frontmatter to YAML
	yamlBytes, err := yaml.Marshal(frontmatter)
	if err != nil {
		return nil, fmt.Errorf("marshaling frontmatter: %w", err)
	}

	// Build the complete markdown file
	var result bytes.Buffer
	result.WriteString("---\n")
	result.Write(yamlBytes)
	result.WriteString("---\n")
	result.Write(body)

	return result.Bytes(), nil
}
