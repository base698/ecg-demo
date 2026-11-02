// loadgen is synthetic traffic: it drives the todo app through the same
// ingress the browser uses, as a handful of tenants. Some tenants are canary
// tenants (eligible to hit unverified candidates) and some are not — the
// controller decides that from X-Tenant, exactly as for real users.
//
// The operation mix is deliberately skewed so the rare path families
// (invalid adds, slow-store retries) fill their obligations last.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

type todo struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Done  bool   `json:"done"`
}

type resp struct {
	Todos []todo `json:"todos"`
	Todo  *todo  `json:"todo"`
	Error string `json:"error"`
	Ecg   struct {
		Path, Outcome, Scenario string
		Eligible                bool
	} `json:"ecg"`
}

var titles = []string{"buy milk", "ship the demo", "write tests", "call mom", "!pay rent", "water plants",
	"fix the shed door", "!renew insurance", "read OMSCS lecture", "smoke a brisket", "plan trip"}

func main() {
	base := env("INGRESS", "http://controller:9000") + "/app/api"
	rps, _ := strconv.ParseFloat(env("RPS", "8"), 64)
	tenants := strings.Split(env("TENANTS", "acme,globex,initech,umbrella,hooli,stark"), ",")
	chaosRate, _ := strconv.ParseFloat(env("CHAOS_RATE", "0.15"), 64)
	quiet := os.Getenv("QUIET") != ""
	client := &http.Client{Timeout: 10 * time.Second}

	log.Printf("loadgen: %.1f rps against %s, tenants=%v", rps, base, tenants)
	st := &tstate{ids: map[string][]int64{}}
	for range time.Tick(time.Duration(float64(time.Second) / rps)) {
		go func() {
			tenant := tenants[rand.Intn(len(tenants))]
			op := pickOp()
			// Keep lists small so complete/delete stay reachable.
			if st.size(tenant) > 6 && (op == "add" || op == "list") {
				op = "delete"
			}
			// Chaos (slow store → timeout → retry) is only injected on list so the
			// rare path family stays rare but reachable.
			chaos := op == "list" && rand.Float64() < chaosRate
			var r *resp
			var err error
			switch op {
			case "list":
				r, err = call(client, base, tenant, chaos, "GET", "/todos", nil)
				if r != nil {
					st.set(tenant, r.Todos)
				}
			case "add":
				r, err = call(client, base, tenant, chaos, "POST", "/todos", map[string]string{"title": titles[rand.Intn(len(titles))]})
				if r != nil && r.Todo != nil {
					st.add(tenant, r.Todo.ID)
				}
			case "add-invalid":
				r, err = call(client, base, tenant, chaos, "POST", "/todos", map[string]string{"title": "   "})
			case "complete", "delete":
				id, ok := st.pick(tenant, op == "delete")
				if !ok {
					r, err = call(client, base, tenant, chaos, "POST", "/todos", map[string]string{"title": titles[rand.Intn(len(titles))]})
					if r != nil && r.Todo != nil {
						st.add(tenant, r.Todo.ID)
					}
					break
				}
				if op == "complete" {
					r, err = call(client, base, tenant, chaos, "POST", fmt.Sprintf("/todos/%d/complete", id), nil)
				} else {
					r, err = call(client, base, tenant, chaos, "DELETE", fmt.Sprintf("/todos/%d", id), nil)
				}
			}
			if err != nil {
				log.Printf("%-8s %-11s error: %v", tenant, op, err)
				return
			}
			if !quiet && r != nil {
				log.Printf("%-8s %-11s elig=%-5t %-8s %s", tenant, r.Ecg.Scenario, r.Ecg.Eligible, r.Ecg.Outcome, r.Ecg.Path)
			}
		}()
	}
}

// tstate tracks the todo ids loadgen believes each tenant has.
type tstate struct {
	mu  sync.Mutex
	ids map[string][]int64
}

func (t *tstate) size(tenant string) int { t.mu.Lock(); defer t.mu.Unlock(); return len(t.ids[tenant]) }
func (t *tstate) add(tenant string, id int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ids[tenant] = append(t.ids[tenant], id)
}
func (t *tstate) set(tenant string, todos []todo) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ids := make([]int64, 0, len(todos))
	for _, td := range todos {
		ids = append(ids, td.ID)
	}
	t.ids[tenant] = ids
}
func (t *tstate) pick(tenant string, remove bool) (int64, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ids := t.ids[tenant]
	if len(ids) == 0 {
		return 0, false
	}
	i := rand.Intn(len(ids))
	id := ids[i]
	if remove {
		t.ids[tenant] = append(ids[:i], ids[i+1:]...)
	}
	return id, true
}

func pickOp() string {
	x := rand.Float64()
	switch {
	case x < 0.38:
		return "list"
	case x < 0.60:
		return "add"
	case x < 0.70:
		return "add-invalid"
	case x < 0.88:
		return "complete"
	default:
		return "delete"
	}
}

func call(client *http.Client, base, tenant string, chaos bool, method, path string, body any) (*resp, error) {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, base+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant", tenant)
	req.Header.Set("X-Ecg-Source", "synthetic")
	if chaos {
		req.Header.Set("X-Ecg-Chaos", "slow-store")
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var r resp
	json.NewDecoder(res.Body).Decode(&r)
	if res.StatusCode >= 500 {
		return &r, fmt.Errorf("%d %s (%s)", res.StatusCode, r.Error, r.Ecg.Path)
	}
	return &r, nil
}
