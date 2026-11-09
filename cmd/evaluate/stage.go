package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nathanfredericks/transactions/internal/learn"
	"github.com/nathanfredericks/transactions/internal/llm"
)

var (
	caseSet   = flag.String("set", "dev", "Case set to score: dev or holdout")
	modelList = flag.String("models", "openai/gpt-6-luna,anthropic/claude-haiku-5.5,google/gemini-3.8-flash", "Comma-separated OpenRouter model IDs")
	reference = flag.String("reference", "google/gemini-3.1-flash-lite", "Current production model, scored once with default reasoning (\"\" to skip)")
	effort    = flag.String("effort", "medium", "Reasoning effort for candidate models")
	runs      = flag.Int("runs", 3, "Runs per candidate model")
	capUSD    = flag.Float64("cap", 15, "Stop when this many dollars have been spent on the key during this command")
	workers   = flag.Int("workers", 6, "Concurrent requests")
	estimate  = flag.Bool("estimate", false, "Print the cost estimate and exit")
	endpoint  = flag.String("endpoint", "https://openrouter.ai/api/v1", "OpenRouter API base URL")
	tag       = flag.String("tag", "", "Suffix for result files, to keep prompt versions apart")
)

type price struct{ in, out float64 }

// prices are USD per token, read from OpenRouter's public model list.
func prices(ids []string) (map[string]price, error) {
	resp, err := http.Get("https://openrouter.ai/api/v1/models")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Data []struct {
			ID      string `json:"id"`
			Pricing struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	out := map[string]price{}
	for _, m := range body.Data {
		var p price
		fmt.Sscan(m.Pricing.Prompt, &p.in)
		fmt.Sscan(m.Pricing.Completion, &p.out)
		out[m.ID] = p
	}
	for _, id := range ids {
		if _, ok := out[id]; !ok {
			return nil, fmt.Errorf("model %s is not listed on OpenRouter", id)
		}
	}
	return out, nil
}

// spent returns the key's all-time usage in dollars.
func spent(key string) (float64, error) {
	req, _ := http.NewRequest(http.MethodGet, *endpoint+"/key", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("key check: status %d", resp.StatusCode)
	}
	var body struct {
		Data struct {
			Usage float64 `json:"usage"`
		} `json:"data"`
	}
	err = json.NewDecoder(resp.Body).Decode(&body)
	return body.Data.Usage, err
}

type call struct {
	Model   string            `json:"model"`
	Effort  string            `json:"effort"`
	Run     int               `json:"run"`
	Case    string            `json:"case"`
	Output  learn.ModelOutput `json:"output"`
	Error   string            `json:"error,omitempty"`
	Latency float64           `json:"latencyMs"`
	In      int64             `json:"inputTokens"`
	Out     int64             `json:"outputTokens"`
}

func (c call) key() string { return fmt.Sprintf("%s|%s|%d|%s", c.Model, c.Effort, c.Run, c.Case) }

type job struct {
	model, effort string
	run           int
	c             Case
}

func models(_ *learn.Mirror, _ []learn.Example, out string) {
	var cases []Case
	b, err := os.ReadFile(filepath.Join(out, "cases-"+*caseSet+".json"))
	if err != nil {
		log.Fatal(err)
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		log.Fatal(err)
	}
	candidates := strings.Split(*modelList, ",")
	all := append([]string(nil), candidates...)
	if *reference != "" {
		all = append(all, *reference)
	}
	pr, err := prices(all)
	if err != nil {
		log.Fatal(err)
	}
	var jobs []job
	for r := 1; r <= *runs; r++ {
		for _, m := range candidates {
			for _, c := range cases {
				jobs = append(jobs, job{m, *effort, r, c})
			}
		}
	}
	if *reference != "" {
		for _, c := range cases {
			jobs = append(jobs, job{*reference, "", 1, c})
		}
	}
	// Estimate: prompt characters / 3.5 tokens in; 1,500 tokens out with
	// reasoning, 400 without.
	var est float64
	for _, j := range jobs {
		ins, msg, _ := learn.Prompt(*j.c.Input)
		in := float64(len(ins)+len(msg)) / 3.5
		o := 1500.0
		if j.effort == "" {
			o = 400
		}
		est += in*pr[j.model].in + o*pr[j.model].out
	}
	fmt.Printf("%d cases, %d calls, estimated $%.2f (cap $%.2f)\n", len(cases), len(jobs), est, *capUSD)
	if *estimate {
		return
	}
	key := os.Getenv("OPENROUTER_EVAL_KEY")
	if key == "" {
		log.Fatal("OPENROUTER_EVAL_KEY is not set")
	}
	start, err := spent(key)
	if err != nil {
		log.Fatal(err)
	}
	resultsPath := filepath.Join(out, "calls-"+*caseSet+*tag+".jsonl")
	done := map[string]bool{}
	if f, err := os.Open(resultsPath); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var c call
			if json.Unmarshal(sc.Bytes(), &c) == nil {
				done[c.key()] = true
			}
		}
		f.Close()
	}
	f, err := os.OpenFile(resultsPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		log.Fatal(err)
	}
	var mu sync.Mutex
	stop := false
	completed := 0
	queue := make(chan job)
	var wg sync.WaitGroup
	for w := 0; w < *workers; w++ {
		wg.Go(func() {
			for j := range queue {
				ins, msg, schema := learn.Prompt(*j.c.Input)
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				res, err := llm.Call(ctx, llm.Config{Endpoint: *endpoint, Key: key, Model: j.model, Effort: j.effort, Timeout: 90 * time.Second}, ins, msg, "transaction_choice", schema)
				cancel()
				c := call{Model: j.model, Effort: j.effort, Run: j.run, Case: j.c.ID, Latency: float64(res.Latency.Milliseconds()), In: res.InputTokens, Out: res.OutputTokens}
				if err == nil {
					c.Output, err = learn.ParseOutput(res.Text, *j.c.Input)
				}
				if err != nil {
					c.Error = err.Error()
				}
				line, _ := json.Marshal(c)
				mu.Lock()
				f.Write(append(line, '\n'))
				completed++
				check := completed%20 == 0
				mu.Unlock()
				if check {
					if now, err := spent(key); err == nil {
						fmt.Printf("%d calls, $%.2f spent\n", completed, now-start)
						if now-start >= *capUSD*0.95 {
							mu.Lock()
							stop = true
							mu.Unlock()
						}
					}
				}
			}
		})
	}
	for _, j := range jobs {
		mu.Lock()
		s := stop
		mu.Unlock()
		if s {
			fmt.Println("spend cap reached; stopping")
			break
		}
		if done[call{Model: j.model, Effort: j.effort, Run: j.run, Case: j.c.ID}.key()] {
			continue
		}
		queue <- j
	}
	close(queue)
	wg.Wait()
	f.Close()
	if now, err := spent(key); err == nil {
		fmt.Printf("done: $%.2f spent this command\n", now-start)
	}
	score(cases, resultsPath, pr, out)
}

