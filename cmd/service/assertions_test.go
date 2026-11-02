package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestResponseAssertions(t *testing.T) {
	cases := []struct {
		name, method, path, body string
		status                   int
		out                      apiResp
		want                     string
	}{
		{"valid add", "POST", "/todos", `{"title":" !pay rent "}`, 201, apiResp{Todo: &todo{ID: 1, Title: "!pay rent", Priority: "high"}}, "passed"},
		{"semantic error despite success", "POST", "/todos", `{"title":"!pay rent"}`, 201, apiResp{Todo: &todo{ID: 1, Title: "!pay rent", Priority: "normal"}}, "failed"},
		{"expected rejection", "POST", "/todos", `{"title":" "}`, 422, apiResp{Error: "title required"}, "passed"},
		{"invalid accepted", "POST", "/todos", `{"title":" "}`, 200, apiResp{}, "failed"},
		{"empty list", "GET", "/todos", "", 200, apiResp{}, "passed"},
		{"error in successful list", "GET", "/todos", "", 200, apiResp{Error: "read failed"}, "failed"},
		{"not found", "POST", "/todos/7/complete", "", 404, apiResp{Error: "not found"}, "passed"},
		{"conflicting not found", "POST", "/todos/7/complete", "", 404, apiResp{Error: "not found", Todo: &todo{ID: 7, Title: "x", Priority: "normal", Done: true}}, "failed"},
		{"wrong list shape", "GET", "/todos", "", 200, apiResp{Todos: []todo{{ID: 1, Title: "x", Priority: "corrupted"}}}, "failed"},
		{"complete", "POST", "/todos/7/complete", "", 200, apiResp{Todo: &todo{ID: 7, Title: "x", Priority: "normal", Done: true}}, "passed"},
		{"complete must be done", "POST", "/todos/7/complete", "", 200, apiResp{Todo: &todo{ID: 7, Title: "x", Priority: "normal"}}, "failed"},
		{"delete", "DELETE", "/todos/7", "", 200, apiResp{Extra: map[string]any{"deleted": "7"}}, "passed"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, why := checkResponse(tt.method, tt.path, []byte(tt.body), tt.status, tt.out)
			if got != tt.want {
				t.Fatalf("%s: %s", got, why)
			}
		})
	}
}
func TestCompleteIsIdempotent(t *testing.T) {
	r := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: r.Addr()})
	defer client.Close()
	s := svc{rdb: client}
	ctx := context.Background()
	td := todo{ID: 7, Title: "x", Priority: "normal", Done: true}
	b, _ := json.Marshal(td)
	client.HSet(ctx, "todos:test", "7", b)
	for i := 0; i < 2; i++ {
		status, out, err := s.storeLogic(ctx, hop{tenant: "test"}, httptest.NewRequest("POST", "/todos/7/complete", nil), nil)
		if err != nil || status != 200 || out.Todo == nil || !out.Todo.Done {
			t.Fatalf("%d %+v %v", status, out, err)
		}
	}
}
func TestBehaviorSettingsChangeIdentity(t *testing.T) {
	original := cfg{config: "default", timeoutMS: 400}
	base := effectiveConfig(original, "binary1")
	changed := original
	changed.latencyMS = 150
	if effectiveConfig(changed, "binary1") == base {
		t.Fatal("latency omitted")
	}
	changed = original
	changed.corrupt = true
	if effectiveConfig(changed, "binary1") == base {
		t.Fatal("corruption omitted")
	}
	if effectiveConfig(original, "binary2") == base {
		t.Fatal("artifact omitted")
	}
}
func TestNoAutomaticRetryOfTimedOutWrite(t *testing.T) {
	var calls atomic.Int32
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(60 * time.Millisecond)
		w.Write([]byte(`{"todos":[]}`))
	}))
	defer downstream.Close()
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(routeResp{Instance: "store1", Addr: downstream.URL})
	}))
	defer controller.Close()
	s := svc{c: cfg{controller: controller.URL, next: "store", timeoutMS: 10}, client: downstream.Client()}
	_, _, retries, err := s.forward(context.Background(), hop{}, httptest.NewRequest("POST", "/todos", strings.NewReader(`{"title":"x"}`)), []byte(`{"title":"x"}`))
	if err == nil || retries != 0 || calls.Load() != 1 {
		t.Fatalf("calls=%d retries=%d err=%v", calls.Load(), retries, err)
	}
}
