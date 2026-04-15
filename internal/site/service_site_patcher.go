package site

import (
	"fmt"
)

// PatchOperation represents a single edit operation on a Puck document.
type PatchOperation struct {
	Op     string         `json:"op"`
	NodeID string         `json:"nodeId,omitempty"`
	Path   string         `json:"path,omitempty"`
	Props  map[string]any `json:"props,omitempty"`
	Node   map[string]any `json:"node,omitempty"`
	Index  *int           `json:"index,omitempty"`
}

// PatchDocument represents a set of patch operations to apply.
type PatchDocument struct {
	Operations []PatchOperation `json:"operations"`
}

// ApplyPatches applies a list of patch operations to a Puck document.
// Supported operations:
//   - replaceProps: update props on a component identified by nodeId
//   - insertAfter: insert a new component after the one identified by nodeId
//   - insertBefore: insert a new component before the one identified by nodeId
//   - remove: remove the component identified by nodeId
//   - insertAt: insert a new component at a specific index in the content array
//   - moveAfter: move an existing component after another
func ApplyPatches(doc map[string]any, patches PatchDocument) (map[string]any, error) {
	if doc == nil {
		return nil, fmt.Errorf("cannot patch nil document")
	}
	content, ok := doc["content"].([]any)
	if !ok {
		content = []any{}
	}

	for i, op := range patches.Operations {
		var err error
		switch op.Op {
		case "replaceProps":
			content, err = patchReplaceProps(content, op)
		case "insertAfter":
			content, err = patchInsertRelative(content, op, true)
		case "insertBefore":
			content, err = patchInsertRelative(content, op, false)
		case "remove":
			content, err = patchRemove(content, op)
		case "insertAt":
			content, err = patchInsertAt(content, op)
		case "moveAfter":
			content, err = patchMoveAfter(content, op)
		default:
			return nil, fmt.Errorf("operation %d: unknown op %q", i, op.Op)
		}
		if err != nil {
			return nil, fmt.Errorf("operation %d (%s): %w", i, op.Op, err)
		}
	}

	doc["content"] = content
	return doc, nil
}

// patchReplaceProps finds a component by nodeId and merges new props into it.
func patchReplaceProps(content []any, op PatchOperation) ([]any, error) {
	if op.NodeID == "" {
		return nil, fmt.Errorf("replaceProps requires nodeId")
	}
	if op.Props == nil {
		return nil, fmt.Errorf("replaceProps requires props")
	}
	found := false
	for i, item := range content {
		comp, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if getComponentID(comp) == op.NodeID {
			props, _ := comp["props"].(map[string]any)
			if props == nil {
				props = map[string]any{}
			}
			for k, v := range op.Props {
				props[k] = v
			}
			comp["props"] = props
			content[i] = comp
			found = true
			break
		}
		// Search in nested Columns children.
		if comp["type"] == "Columns" {
			if updated, err := replacePropsInColumns(comp, op); err == nil && updated {
				content[i] = comp
				found = true
				break
			}
		}
	}
	if !found {
		return nil, fmt.Errorf("node %q not found", op.NodeID)
	}
	return content, nil
}

// replacePropsInColumns recursively searches Columns for a nodeId.
func replacePropsInColumns(comp map[string]any, op PatchOperation) (bool, error) {
	props, _ := comp["props"].(map[string]any)
	if props == nil {
		return false, nil
	}
	cols, ok := props["columns"].([]any)
	if !ok {
		return false, nil
	}
	for _, col := range cols {
		colMap, ok := col.(map[string]any)
		if !ok {
			continue
		}
		children, ok := colMap["children"].([]any)
		if !ok {
			continue
		}
		for j, child := range children {
			childComp, ok := child.(map[string]any)
			if !ok {
				continue
			}
			if getComponentID(childComp) == op.NodeID {
				childProps, _ := childComp["props"].(map[string]any)
				if childProps == nil {
					childProps = map[string]any{}
				}
				for k, v := range op.Props {
					childProps[k] = v
				}
				childComp["props"] = childProps
				children[j] = childComp
				return true, nil
			}
		}
	}
	return false, nil
}

