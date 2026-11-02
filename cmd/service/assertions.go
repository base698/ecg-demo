package main

import (
	"encoding/json"
	"strconv"
	"strings"
)

func effectiveConfig(c cfg, artifact string) string {
	b, _ := json.Marshal(struct {
		User, Artifact, Next         string
		FailRate                     float64
		LatencyMS, TimeoutMS, SlowMS int
		Corrupt                      bool
	}{c.config, artifact, c.next, c.failRate, c.latencyMS, c.timeoutMS, c.slowMS, c.corrupt})
	return string(b)
}

// Request-local response contracts only. These do not prove durable writes,
// isolation, authorization, or cross-request business invariants.
func checkResponse(method, path string, body []byte, status int, out apiResp) (string, string) {
	fail := func(reason string) (string, string) { return "failed", reason }
	if status >= 200 && status < 300 && out.Error != "" {
		return fail("successful response must not contain an error")
	}
	if status == 404 && (out.Todo != nil || len(out.Todos) != 0 || len(out.Extra) != 0) {
		return fail("not-found response must not also contain a result")
	}
	validTodo := func(t todo) bool {
		pri := "normal"
		if strings.HasPrefix(t.Title, "!") {
			pri = "high"
		}
		return t.ID > 0 && strings.TrimSpace(t.Title) != "" && len(t.Title) <= 80 && t.Priority == pri
	}
	switch {
	case method == "POST" && path == "/todos":
		var in struct{ Title string }
		json.Unmarshal(body, &in)
		title := strings.TrimSpace(in.Title)
		if title == "" || len(title) > 80 {
			if status != 422 || out.Error == "" || out.Todo != nil {
				return fail("invalid title must be rejected with 422")
			}
		} else if status != 201 || out.Todo == nil || out.Todo.Title != title || !validTodo(*out.Todo) {
			return fail("created todo must match the submitted title and priority")
		}
	case method == "GET" && path == "/todos":
		if status != 200 {
			return fail("list must return 200")
		}
		ids := map[int64]bool{}
		for _, t := range out.Todos {
			if !validTodo(t) || ids[t.ID] {
				return fail("list contains invalid or duplicate todos")
			}
			ids[t.ID] = true
		}
	case method == "POST" && strings.HasSuffix(path, "/complete"):
		if status == 404 && out.Error != "" {
			return "passed", "not-found response contract"
		}
		id, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(path, "/todos/"), "/complete"), 10, 64)
		if status != 200 || out.Todo == nil || out.Todo.ID != id || !out.Todo.Done || !validTodo(*out.Todo) {
			return fail("complete must return the requested todo marked done")
		}
	case method == "DELETE" && strings.HasPrefix(path, "/todos/"):
		if status == 404 && out.Error != "" {
			return "passed", "not-found response contract"
		}
		if status != 200 || out.Extra == nil || out.Extra["deleted"] != strings.TrimPrefix(path, "/todos/") {
			return fail("delete must acknowledge the requested id")
		}
	default:
		if status != 404 || out.Error == "" {
			return fail("unknown route must return 404")
		}
	}
	return "passed", "request-local response contract"
}
