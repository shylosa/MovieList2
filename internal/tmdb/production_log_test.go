package tmdb

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ptn "github.com/razsteinmetz/go-ptn"
	"golang.org/x/time/rate"
)

func TestProductionReleaseParsing(t *testing.T) {
	raw, err := ptn.Parse("Lovelace.(2013).BDRip-AVC.andre90.mkv")
	if err != nil || raw == nil || raw.IsMovie {
		t.Fatalf("expected reproduction of go-ptn release/episode confusion: %+v %v", raw, err)
	}
	for _, tt := range []struct {
		file, title string
		kind        MediaType
		year        int
	}{
		{"Lovelace.(2013).BDRip-AVC.andre90.mkv", "Lovelace", MediaTypeMovie, 2013},
		{"Prakticheskaya.magiya.1998.BDRip.TRIPLE.mkv", "Prakticheskaya magiya", MediaTypeMovie, 1998},
		{"Prakticheskaya.magiya.1998.TRIPLE.BDRip.XviD.AC3.-HQCLUB.avi", "Prakticheskaya magiya", MediaTypeMovie, 1998},
		{"Triple.2016.BDRip.mkv", "Triple", MediaTypeMovie, 2016},
		{"Series.S01.2020.WEBRip.mkv", "Series", MediaTypeTV, 2020},
		{"Series.S01E01.2020.WEBRip.mkv", "Series", MediaTypeTV, 2020},
		{"Series.2020.WEBRip.S01E01.mkv", "Series", MediaTypeTV, 2020},
	} {
		t.Run(tt.file, func(t *testing.T) {
			got := ParseFilename(tt.file)
			if got.CleanTitle != tt.title || got.Year != tt.year || got.MediaType != tt.kind {
				t.Fatalf("parsed=%+v", got)
			}
			for _, candidate := range generateTitleCandidates(got.CleanTitle, tt.file) {
				if strings.Contains(candidate, "andre90") || strings.Contains(candidate, "()") || (tt.title != "Triple" && strings.Contains(candidate, "TRIPLE")) {
					t.Fatalf("polluted candidate: %q", candidate)
				}
			}
		})
	}
	for _, title := range []string{"Lovelace - andre90", "Lovelace () - andre90"} {
		for _, candidate := range generateTitleCandidates(title, "Lovelace.(2013).BDRip-AVC.andre90.mkv") {
			if candidate != "Lovelace" {
				t.Fatalf("polluted parsed title: %q", candidate)
			}
		}
	}
	for _, title := range []string{"The Triple", "Triple Triple"} {
		if got := cleanReleaseTitle(title, strings.ReplaceAll(title, " ", ".")+".2016.BDRip.TRIPLE.mkv"); got != title {
			t.Fatalf("genuine title word removed: %q => %q", title, got)
		}
	}
}

func TestFolderFallbackDoesNotRepeatBetweenCascades(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	defer slog.SetDefault(previous)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[]}`)
	}))
	defer server.Close()
	c := &Client{client: &http.Client{Transport: &candidateDetailsTransport{server.URL}}, rateLimiter: rate.NewLimiter(rate.Inf, 1)}
	parsed := ParsedFile{CleanTitle: "Другая история", ParentDir: "Вместе до конца", MediaType: MediaTypeMovie, TitleLang: TitleLangCyrillic}
	if _, err := c.runPipeline(context.Background(), parsed, "Другая история 8.WEB-DLRip.avi"); err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(output.String(), `"msg":"folder_en_fallback"`); count != 1 {
		t.Fatalf("expected one folder fallback, got %d: %s", count, output.String())
	}
	ctx := withSearchAttempts(context.Background())
	for _, tt := range []struct {
		query  string
		qy, ty int
		kind   MediaType
	}{
		{"Вместе до конца", 0, 0, MediaTypeMovie},
		{"Вместе до конца", 2020, 2020, MediaTypeMovie},
		{"Вместе до конца", 0, 2020, MediaTypeMovie},
		{"Вместе до конца", 0, 0, MediaTypeTV},
		{"Vmeste do konca", 0, 0, MediaTypeMovie},
	} {
		if !claimSearchAttempt(ctx, tt.query, tt.qy, tt.ty, tt.kind) || claimSearchAttempt(ctx, tt.query, tt.qy, tt.ty, tt.kind) {
			t.Fatalf("attempt identity lost: %+v", tt)
		}
	}
}
