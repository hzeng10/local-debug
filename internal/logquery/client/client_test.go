// Local addition (NOT vendored): tests for the HTTP contract ldbg depends on.
package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestQueryPostsFormAndDecodes(t *testing.T) {
	var gotQuery, gotLimit, gotOffset, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/select/logsql/query" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = r.ParseForm()
		gotQuery, gotLimit, gotOffset = r.Form.Get("query"), r.Form.Get("limit"), r.Form.Get("offset")
		gotCT = r.Header.Get("Content-Type")
		fmt.Fprintln(w, `{"_msg":"one","service":"orders"}`)
		fmt.Fprintln(w, `not json at all`)
		fmt.Fprintln(w, `{"_msg":"two","service":"orders"}`)
	}))
	defer srv.Close()

	logs, err := New(srv.URL, 5*time.Second).Query(context.Background(), `_time:1h service:="orders"`, "_time desc", 100, 10)
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery != `_time:1h service:="orders"` || gotLimit != "100" || gotOffset != "10" {
		t.Errorf("form = (%q, %q, %q)", gotQuery, gotLimit, gotOffset)
	}
	if gotCT != "application/x-www-form-urlencoded" {
		t.Errorf("content-type = %q", gotCT)
	}
	if len(logs) != 2 || logs[0]["_msg"] != "one" || logs[1]["_msg"] != "two" {
		t.Errorf("decoded %d records (malformed line must be skipped): %v", len(logs), logs)
	}
}

func TestQueryNon200ReturnsBodyError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad LogsQL near 'oops'", http.StatusBadRequest)
	}))
	defer srv.Close()

	_, err := New(srv.URL, 5*time.Second).Query(context.Background(), "oops", "", 0, 0)
	if err == nil || !strings.Contains(err.Error(), "bad LogsQL") {
		t.Fatalf("want body in error, got %v", err)
	}
}

func TestQuerySortPipeOnlyForNonDefault(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.Form.Get("query")
	}))
	defer srv.Close()
	cl := New(srv.URL, 5*time.Second)

	_, _ = cl.Query(context.Background(), "base", "_time desc", 0, 0)
	if got != "base" {
		t.Errorf("default sort must not append a pipe, got %q", got)
	}
	_, _ = cl.Query(context.Background(), "base", "_time asc", 0, 0)
	if got != "base | sort by (_time) asc" {
		t.Errorf("non-default sort must append a pipe, got %q", got)
	}
	_, _ = cl.Query(context.Background(), "base | stats count()", "_time asc", 0, 0)
	if got != "base | stats count()" {
		t.Errorf("piped query must never get a second pipe, got %q", got)
	}
}

func TestTailStopsOnContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl, _ := w.(http.Flusher)
		fmt.Fprintln(w, `{"_msg":"live"}`)
		if fl != nil {
			fl.Flush()
		}
		<-r.Context().Done() // block until the client goes away
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	received := make(chan struct{})
	var got []Log
	done := make(chan error, 1)
	go func() {
		done <- New(srv.URL, 0).Tail(ctx, "q", func(l Log) {
			got = append(got, l)
			close(received)
		})
	}()

	select {
	case <-received: // cancel only after the record reached the callback
	case <-time.After(5 * time.Second):
		t.Fatal("streamed record never reached the callback")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Tail did not return after context cancel")
	}
	if len(got) != 1 || got[0]["_msg"] != "live" {
		t.Errorf("want the one streamed record, got %v", got)
	}
}
