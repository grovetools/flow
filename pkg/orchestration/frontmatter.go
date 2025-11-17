package orchestration

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

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
				return nil, nil, fmt.Errorf("invalid frontmatter: no closing delimiter found")
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
	
	// Find where the body content starts (after closing delimiter and newline)
	bodyStart := endIdx
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
	
	// Update fields in the document
	for key, value := range updates {
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

// RebuildMarkdownWithFrontmatter rebuilds a markdown file with new frontmatter data
func RebuildMarkdownWithFrontmatter(frontmatter map[string]interface{}, body []byte) ([]byte, error) {
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