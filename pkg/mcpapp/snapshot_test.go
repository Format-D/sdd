package mcpapp_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/bradleyjkemp/cupaloy/v2"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// snapshotResponse records a whole tool response as readable text and compares
// it with the stored snapshot (d-cpt-t8i). Prose renders line by line so a diff
// names the changed sentence; session handles are aliased per distinct value.
func snapshotResponse(t *testing.T, res *mcp.CallToolResult) {
	t.Helper()
	var sb strings.Builder
	if res.IsError {
		sb.WriteString("isError: true\n")
	}
	if res.IsError || res.StructuredContent == nil {
		writeScalar(&sb, "content", contentText(res), 0)
	}
	if res.StructuredContent != nil {
		raw, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		writeValue(&sb, value, 0)
	}
	cupaloy.New(cupaloy.SnapshotFileExtension(".txt")).SnapshotT(t, aliasSessions(sb.String()))
}

var sessionHandle = regexp.MustCompile(`s_\d{8}-\d{6}-[0-9a-f]{32}`)

func aliasSessions(text string) string {
	aliases := map[string]string{}
	return sessionHandle.ReplaceAllStringFunc(text, func(handle string) string {
		alias, ok := aliases[handle]
		if !ok {
			alias = fmt.Sprintf("<session-%d>", len(aliases)+1)
			aliases[handle] = alias
		}
		return alias
	})
}

func writeValue(sb *strings.Builder, value any, indent int) {
	pad := strings.Repeat(" ", indent)
	switch v := value.(type) {
	case map[string]any:
		for _, key := range slices.Sorted(mapKeys(v)) {
			writeField(sb, key, v[key], indent)
		}
	case []any:
		for _, item := range v {
			switch item.(type) {
			case map[string]any, []any:
				sb.WriteString(pad + "-\n")
				writeValue(sb, item, indent+2)
			default:
				sb.WriteString(pad + "- " + scalar(item) + "\n")
			}
		}
	default:
		sb.WriteString(pad + scalar(v) + "\n")
	}
}

func writeField(sb *strings.Builder, key string, value any, indent int) {
	pad := strings.Repeat(" ", indent)
	switch v := value.(type) {
	case map[string]any, []any:
		sb.WriteString(pad + key + ":\n")
		writeValue(sb, v, indent+2)
	case string:
		writeScalar(sb, key, v, indent)
	default:
		sb.WriteString(pad + key + ": " + scalar(v) + "\n")
	}
}

// writeScalar renders a string field; text with line breaks becomes an
// indented block so every line diffs on its own.
func writeScalar(sb *strings.Builder, key, text string, indent int) {
	pad := strings.Repeat(" ", indent)
	if !strings.Contains(text, "\n") {
		sb.WriteString(pad + key + ": " + text + "\n")
		return
	}
	sb.WriteString(pad + key + ": |\n")
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			sb.WriteString("\n")
			continue
		}
		sb.WriteString(pad + "  " + line + "\n")
	}
}

func scalar(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%v", v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func mapKeys(m map[string]any) func(func(string) bool) {
	return func(yield func(string) bool) {
		for key := range m {
			if !yield(key) {
				return
			}
		}
	}
}
