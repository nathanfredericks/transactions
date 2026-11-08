package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/nathanfredericks/transactions/internal/learn"
)

func TestSyncMirrorUsesDeltasAfterTheFirstFetch(t *testing.T) {
	var queries []string
	round := 0
	setupService(t, func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Path+"?"+r.URL.RawQuery)
		switch {
		case strings.HasSuffix(r.URL.Path, "/scheduled_transactions"):
			fmt.Fprintf(w, `{"data":{"scheduled_transactions":[],"server_knowledge":%d}}`, 10+round)
		case strings.HasSuffix(r.URL.Path, "/transactions"):
			if round == 0 {
				fmt.Fprint(w, `{"data":{"transactions":[{"id":"t1","date":"2026-01-01","amount":-1000,"approved":true,"payee_id":"p1","subtransactions":[]}],"server_knowledge":10}}`)
			} else {
				fmt.Fprint(w, `{"data":{"transactions":[{"id":"t1","date":"2026-01-01","amount":-1000,"approved":true,"deleted":true}],"server_knowledge":11}}`)
			}
		case strings.HasSuffix(r.URL.Path, "/payees"):
			fmt.Fprintf(w, `{"data":{"payees":[{"id":"p1","name":"Shop"}],"server_knowledge":%d}}`, 10+round)
		case strings.HasSuffix(r.URL.Path, "/categories"):
			if round == 0 {
				fmt.Fprint(w, `{"data":{"category_groups":[{"name":"Frequent","categories":[{"id":"c1","name":"Food","category_group_id":"g1"}]}],"server_knowledge":10}}`)
			} else {
				fmt.Fprint(w, `{"data":{"category_groups":[{"name":"Events","categories":[{"id":"c2","name":"Trip 2026","category_group_id":"g2"}]}],"server_knowledge":11}}`)
			}
		default:
			http.NotFound(w, r)
		}
	})
	m := learn.NewMirror()
	changed, err := SyncMirror(context.Background(), m, "2026-02-01")
	if err != nil || len(changed) != 1 || m.Knowledge["transactions"] != 10 {
		t.Fatalf("first: %v %v %+v", changed, err, m.Knowledge)
	}
	if !strings.Contains(queries[0], "since_date=") || strings.Contains(queries[0], "last_knowledge") {
		t.Fatalf("the first fetch is bounded by date: %s", queries[0])
	}
	if m.Categories["c1"].GroupName != "Frequent" || m.CategorySeen["c1"] != "2000-01-01" {
		t.Fatalf("categories present at the first sync count as long existing: %+v %v", m.Categories["c1"], m.CategorySeen)
	}
	round, queries = 1, nil
	if _, err := SyncMirror(context.Background(), m, "2026-02-02"); err != nil {
		t.Fatal(err)
	}
	for _, q := range queries {
		if !strings.Contains(q, "last_knowledge_of_server=10") || strings.Contains(q, "since_date") {
			t.Fatalf("later syncs are deltas: %s", q)
		}
	}
	if !m.Transactions["t1"].Deleted || m.Knowledge["transactions"] != 11 {
		t.Fatal("a deleted transaction must be kept, marked deleted")
	}
	if m.CategorySeen["c2"] != "2026-02-02" {
		t.Fatalf("a new category is dated when first seen: %v", m.CategorySeen)
	}
}
