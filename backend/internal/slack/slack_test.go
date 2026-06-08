package slack

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/patchpilot/backend/internal/api"
)

func TestFingerprintStableAndChanges(t *testing.T) {
	a := []api.Problem{{ID: "error:svc", Occurrences: 5}, {ID: "performance:svc", Occurrences: 3}}
	b := []api.Problem{{ID: "performance:svc", Occurrences: 3}, {ID: "error:svc", Occurrences: 5}}
	if fingerprint(a) != fingerprint(b) {
		t.Fatal("fingerprint should be order-independent")
	}
	// Crossing an occurrence bucket changes the fingerprint.
	c := []api.Problem{{ID: "error:svc", Occurrences: 500}, {ID: "performance:svc", Occurrences: 3}}
	if fingerprint(a) == fingerprint(c) {
		t.Fatal("fingerprint should change when a bug's volume bucket changes")
	}
}

func TestSyncPostsOnceThenDedupes(t *testing.T) {
	var posts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&posts, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := New(srv.URL)
	probs := []api.Problem{{ID: "error:svc", Title: "boom", Occurrences: 5}}

	if err := n.Sync(probs); err != nil {
		t.Fatal(err)
	}
	if err := n.Sync(probs); err != nil { // unchanged → no post
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&posts); got != 1 {
		t.Fatalf("expected 1 post for unchanged set, got %d", got)
	}

	// A new bug appears → another post.
	probs = append(probs, api.Problem{ID: "performance:svc", Title: "slow", Occurrences: 4})
	if err := n.Sync(probs); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&posts); got != 2 {
		t.Fatalf("expected 2 posts after the bug set changed, got %d", got)
	}
}

func TestDisabledNotifierDoesNotPost(t *testing.T) {
	n := New("") // non-nil, but disabled with no webhook
	if n == nil {
		t.Fatal("New should never return nil")
	}
	if st := n.Status(); st.Enabled || st.Configured {
		t.Fatalf("New(\"\") should be disabled+unconfigured, got %+v", st)
	}
	// Sync is a no-op when there's no webhook to post to.
	if err := n.Sync([]api.Problem{{ID: "error:svc", Occurrences: 1}}); err != nil {
		t.Fatalf("Sync on an unconfigured notifier should be a no-op, got %v", err)
	}
	if err := n.Test(); err == nil {
		t.Fatal("Test() should error when no webhook is configured")
	}
}

func TestSetConfigEnablesThenDisables(t *testing.T) {
	var posts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&posts, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := New("") // start disabled
	n.SetConfig(true, srv.URL)
	if st := n.Status(); !st.Enabled || !st.Configured {
		t.Fatalf("expected enabled+configured after SetConfig, got %+v", st)
	}
	if err := n.Sync([]api.Problem{{ID: "error:svc", Occurrences: 5}}); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&posts); got != 1 {
		t.Fatalf("expected 1 post after enabling, got %d", got)
	}

	// Toggling off (empty webhook keeps the secret) stops posting on a changed set.
	n.SetConfig(false, "")
	if st := n.Status(); st.Enabled || !st.Configured {
		t.Fatalf("expected disabled but still configured, got %+v", st)
	}
	if err := n.Sync([]api.Problem{{ID: "error:svc", Occurrences: 500}}); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&posts); got != 1 {
		t.Fatalf("expected no new post while disabled, got %d", got)
	}
}
