package tmdb

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"golang.org/x/time/rate"
)

var (
	r14Input  = flag.String("r14-input", "", "absolute path to the R14-A UTF-8 input list")
	r14Output = flag.String("r14-output", "", "absolute path to a new private R14-A JSON report")
	r14Root   = flag.String("r14-root", "", "optional media source root for parent fallback")
)

const r14FixtureKey = "r14-offline-fixture-key"

type r14Expected struct {
	CleanTitle string    `json:"clean_title,omitempty"`
	Year       *int      `json:"year,omitempty"`
	MediaType  MediaType `json:"media_type,omitempty"`
	TMDBID     int       `json:"tmdb_id,omitempty"`
}

type r14Scenario struct {
	Input    string      `json:"input"`
	Expected r14Expected `json:"expected"`
}

type r14Request struct {
	Endpoint string `json:"endpoint"`
	Language string `json:"language"`
	Query    string `json:"query,omitempty"`
	YearKey  string `json:"year_parameter,omitempty"`
	Year     string `json:"url_year,omitempty"`
	IMDBID   string `json:"imdb_id,omitempty"`
	Reason   string `json:"reason"`
}

type r14Result struct {
	Number int `json:"scenario"`
	r14Scenario
	Parsed      ParsedFile   `json:"parsed"`
	ParseStatus string       `json:"parse_status"`
	Anomalies   []string     `json:"anomalies,omitempty"`
	PlanStatus  string       `json:"plan_status"`
	Identity    string       `json:"identity_status"`
	Outcome     string       `json:"offline_outcome"`
	TypedHTTP   int          `json:"typed_search_http"`
	Requests    []r14Request `json:"requests"`
}

func readR14List(ctx context.Context, input string) ([]r14Scenario, error) {
	if input == "" {
		return nil, errors.New("вкажіть вхідний список через -r14-input")
	}
	file, err := os.Open(input)
	if err != nil {
		return nil, errors.New("не вдалося відкрити вхідний список")
	}
	defer file.Close()
	buffer := bufio.NewReader(file)
	if prefix, _ := buffer.Peek(3); string(prefix) == "\ufeff" {
		_, _ = buffer.Discard(3)
	}
	reader := csv.NewReader(buffer)
	reader.Comma = '\t'
	reader.FieldsPerRecord = -1
	var scenarios []r14Scenario
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fields, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("запис %d: пошкоджений TSV", len(scenarios)+1)
		}
		line, _ := reader.FieldPos(0)
		for _, field := range fields {
			if !utf8.ValidString(field) || strings.ContainsAny(field, "\x00\r\n") {
				return nil, fmt.Errorf("рядок %d: потрібен коректний однорядковий UTF-8", line)
			}
		}
		if strings.TrimSpace(fields[0]) == "" || len(fields) > 5 {
			return nil, fmt.Errorf("рядок %d: потрібні назва або шлях і до чотирьох полів оракула", line)
		}
		var row [5]string
		copy(row[:], fields)
		expected := r14Expected{CleanTitle: row[1], MediaType: MediaType(row[3])}
		if row[2] != "" {
			year, err := strconv.Atoi(row[2])
			if err != nil || year < 0 {
				return nil, fmt.Errorf("рядок %d: некоректний очікуваний рік", line)
			}
			expected.Year = &year
		}
		if expected.MediaType != "" && expected.MediaType != MediaTypeMovie && expected.MediaType != MediaTypeTV {
			return nil, fmt.Errorf("рядок %d: очікуваний тип має бути movie або tv", line)
		}
		if row[4] != "" {
			id, err := strconv.Atoi(row[4])
			if err != nil || id <= 0 {
				return nil, fmt.Errorf("рядок %d: очікуваний TMDB ID має бути додатним числом", line)
			}
			expected.TMDBID = id
		}
		scenarios = append(scenarios, r14Scenario{Input: row[0], Expected: expected})
	}
	if len(scenarios) == 0 {
		return nil, errors.New("вхідний список порожній")
	}
	return scenarios, nil
}

func (r *r14Result) checkParse() {
	r.ParseStatus = "UNKNOWN"
	compare := func(field string, matches bool) {
		if !matches {
			r.ParseStatus = "PARSE_DEFECT"
			r.Anomalies = append(r.Anomalies, field+"_differs_from_oracle")
		} else if r.ParseStatus == "UNKNOWN" {
			r.ParseStatus = "PARSE_OK"
		}
	}
	if r.Expected.CleanTitle != "" {
		compare("clean_title", r.Parsed.CleanTitle == r.Expected.CleanTitle)
	}
	if r.Expected.Year != nil {
		compare("year", r.Parsed.Year == *r.Expected.Year)
	}
	if r.Expected.MediaType != "" {
		compare("media_type", r.Parsed.MediaType == r.Expected.MediaType)
	}
	if r.Parsed.CleanTitle == "" {
		r.Anomalies = append(r.Anomalies, "empty_clean_title")
	}
	if strings.ContainsFunc(r.Parsed.CleanTitle, isCyrillic) && strings.ContainsFunc(r.Parsed.CleanTitle, func(ch rune) bool { return unicode.Is(unicode.Latin, ch) }) {
		r.Anomalies = append(r.Anomalies, "mixed_scripts_in_clean_title")
	}
}

