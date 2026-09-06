package log

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestNewWithWriterWritesJSON(t *testing.T) {
	var buf bytes.Buffer

	NewWithWriter(&buf).Info("restore started", "component", "runner")

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("logger output is not valid JSON: %v", err)
	}
	if record["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", record["level"])
	}
	if record["msg"] != "restore started" {
		t.Errorf("msg = %v, want restore started", record["msg"])
	}
	if record["component"] != "runner" {
		t.Errorf("component = %v, want runner", record["component"])
	}
}
