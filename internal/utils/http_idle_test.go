package utils

import (
	"bytes"
	"context"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type logNotificationWriter struct{ records chan string }

func (w logNotificationWriter) Write(p []byte) (int, error) {
	w.records <- string(p)
	return len(p), nil
}

func TestTransportUnsolicitedIdle400DoesNotFailCompletedRequest(t *testing.T) {
	idle := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) != 1 {
			io.WriteString(w, "ok")
			return
		}
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		io.WriteString(buf, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
		buf.Flush()
		select {
		case <-idle:
		case <-r.Context().Done():
			return
		}
		io.WriteString(buf, "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		buf.Flush()
	}))
	defer server.Close()
	logs := make(chan string, 4)
	previous, flags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(standardLogBridge{logger: slog.New(slog.NewJSONHandler(logNotificationWriter{logs}, nil))})
	defer func() { log.SetOutput(previous); log.SetFlags(flags) }()
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	req, _ := http.NewRequest("GET", server.URL, nil)
	req = req.WithContext(httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{PutIdleConn: func(err error) {
		if err == nil {
			close(idle)
		}
	}}))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || string(body) != "ok" {
		t.Fatalf("completed request failed: %s %v", body, err)
	}
	select {
	case record := <-logs:
		if !strings.Contains(record, `"msg":"http_idle_unsolicited_response"`) || !strings.Contains(record, `"response_status":400`) {
			t.Fatal(record)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("idle response not reproduced")
	}
	resp, err = client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || calls.Load() != 2 {
		t.Fatal("transport failed to reconnect")
	}
}

func TestIdleDiagnosticOmitsRemotePayload(t *testing.T) {
	var buf bytes.Buffer
	w := standardLogBridge{logger: slog.New(slog.NewJSONHandler(&buf, nil))}
	w.Write([]byte(`Unsolicited response received on idle HTTP channel starting with "HTTP/1.1 400 Bad Request SECRET_REMOTE_CONTENT"; err=nil`))
	if strings.Contains(buf.String(), "SECRET_REMOTE_CONTENT") || !strings.Contains(buf.String(), `"provider":"unknown"`) {
		t.Fatal(buf.String())
	}
}
