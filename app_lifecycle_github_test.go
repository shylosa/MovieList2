package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"movielist-app/internal/config"
	"movielist-app/internal/storage"
	"movielist-app/internal/tmdb"
)

func newTestAppDB(t *testing.T, movies []storage.Movie) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.New(filepath.Join(dir, "movies.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.InitSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(movies) > 0 {
		if err := db.SaveMoviesBatch(context.Background(), movies); err != nil {
			t.Fatal(err)
		}
	}
	a := NewApp()
	a.ctx = context.Background()
	a.db = db
	a.cfg = &config.Config{AppVersion: "test", HTMLPath: filepath.Join(dir, "local.html"), GitHubPagesBranch: "pages-test"}
	a.eventEmitter = func(context.Context, string, ...interface{}) {}
	return a, dir
}

func TestFinalizeScanAlwaysUsesLifecycleContext(t *testing.T) {
	a, dir := newTestAppDB(t, []storage.Movie{{Filename: "Enemy.mkv", TmdbID: 181886, TitleEN: "Enemy"}})
	var events []string
	a.eventEmitter = func(_ context.Context, name string, _ ...interface{}) { events = append(events, name) }

	a.finalizeScan("cancelled", false)
	content, err := os.ReadFile(filepath.Join(dir, "local.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "Enemy") {
		t.Fatal("finalize generated an empty showcase instead of reading the lifecycle DB")
	}
	if got := a.db.GetState(context.Background(), "last_scan_at"); got != "" {
		t.Fatalf("failed finalize changed last_scan_at: value=%q", got)
	}
	finished := 0
	for _, event := range events {
		if event == "scan-finished" {
			finished++
		}
	}
	if finished != 1 {
		t.Fatalf("events = %v; want one scan-finished", events)
	}

	a.finalizeScan("success", true)
	if got := a.db.GetState(context.Background(), "last_scan_at"); got == "" {
		t.Fatal("successful finalize did not set last_scan_at")
	}
}

func TestRunScanPersistsNewMovieBeforePosterCleanup(t *testing.T) {
	a, dir := newTestAppDB(t, nil)
	mediaDir := filepath.Join(dir, "media")
	postersDir := filepath.Join(dir, "posters")
	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(mediaDir, "Dune.2021.mkv")
	if err := os.WriteFile(path, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.cfg.MediaFolderPath = mediaDir
	a.cfg.PostersDir = postersDir
	a.diskFileScanner = func(context.Context) ([]string, error) { return []string{path}, nil }
	client := tmdb.NewClient(a.cfg)
	defer client.Close()
	client.SetTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"page":1,"results":[]}`
		contentType := "application/json"
		switch {
		case r.URL.Host == "image.tmdb.org":
			body, contentType = "poster", "image/jpeg"
		case strings.Contains(r.URL.Path, "/search/movie"):
			body = `{"page":1,"results":[{"id":438631,"title":"Дюна","original_title":"Dune","release_date":"2021-09-15","original_language":"en","popularity":100}]}`
		case strings.Contains(r.URL.Path, "/movie/438631"):
			body = `{"id":438631,"title":"Дюна","original_title":"Dune","release_date":"2021-09-15","overview":"Український опис","poster_path":"/dune.jpg","genres":[],"credits":{"cast":[]}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(bytes.NewBufferString(body)), Request: r}, nil
	}))
	a.tmdbClient = client
	a.RunScan()
	a.wg.Wait()
	movie, err := a.db.GetMovieByFilename(context.Background(), "Dune.2021.mkv")
	if err != nil || movie == nil || movie.TmdbID != 438631 {
		t.Fatalf("movie not persisted before cleanup: movie=%+v err=%v", movie, err)
	}
	if movie.LocalPosterPath == "" {
		t.Fatal("poster path was not persisted")
	}
	if _, err := os.Stat(movie.LocalPosterPath); err != nil {
		t.Fatalf("poster removed by cleanup: %v", err)
	}
}

