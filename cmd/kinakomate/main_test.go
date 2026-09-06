package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestRun_CommandErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing command", args: nil},
		{name: "unknown command", args: []string{"unknown"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := run(context.Background(), tt.args); err == nil {
				t.Fatal("run succeeded, want command error")
			}
		})
	}
}

func TestRun_HelpSucceeds(t *testing.T) {
	if err := run(context.Background(), []string{"help"}); err != nil {
		t.Fatalf("run help returned error: %v", err)
	}
}

func TestLogCommandErrorWritesStructuredJSON(t *testing.T) {
	var output bytes.Buffer
	logCommandError(&output, errors.New("unknown command: nope"))

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("command error is not JSON: %v; output=%q", err, output.String())
	}
	if record["msg"] != "kinakomate command failed" {
		t.Fatalf("msg = %#v, want structured command message", record["msg"])
	}
	if record["error"] != "unknown command: nope" {
		t.Fatalf("error = %#v, want command error", record["error"])
	}
	if bytes.Contains(output.Bytes(), []byte("kinakomate: unknown")) {
		t.Fatalf("output contains legacy plain-text error: %q", output.String())
	}
}