// patchInsertRelative inserts a node before or after a target node.
func patchInsertRelative(content []any, op PatchOperation, after bool) ([]any, error) {
	if op.NodeID == "" {
		return nil, fmt.Errorf("insert requires nodeId")
	}
	if op.Node == nil {
		return nil, fmt.Errorf("insert requires node")
	}
	idx := -1
	for i, item := range content {
		comp, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if getComponentID(comp) == op.NodeID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, fmt.Errorf("node %q not found", op.NodeID)
	}
	insertIdx := idx
	if after {
		insertIdx = idx + 1
	}
	// Insert at position.
	result := make([]any, 0, len(content)+1)
	result = append(result, content[:insertIdx]...)
	result = append(result, op.Node)
	result = append(result, content[insertIdx:]...)
	return result, nil
}

// patchRemove removes a component by nodeId.
func patchRemove(content []any, op PatchOperation) ([]any, error) {
	if op.NodeID == "" {
		return nil, fmt.Errorf("remove requires nodeId")
	}
	result := make([]any, 0, len(content))
	found := false
	for _, item := range content {
		comp, ok := item.(map[string]any)
		if !ok {
			result = append(result, item)
			continue
		}
		if getComponentID(comp) == op.NodeID {
			found = true
			continue
		}
		result = append(result, item)
	}
	if !found {
		return nil, fmt.Errorf("node %q not found", op.NodeID)
	}
	return result, nil
}

// patchInsertAt inserts a node at a specific index.
func patchInsertAt(content []any, op PatchOperation) ([]any, error) {
	if op.Node == nil {
		return nil, fmt.Errorf("insertAt requires node")
	}
	idx := len(content) // Default: append
	if op.Index != nil {
		idx = *op.Index
	}
	if idx < 0 {
		idx = 0
	}
	if idx > len(content) {
		idx = len(content)
	}
	result := make([]any, 0, len(content)+1)
	result = append(result, content[:idx]...)
	result = append(result, op.Node)
	result = append(result, content[idx:]...)
	return result, nil
}

// patchMoveAfter moves a component identified by nodeId to after the target component identified by Path.
func patchMoveAfter(content []any, op PatchOperation) ([]any, error) {
	if op.NodeID == "" || op.Path == "" {
		return nil, fmt.Errorf("moveAfter requires both nodeId and path (target nodeId)")
	}
	// Find and remove the source node.
	var movedNode any
	remaining := make([]any, 0, len(content))
	for _, item := range content {
		comp, ok := item.(map[string]any)
		if !ok {
			remaining = append(remaining, item)
			continue
		}
		if getComponentID(comp) == op.NodeID {
			movedNode = item
			continue
		}
		remaining = append(remaining, item)
	}
	if movedNode == nil {
		return nil, fmt.Errorf("source node %q not found", op.NodeID)
	}
	// Find target position and insert after it.
	targetIdx := -1
	for i, item := range remaining {
		comp, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if getComponentID(comp) == op.Path {
			targetIdx = i
			break
		}
	}
	if targetIdx < 0 {
		return nil, fmt.Errorf("target node %q not found", op.Path)
	}
	result := make([]any, 0, len(remaining)+1)
	result = append(result, remaining[:targetIdx+1]...)
	result = append(result, movedNode)
	result = append(result, remaining[targetIdx+1:]...)
	return result, nil
}

// getComponentID extracts the identifier from a component.
// Uses props.id first, then falls back to the type field.
func getComponentID(comp map[string]any) string {
	if props, ok := comp["props"].(map[string]any); ok {
		if id, ok := props["id"].(string); ok && id != "" {
			return id
		}
	}
	if t, ok := comp["type"].(string); ok {
		return t
	}
	return ""
}