func (r *r14Result) record(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	values := req.URL.Query()
	if req.Method != http.MethodGet || req.URL.Scheme != "https" || req.URL.Host != "api.themoviedb.org" || values.Get("api_key") != r14FixtureKey {
		return nil, errors.New("запит поза межами офлайн-фікстури")
	}
	request := r14Request{Endpoint: req.URL.Path, Language: values.Get("language"), Reason: "initial_request"}
	body := `{"page":1,"results":[],"total_pages":0,"total_results":0}`
	switch request.Endpoint {
	case "/3/search/movie", "/3/search/tv":
		request.Query = values.Get("query")
		for _, key := range []string{"year", "first_air_date_year"} {
			if values.Has(key) {
				request.YearKey, request.Year = key, values.Get(key)
			}
		}
		r.TypedHTTP++
	default:
		if request.Endpoint != "/3/find/"+r.Parsed.IMDBID || r.Parsed.IMDBID == "" || values.Get("external_source") != "imdb_id" {
			return nil, errors.New("непередбачений endpoint офлайн-діагностики")
		}
		request.IMDBID = r.Parsed.IMDBID
		body = `{"movie_results":[],"tv_results":[]}`
	}
	if len(r.Requests) > 0 {
		prior := r.Requests[len(r.Requests)-1]
		request.Reason = "previous_search_empty"
		if prior.IMDBID != "" {
			request.Reason = "imdb_find_empty; title_search"
		} else {
			if request.Query != prior.Query {
				request.Reason += "; query_changed"
			}
			if request.Endpoint != prior.Endpoint {
				request.Reason += "; endpoint_changed"
			}
			if request.Year != prior.Year {
				request.Reason += "; year_filter_changed"
			}
			if request.Language != prior.Language {
				request.Reason += "; language_changed"
			}
		}
	}
	r.Requests = append(r.Requests, request)
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func diagnoseR14(ctx context.Context, scenarios []r14Scenario, root string) ([]r14Result, error) {
	results := make([]r14Result, 0, len(scenarios))
	for i, scenario := range scenarios {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result := r14Result{
			Number: i + 1, r14Scenario: scenario, Parsed: ParseFilename(scenario.Input),
			PlanStatus: "SEARCH_PLAN", Identity: "NEEDS_LIVE_VERIFICATION", Requests: []r14Request{},
		}
		result.checkParse()
		client := &Client{
			client: &http.Client{Transport: searchFixtureTransport(result.record)},
			apiKey: r14FixtureKey, rateLimiter: rate.NewLimiter(rate.Inf, 1), mediaRoot: root,
		}
		info, err := client.FetchFromParsed(ctx, result.Parsed, scenario.Input)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil || info != nil {
			return nil, fmt.Errorf("сценарій %d: неочікуваний результат офлайн-каскаду", result.Number)
		}
		result.Outcome = "empty_fixture_cascade_exhausted"
		if result.TypedHTTP == 0 {
			result.Outcome = "no_typed_search"
		}
		results = append(results, result)
	}
	return results, nil
}

func TestR14FilenameReport(t *testing.T) {
	if *r14Input == "" && *r14Output == "" && *r14Root == "" {
		t.Skip("opt-in R14-A report: use -r14-input and -r14-output")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	defer slog.SetDefault(logger)

	scenarios, err := readR14List(ctx, *r14Input)
	if err != nil {
		t.Fatal(err)
	}
	if *r14Output == "" {
		t.Fatal("вкажіть новий локальний файл звіту через -r14-output")
	}
	results, err := diagnoseR14(ctx, scenarios, *r14Root)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(*r14Output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal("не вдалося створити звіт, перевірте шлях і чи файл уже існує")
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	writeErr := encoder.Encode(results)
	if closeErr := file.Close(); writeErr != nil || closeErr != nil {
		t.Fatal("не вдалося завершити запис звіту")
	}
}

func TestR14OfflineDiagnostic(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	root := filepath.Join(dir, "Медіатека")
	input := filepath.Join(root, "TT1234567", "Зоря.S01E02.2021.mkv")
	list := "\ufeff\"" + input + "\"\tЗоря\t2021\ttv\t42\r\n" +
		"Signal.2024.mkv\tSignal\t2023\tmovie\n" + "Plain.mkv\n"
	if err := os.WriteFile("list.tsv", []byte(list), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	scenarios, err := readR14List(ctx, "list.tsv")
	if err != nil {
		t.Fatal(err)
	}
	results, err := diagnoseR14(ctx, scenarios, root)
	if err != nil {
		t.Fatal(err)
	}
	first := results[0]
	wantParsed := ParsedFile{OriginalName: "Зоря.S01E02.2021", CleanTitle: "Зоря", Year: 2021,
		MediaType: MediaTypeTV, ParentDir: "TT1234567", IMDBID: "tt1234567", TitleLang: TitleLangCyrillic}
	if first.Input != input || first.Parsed != wantParsed || first.ParseStatus != "PARSE_OK" ||
		first.Expected.TMDBID != 42 || first.Identity != "NEEDS_LIVE_VERIFICATION" || first.PlanStatus != "SEARCH_PLAN" {
		t.Fatalf("unexpected Cyrillic/IMDb scenario: %+v", first)
	}
	wantPrefix := []r14Request{
		{Endpoint: "/3/find/tt1234567", Language: "uk-UA", IMDBID: "tt1234567", Reason: "initial_request"},
		{Endpoint: "/3/search/tv", Language: "uk-UA", Query: "Зоря", YearKey: "first_air_date_year", Year: "2021", Reason: "imdb_find_empty; title_search"},
		{Endpoint: "/3/search/tv", Language: "ru-RU", Query: "Зоря", YearKey: "first_air_date_year", Year: "2021", Reason: "previous_search_empty; language_changed"},
	}
	if len(first.Requests) != first.TypedHTTP+1 || len(first.Requests) < len(wantPrefix) || !reflect.DeepEqual(first.Requests[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("unexpected recorded HTTP sequence: %+v", first.Requests)
	}
	if results[1].ParseStatus != "PARSE_DEFECT" || !reflect.DeepEqual(results[1].Anomalies, []string{"year_differs_from_oracle"}) || results[2].ParseStatus != "UNKNOWN" {
		t.Fatalf("unexpected oracle statuses: %+v", results)
	}
	report, err := json.Marshal(results)
	if err != nil || strings.Contains(string(report), r14FixtureKey) || strings.Contains(string(report), "api_key") {
		t.Fatal("report encoding failed or included an API key")
	}
	// No App/SQLite dependencies are initialized, and every HTTP request is answered in memory.
	// An empty working directory apart from the list catches accidental local DB or poster creation.
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "list.tsv" {
		t.Fatalf("unexpected files after offline run: %v, %v", entries, err)
	}
	for _, endpoint := range []*url.URL{
		{Scheme: "https", Host: "image.tmdb.org", Path: "/poster.jpg"},
		{Scheme: "https", Host: "api.themoviedb.org", Path: "/3/movie/42"},
	} {
		endpoint.RawQuery = "api_key=" + r14FixtureKey
		req := &http.Request{Method: http.MethodGet, URL: endpoint}
		if _, err := first.record(req); err == nil {
			t.Fatal("transport accepted a poster or details request")
		}
	}
}

func TestR14BadInput(t *testing.T) {
	for _, input := range []struct{ name, data, message string }{
		{"missing option", "", "вкажіть вхідний список"},
		{"missing file", "", "не вдалося відкрити"},
		{"empty", "\r\n", "список порожній"},
		{"damaged TSV", "\"missing quote\n", "пошкоджений TSV"},
		{"invalid UTF-8", "\xff.mkv\n", "коректний однорядковий UTF-8"},
		{"bad oracle", "Title.mkv\tTitle\tunknown\n", "некоректний очікуваний рік"},
	} {
		t.Run(input.name, func(t *testing.T) {
			var path string
			if input.name != "missing option" {
				path = filepath.Join(t.TempDir(), "list.tsv")
				if input.name != "missing file" {
					if err := os.WriteFile(path, []byte(input.data), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := readR14List(context.Background(), path); err == nil || !strings.Contains(err.Error(), input.message) {
				t.Fatalf("unexpected input error: %v", err)
			}
		})
	}
}

func TestR14Cancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := "Signal.2024.mkv"
	result := r14Result{Parsed: ParseFilename(input)}
	client := &Client{
		apiKey: r14FixtureKey, rateLimiter: rate.NewLimiter(rate.Inf, 1),
		client: &http.Client{Transport: searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
			response, err := result.record(req)
			cancel()
			return response, err
		})},
	}
	if _, err := client.FetchFromParsed(ctx, result.Parsed, input); !errors.Is(err, context.Canceled) || result.TypedHTTP != 1 {
		t.Fatalf("cancellation after first HTTP: count=%d, err=%v", result.TypedHTTP, err)
	}
	if results, err := diagnoseR14(ctx, []r14Scenario{{Input: input}}, ""); !errors.Is(err, context.Canceled) || results != nil {
		t.Fatalf("canceled report: results=%v, err=%v", results, err)
	}
}
