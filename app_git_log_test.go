package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestGitFailureLogsCauseWithoutCredentials(t *testing.T) {
	for _, command := range []string{"rev-parse", "add", "commit", "push"} {
		t.Run(command, func(t *testing.T) {
			a, _ := newTestAppDB(t, nil)
			var output bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
			defer slog.SetDefault(previous)
			var ui string
			a.eventEmitter = func(_ context.Context, name string, args ...interface{}) {
				if name == "log-message" {
					ui += args[0].(string)
				}
			}
			a.gitRunner = func(_ context.Context, _, _ string, args ...string) ([]byte, error) {
				if args[0] != command {
					return nil, nil
				}
				return []byte("To https://user:private-password@github.com/example/repo.git?token=private-query\n! [rejected] main -> main (non-fast-forward)\nAuthorization: Bearer private-header\npassword=private-value\nghp_privateToken\nC:\\private\\repo"), errors.New("exit status 1")
			}
			if command == "rev-parse" {
				_, _ = a.gitRepoRoot()
			} else {
				_ = a.deployToGitHubPagesIn(t.TempDir())
			}
			var event map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &event); err != nil {
				t.Fatal(err)
			}
			if event["msg"] != "git_command_failed" || event["command"] != command || event["level"] != "ERROR" || !strings.Contains(event["output"].(string), "non-fast-forward") {
				t.Fatalf("missing Git failure cause: %v", event)
			}
			for _, secret := range []string{"private-password", "private-query", "private-header", "private-value", "ghp_privateToken", `C:\private\repo`} {
				if strings.Contains(output.String(), secret) || strings.Contains(ui, secret) {
					t.Fatalf("diagnostics exposed %q", secret)
				}
			}
		})
	}
}

func TestGitUnchangedCommitIsInformationalAndStillPushes(t *testing.T) {
	a, _ := newTestAppDB(t, nil)
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	defer slog.SetDefault(previous)
	pushed := false
	a.gitRunner = func(_ context.Context, _, _ string, args ...string) ([]byte, error) {
		if args[0] == "commit" {
			return []byte("nothing to commit, working tree clean"), errors.New("exit status 1")
		}
		if args[0] == "push" {
			pushed = true
		}
		return nil, nil
	}
	if err := a.deployToGitHubPagesIn(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &event); err != nil {
		t.Fatal(err)
	}
	if !pushed || event["msg"] != "git_commit_no_changes" || event["level"] != "INFO" {
		t.Fatalf("unchanged commit handling: %v, pushed=%v", event, pushed)
	}
}