func TestRunScanDiskErrorDoesNotCleanPosters(t *testing.T) {
	a, dir := newTestAppDB(t, nil)
	postersDir := filepath.Join(dir, "posters")
	if err := os.MkdirAll(postersDir, 0o755); err != nil {
		t.Fatal(err)
	}
	poster := filepath.Join(postersDir, "keep.jpg")
	if err := os.WriteFile(poster, []byte("poster"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.cfg.PostersDir = postersDir
	a.diskFileScanner = func(context.Context) ([]string, error) { return nil, errors.New("disk unavailable") }
	a.RunScan()
	a.wg.Wait()
	if _, err := os.Stat(poster); err != nil {
		t.Fatalf("disk error triggered poster cleanup: %v", err)
	}
}

func TestSyncToGitHubEmptyDBDoesNotRunGit(t *testing.T) {
	a, _ := newTestAppDB(t, nil)
	called := false
	a.gitRunner = func(context.Context, string, string, ...string) ([]byte, error) {
		called = true
		return nil, nil
	}
	var payload map[string]interface{}
	a.eventEmitter = func(_ context.Context, name string, data ...interface{}) {
		if name == "github-sync-finished" && len(data) == 1 {
			payload, _ = data[0].(map[string]interface{})
		}
	}
	a.SyncToGitHub()
	a.wg.Wait()
	if called || payload == nil || payload["success"] != false {
		t.Fatalf("gitCalled=%v payload=%#v; want no git and failure event", called, payload)
	}
}

func TestSyncToGitHubMissingRepository(t *testing.T) {
	a, _ := newTestAppDB(t, []storage.Movie{{Filename: "Enemy.mkv", TmdbID: 181886}})
	a.gitRunner = func(context.Context, string, string, ...string) ([]byte, error) {
		return []byte("not a git repository"), errors.New("exit status 128")
	}
	var payload map[string]interface{}
	a.eventEmitter = func(_ context.Context, name string, data ...interface{}) {
		if name == "github-sync-finished" && len(data) == 1 {
			payload, _ = data[0].(map[string]interface{})
		}
	}
	a.SyncToGitHub()
	a.wg.Wait()
	if payload == nil || payload["success"] != false || !strings.Contains(payload["message"].(string), "not a git repository") {
		t.Fatalf("payload=%#v; want repository failure event", payload)
	}
}

func TestSyncToGitHubCommandOrderAndMobileOutput(t *testing.T) {
	a, _ := newTestAppDB(t, []storage.Movie{{Filename: "Enemy.mkv", TmdbID: 181886, TitleEN: "Enemy", PosterURL: "https://image.example/enemy.jpg", LocalPosterPath: "posters/local.jpg"}})
	repo := t.TempDir()
	var calls []string
	a.gitRunner = func(_ context.Context, dir, name string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		if len(args) >= 2 && args[0] == "rev-parse" {
			return []byte(repo + "\n"), nil
		}
		if dir != repo {
			t.Fatalf("git command dir = %q; want %q", dir, repo)
		}
		return nil, nil
	}
	success, msg := a.syncToGitHub()
	if !success {
		t.Fatal(msg)
	}
	want := []string{"git rev-parse --show-toplevel", "git add -f index.html", "git commit -m Update mobile showcase", "git push origin pages-test"}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v", calls)
	}
	for i := range want {
		if !strings.HasPrefix(calls[i], want[i]) {
			t.Fatalf("call[%d] = %q; want prefix %q", i, calls[i], want[i])
		}
	}
	content, err := os.ReadFile(filepath.Join(repo, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	html := string(content)
	if !strings.Contains(html, "https://image.example/enemy.jpg") || strings.Contains(html, "posters/local.jpg") {
		t.Fatal("mobile showcase did not use the TMDB CDN poster URL")
	}
}

func TestDeployToGitHubPagesCommitFailureStillPushes(t *testing.T) {
	a, _ := newTestAppDB(t, nil)
	var calls []string
	logged := false
	a.eventEmitter = func(_ context.Context, name string, _ ...interface{}) {
		if name == "log-message" {
			logged = true
		}
	}
	a.gitRunner = func(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		if len(args) > 0 && args[0] == "commit" {
			return []byte("nothing to commit"), errors.New("exit status 1")
		}
		return nil, nil
	}
	if err := a.deployToGitHubPagesIn(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || !strings.HasPrefix(calls[2], "git push origin pages-test") {
		t.Fatalf("push was not attempted after commit failure: %v", calls)
	}
	if !logged {
		t.Fatal("commit failure was not logged")
	}
}

func TestDeployToGitHubPagesPushFailure(t *testing.T) {
	a, _ := newTestAppDB(t, nil)
	a.gitRunner = func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "push" {
			return []byte("rejected"), errors.New("exit status 1")
		}
		return nil, nil
	}
	if err := a.deployToGitHubPagesIn(t.TempDir()); err == nil || !strings.Contains(err.Error(), "push failed") {
		t.Fatalf("error = %v; want push failed", err)
	}
}

func TestSyncToGitHubEventsWaitGroupAndDuplicateGuard(t *testing.T) {
	a, _ := newTestAppDB(t, []storage.Movie{{Filename: "Enemy.mkv", TmdbID: 181886, TitleEN: "Enemy"}})
	repo := t.TempDir()
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	a.gitRunner = func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "rev-parse" {
			once.Do(func() { close(entered); <-release })
			return []byte(repo + "\n"), nil
		}
		return nil, nil
	}
	var mu sync.Mutex
	var events []string
	a.eventEmitter = func(_ context.Context, name string, _ ...interface{}) {
		mu.Lock()
		events = append(events, name)
		mu.Unlock()
	}
	a.SyncToGitHub()
	<-entered
	a.SyncToGitHub()
	close(release)
	a.wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	started, finished := 0, 0
	for _, event := range events {
		if event == "github-sync-started" {
			started++
		}
		if event == "github-sync-finished" {
			finished++
		}
	}
	if started != 1 || finished != 1 {
		t.Fatalf("events=%v; want one started and one finished", events)
	}
}

func TestSyncToGitHubPushFailureEmitsFailure(t *testing.T) {
	a, _ := newTestAppDB(t, []storage.Movie{{Filename: "Enemy.mkv", TmdbID: 181886}})
	repo := t.TempDir()
	a.gitRunner = func(_ context.Context, _ string, _ string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "rev-parse" {
			return []byte(repo), nil
		}
		if len(args) > 0 && args[0] == "push" {
			return []byte("rejected"), errors.New("exit status 1")
		}
		return nil, nil
	}
	var payload map[string]interface{}
	a.eventEmitter = func(_ context.Context, name string, data ...interface{}) {
		if name == "github-sync-finished" && len(data) == 1 {
			payload, _ = data[0].(map[string]interface{})
		}
	}
	a.SyncToGitHub()
	a.wg.Wait()
	if payload == nil || payload["success"] != false {
		t.Fatalf("finished payload = %#v; want success=false", payload)
	}
}

func TestRunScanDiskErrorPreservesShowcaseAndState(t *testing.T) {
	a, dir := newTestAppDB(t, []storage.Movie{{Filename: "Enemy.mkv", TmdbID: 181886, TitleEN: "Enemy"}})
	a.diskFileScanner = func(context.Context) ([]string, error) { return nil, errors.New("disk unavailable") }
	var mu sync.Mutex
	var events []string
	a.eventEmitter = func(_ context.Context, name string, _ ...interface{}) {
		mu.Lock()
		events = append(events, name)
		mu.Unlock()
	}
	a.RunScan()
	a.wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	assertFailedScanFinalization(t, a, dir, events)
}

func TestRunScanCancellationPreservesShowcaseAndState(t *testing.T) {
	a, dir := newTestAppDB(t, []storage.Movie{{Filename: "Enemy.mkv", TmdbID: 181886, TitleEN: "Enemy"}})
	entered := make(chan struct{})
	a.diskFileScanner = func(ctx context.Context) ([]string, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	var mu sync.Mutex
	var events []string
	a.eventEmitter = func(_ context.Context, name string, _ ...interface{}) {
		mu.Lock()
		events = append(events, name)
		mu.Unlock()
	}
	a.RunScan()
	<-entered
	a.StopScan()
	a.wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	assertFailedScanFinalization(t, a, dir, events)
}

func TestRunScanNoChangesUsesCurrentDatabase(t *testing.T) {
	a, dir := newTestAppDB(t, []storage.Movie{{Filename: "Enemy.mkv", TmdbID: 181886, TitleEN: "Enemy"}})
	a.cfg.MediaFolderPath = dir
	path := filepath.Join(dir, "Enemy.mkv")
	a.diskFileScanner = func(context.Context) ([]string, error) { return []string{path}, nil }
	a.RunScan()
	a.wg.Wait()
	content, err := os.ReadFile(a.cfg.HTMLPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "Enemy") {
		t.Fatal("no-change scan did not generate showcase from current database")
	}
	if got := a.db.GetState(context.Background(), "last_scan_at"); got == "" {
		t.Fatal("successful no-change scan did not set last_scan_at")
	}
}

func assertFailedScanFinalization(t *testing.T, a *App, dir string, events []string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(dir, "local.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "Enemy") {
		t.Fatal("failed scan replaced showcase with an empty collection")
	}
	if got := a.db.GetState(context.Background(), "last_scan_at"); got != "" {
		t.Fatalf("failed scan changed last_scan_at to %q", got)
	}
	finished := 0
	for _, event := range events {
		if event == "scan-finished" {
			finished++
		}
	}
	if finished != 1 {
		t.Fatalf("events=%v; want exactly one scan-finished", events)
	}
}
