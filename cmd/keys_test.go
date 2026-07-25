package cmd

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/grovetools/flow/pkg/tui/view"
)

func TestKeysCommandJSONMatchesHostedKeys(t *testing.T) {
	cmd := NewKeysCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	var got view.HostedKeyReference
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, out.String())
	}
	want := view.HostedKeys()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON output differs from view.HostedKeys()\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestWriteHostedKeysHumanStable(t *testing.T) {
	ref := view.HostedKeyReference{
		SchemaVersion: 1,
		App:           "flow",
		Bindings: []view.HostedKeyBinding{
			{
				Scope:          "view-host",
				Action:         "finish_plan",
				Keys:           []string{"ctrl+f"},
				Description:    "open Finish Plan",
				HostSwallowed:  true,
				CollisionHints: []string{"treemux.toggle_fullscreen"},
			},
			{
				Scope:       "status/Navigation",
				Action:      "move_up",
				Keys:        []string{"k", "up"},
				Description: "move up",
				ConfigKey:   "up",
			},
		},
	}

	var out bytes.Buffer
	if err := writeHostedKeysHuman(&out, ref); err != nil {
		t.Fatalf("writeHostedKeysHuman() error = %v", err)
	}
	const want = `flow hosted keys (schema 1)
- view-host/finish_plan
  keys: ctrl+f
  description: open Finish Plan
  config-key: -
  host-swallowed: true
  collision-hints: treemux.toggle_fullscreen
- status/Navigation/move_up
  keys: k, up
  description: move up
  config-key: up
  host-swallowed: false
  collision-hints: -
`
	if out.String() != want {
		t.Fatalf("human output changed\ngot:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestKeysCommandRejectsArguments(t *testing.T) {
	cmd := NewKeysCmd()
	cmd.SetArgs([]string{"extra"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want argument rejection")
	}
}
