package stages

import (
	"encoding/json"
	"os"
)

func readFileBytes(path string) ([]byte, error) { return os.ReadFile(path) }
func jsonUnmarshal(b []byte, v any) error       { return json.Unmarshal(b, v) }
