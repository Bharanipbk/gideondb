package metadata

import (
	"encoding/json"
	"testing"
)

func FuzzParseNeverPanics(f *testing.F) {
	for _, seed := range []string{
		`{"topic":{"$eq":"database"}}`,
		`{"$and":[{"score":{"$gte":0.5}},{"active":{"$eq":true}}]}`,
		`null`, `{}`, `{"$not":[]}`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 64<<10 {
			t.Skip()
		}
		var value any
		if json.Unmarshal([]byte(raw), &value) != nil {
			return
		}
		object, ok := value.(map[string]any)
		if !ok {
			return
		}
		_, _ = Parse(object)
	})
}
