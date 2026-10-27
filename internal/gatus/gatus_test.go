package gatus

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

const sample = `[
 {"name":"b","group":"web","results":[{"success":true},{"success":false,"errors":["timeout"],"conditionResults":[{"condition":"[STATUS] == 200","success":false},{"condition":"[BODY] == ok","success":true}]}]},
 {"name":"a","group":"web","results":[{"success":true}]},
 {"name":"z","group":"","results":[]}
]`

func TestParse(t *testing.T) {
	eps, n, err := Parse([]byte(sample))
	if err != nil || n != 3 || len(eps) != 3 {
		t.Fatal(err, n, eps)
	}
	b := eps[0]
	if b.Success || !b.HasData || len(b.Failures) != 2 || b.Failures[0] != "[STATUS] == 200" || b.Failures[1] != "timeout" {
		t.Fatalf("%+v", b)
	}
	if !eps[1].Success || eps[2].HasData {
		t.Fatalf("%+v %+v", eps[1], eps[2])
	}
	if _, _, err := Parse([]byte("{")); err == nil {
		t.Fatal("expected error")
	}
}

func TestStatusesSortedAndErrors(t *testing.T) {
	code := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/endpoints/statuses" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.WriteHeader(code)
		io.WriteString(w, sample)
	}))
	defer srv.Close()
	c := New(srv.URL, nil)
	eps, err := c.Statuses(context.Background())
	if err != nil || eps[0].Group != "" || eps[1].Name != "a" || eps[2].Name != "b" {
		t.Fatalf("%v %+v", err, eps)
	}
	code = 500
	if _, err := c.Statuses(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}
