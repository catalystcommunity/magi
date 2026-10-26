package workshop

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestModelSchemaFollowsTrustedLoopState(t *testing.T) {
	a := &Adapter{}
	for _, finish := range []bool{false, true} {
		input, _ := json.Marshal(map[string]any{"messages": []any{map[string]string{"role": "system", "content": "test"}, map[string]string{"role": "user", "content": "test"}}, "allowed_tools": []string{"read_file"}, "can_finish": finish})
		body, _, e := a.model(context.Background(), input)
		if e != nil {
			t.Fatal(e)
		}
		var request struct {
			Format struct {
				Schema struct {
					Body struct {
						Choices []json.RawMessage `json:"oneOf"`
					} `json:"schema"`
				} `json:"json_schema"`
			} `json:"response_format"`
		}
		if e = json.Unmarshal(body, &request); e != nil {
			t.Fatal(e)
		}
		choices := request.Format.Schema.Body.Choices
		want := 1
		if finish {
			want = 2
		}
		if len(choices) != want {
			t.Fatal("unexpected actions", string(body))
		}
		schema := string(choices[0])
		if !strings.Contains(schema, `"const":"read_file"`) || strings.Index(schema, `"tool":`) > strings.Index(schema, `"arguments":`) {
			t.Fatal("action must precede arguments", schema)
		}
		if finish && !strings.Contains(string(choices[1]), `"answer"`) {
			t.Fatal("missing permitted finish")
		}
	}
	_, _, e := a.model(context.Background(), json.RawMessage(`{"messages":[{},{}],"allowed_tools":["shell"],"can_finish":false}`))
	if e == nil {
		t.Fatal("uninstalled action allowed")
	}
}