type agg struct {
	calls, invalid                   int
	payeeOK, newOK, newN, falseMatch int
	missed, reachN, nameClose, catOK int
	catNewOK, catNewN                int
	cost                             float64
	latencies                        []float64
	correct                          map[string][]bool
}

func score(cases []Case, path string, pr map[string]price, out string) {
	byID := map[string]Case{}
	for _, c := range cases {
		byID[c.ID] = c
	}
	f, err := os.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	stats := map[string]*agg{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var c call
		if json.Unmarshal(sc.Bytes(), &c) != nil {
			continue
		}
		cs, ok := byID[c.Case]
		if !ok {
			continue
		}
		name := c.Model
		if c.Effort != "" {
			name += " (" + c.Effort + ")"
		}
		a := stats[name]
		if a == nil {
			a = &agg{correct: map[string][]bool{}}
			stats[name] = a
		}
		a.calls++
		a.cost += float64(c.In)*pr[c.Model].in + float64(c.Out)*pr[c.Model].out
		a.latencies = append(a.latencies, c.Latency)
		ok = false
		if c.Error != "" {
			a.invalid++
		} else {
			switch {
			case !cs.Seen:
				a.newN++
				if c.Output.PayeeID == "none" {
					a.newOK++
					ok = true
					if learn.Similar(c.Output.NewPayeeName, cs.LabelName) {
						a.nameClose++
					}
				} else {
					a.falseMatch++
				}
				a.catNewN++
				if c.Output.CategoryID == cs.LabelCat {
					a.catNewOK++
				}
			case cs.Reachable:
				a.reachN++
				if c.Output.PayeeID == cs.Label {
					ok = true
				} else if c.Output.PayeeID == "none" {
					a.missed++
				}
			}
			if c.Output.CategoryID == cs.LabelCat {
				a.catOK++
			}
		}
		if ok {
			a.payeeOK++
		}
		a.correct[c.Case] = append(a.correct[c.Case], ok)
	}
	names := make([]string, 0, len(stats))
	for n := range stats {
		names = append(names, n)
	}
	sort.Strings(names)
	unreachable := 0
	for _, c := range cases {
		if c.Seen && !c.Reachable {
			unreachable++
		}
	}
	var w strings.Builder
	fmt.Fprintf(&w, "# Model comparison (%s set, %d cases)\n\n", *caseSet, len(cases))
	fmt.Fprintf(&w, "A new merchant (no payee yet) is answered correctly by \"none\". %d cases had an existing payee that retrieval didn't offer; no model can get those right, and they count as wrong for every model.\n\n", unreachable)
	fmt.Fprintf(&w, "| Model | Calls | Payee correct | New merchant: said none | Wrong existing payee for a new merchant | Known merchant offered: said none | New name close to yours | Category (all) | Category (new merchants) | Invalid | p95 latency | Cost |\n|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, n := range names {
		a := stats[n]
		sort.Float64s(a.latencies)
		p95 := 0.0
		if len(a.latencies) > 0 {
			p95 = a.latencies[int(math.Ceil(0.95*float64(len(a.latencies))))-1] / 1000
		}
		fmt.Fprintf(&w, "| %s | %d | %s | %s | %d | %d of %d | %d of %d | %s | %s | %d | %.1fs | $%.2f |\n", n, a.calls, pct(learn.Wilson(a.payeeOK, a.calls)), pct(learn.Wilson(a.newOK, a.newN)), a.falseMatch, a.missed, a.reachN, a.nameClose, a.newOK, pct(learn.Wilson(a.catOK, a.calls-a.invalid)), pct(learn.Wilson(a.catNewOK, a.catNewN)), a.invalid, p95, a.cost)
	}
	fmt.Fprintf(&w, "\n## Run-to-run agreement and paired tests (per-case majority over runs)\n\n")
	maj := map[string]map[string]bool{}
	for _, n := range names {
		a := stats[n]
		agree, total := 0, 0
		maj[n] = map[string]bool{}
		for id, rs := range a.correct {
			k := 0
			for _, r := range rs {
				if r {
					k++
				}
			}
			maj[n][id] = 2*k > len(rs)
			if len(rs) > 1 {
				total++
				if k == 0 || k == len(rs) {
					agree++
				}
			}
		}
		if total > 0 {
			fmt.Fprintf(&w, "- %s: same payee outcome on every run for %d of %d cases\n", n, agree, total)
		}
	}
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			b, c := 0, 0
			for id := range byID {
				x, y := maj[names[i]][id], maj[names[j]][id]
				if x && !y {
					b++
				} else if y && !x {
					c++
				}
			}
			fmt.Fprintf(&w, "- %s vs %s: only first right %d, only second right %d, McNemar p=%.3f\n", names[i], names[j], b, c, learn.McNemar(b, c))
		}
	}
	write(out, "models-"+*caseSet+*tag+".md", w.String())
	fmt.Print(w.String())
}
