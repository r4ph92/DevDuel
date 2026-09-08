package store_test

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/r4ph92/DevDuel/internal/id"
	"github.com/r4ph92/DevDuel/internal/store"
	"github.com/r4ph92/DevDuel/internal/store/storetest"
)

func TestAppendEventNumbersFromOne(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()
	m := seed(t, db)

	for want := int64(1); want <= 3; want++ {
		got, err := db.AppendEvent(ctx, m.id, "match.started", nil)
		if err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
		if got != want {
			t.Errorf("AppendEvent returned seq %d, want %d", got, want)
		}
	}
}

// The allocator is the reason this method exists. Reading the current maximum
// and adding one hands the same number to two concurrent writers, and a hole
// in the sequence leaves a reconnecting client waiting for an event nobody
// ever wrote.
func TestAppendEventIsGaplessUnderConcurrency(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()
	m := seed(t, db)

	const writers = 8
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		seqs []int64
	)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()

			seq, err := db.AppendEvent(ctx, m.id, "judge.finished", nil)
			if err != nil {
				t.Errorf("writer %d: %v", i, err)
				return
			}

			mu.Lock()
			defer mu.Unlock()
			seqs = append(seqs, seq)
		}()
	}
	wg.Wait()

	if len(seqs) != writers {
		t.Fatalf("%d writers appended, want %d", len(seqs), writers)
	}

	seen := make(map[int64]bool, len(seqs))
	for _, seq := range seqs {
		if seq < 1 || seq > writers {
			t.Errorf("seq %d is outside 1..%d", seq, writers)
		}
		if seen[seq] {
			t.Errorf("seq %d was handed out twice", seq)
		}
		seen[seq] = true
	}
}

func TestEventsAfterReturnsWhatFollowsInOrder(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()
	m := seed(t, db)

	kinds := []string{"match.started", "judge.queued", "judge.finished"}
	for _, kind := range kinds {
		if _, err := db.AppendEvent(ctx, m.id, kind, nil); err != nil {
			t.Fatalf("AppendEvent(%s): %v", kind, err)
		}
	}

	// Seq 0 is what a client that has seen nothing asks for.
	all, err := db.EventsAfter(ctx, m.id, 0)
	if err != nil {
		t.Fatalf("EventsAfter: %v", err)
	}
	if len(all) != len(kinds) {
		t.Fatalf("EventsAfter(0) returned %d events, want %d", len(all), len(kinds))
	}
	for i, e := range all {
		if e.Type != kinds[i] {
			t.Errorf("event %d is %q, want %q", i, e.Type, kinds[i])
		}
		if e.Seq != int64(i+1) {
			t.Errorf("event %d has seq %d, want %d", i, e.Seq, i+1)
		}
		if e.CreatedAt.IsZero() {
			t.Errorf("event %d has no created_at", i)
		}
	}

	rest, err := db.EventsAfter(ctx, m.id, 2)
	if err != nil {
		t.Fatalf("EventsAfter: %v", err)
	}
	if len(rest) != 1 || rest[0].Type != "judge.finished" {
		t.Errorf("EventsAfter(2) = %v, want only the last event", rest)
	}

	caught, err := db.EventsAfter(ctx, m.id, 3)
	if err != nil {
		t.Fatalf("EventsAfter: %v", err)
	}
	if len(caught) != 0 {
		t.Errorf("EventsAfter(3) returned %d events, want none", len(caught))
	}
}

func TestAppendEventCarriesItsPayload(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	ctx := t.Context()
	m := seed(t, db)

	want := json.RawMessage(`{"requirement":"health","status":"pass"}`)
	if _, err := db.AppendEvent(ctx, m.id, "judge.result", want); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	// An event with nothing to say still has to read back as an object,
	// rather than as null or as an empty string.
	if _, err := db.AppendEvent(ctx, m.id, "match.ended", nil); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	events, err := db.EventsAfter(ctx, m.id, 0)
	if err != nil {
		t.Fatalf("EventsAfter: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("read back %d events, want 2", len(events))
	}

	var got map[string]string
	if err := json.Unmarshal(events[0].Payload, &got); err != nil {
		t.Fatalf("unmarshal payload %q: %v", events[0].Payload, err)
	}
	if got["requirement"] != "health" || got["status"] != "pass" {
		t.Errorf("payload = %v, want the one that was appended", got)
	}

	var empty map[string]string
	if err := json.Unmarshal(events[1].Payload, &empty); err != nil {
		t.Fatalf("unmarshal empty payload %q: %v", events[1].Payload, err)
	}
	if len(empty) != 0 {
		t.Errorf("payload = %v, want an empty object", empty)
	}
}

func TestAppendEventToAMatchThatIsNotThere(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)

	_, err := db.AppendEvent(t.Context(), id.New(), "match.started", nil)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("AppendEvent = %v, want ErrNotFound", err)
	}
}
