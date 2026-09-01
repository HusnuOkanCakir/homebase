package events

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/store"
)

func newRecorder(t *testing.T) *Recorder {
	t.Helper()

	s, err := store.Open(t.Context(), t.TempDir()+"/events.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	return NewRecorder(s.DB(), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestRecordsAndReadsBackAnEvent(t *testing.T) {
	r := newRecorder(t)
	ctx := t.Context()

	r.Warn(ctx, "application_unhealthy", "jellyfin", "health_check_failed",
		"Jellyfin is not responding.")

	list, err := r.List(ctx, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d events", len(list))
	}

	event := list[0]
	if event.Type != "application_unhealthy" {
		t.Errorf("type = %q", event.Type)
	}
	if event.Severity != SeverityWarning {
		t.Errorf("severity = %q", event.Severity)
	}
	if event.Subject == nil || *event.Subject != "jellyfin" {
		t.Errorf("subject = %v", event.Subject)
	}
	// The whole point of the reason field: a consumer branches on this rather
	// than on the wording of the message.
	if event.Reason == nil || *event.Reason != "health_check_failed" {
		t.Errorf("reason = %v", event.Reason)
	}
	if event.ID == "" {
		t.Error("no id")
	}
	if event.OccurredAt.IsZero() {
		t.Error("no timestamp")
	}
}

// null and false are different statements. "Was this recoverable?" is not a
// meaningful question about every event, and answering "no" where the honest
// answer is "not applicable" tells a user their photographs are unrecoverable.
func TestRecoverableDistinguishesUnknownFromFalse(t *testing.T) {
	r := newRecorder(t)
	ctx := t.Context()

	r.Info(ctx, "backup_completed", "weekly", "The weekly backup finished.")
	r.Error(ctx, "backup_failed", "weekly", "disk_full", "The backup disk is full.", true)

	list, err := r.List(ctx, Query{})
	if err != nil {
		t.Fatal(err)
	}

	byType := map[string]Event{}
	for _, event := range list {
		byType[event.Type] = event
	}

	if got := byType["backup_completed"]; got.Recoverable != nil {
		t.Errorf("an informational event claimed recoverability: %v", *got.Recoverable)
	}
	if got := byType["backup_failed"]; got.Recoverable == nil || !*got.Recoverable {
		t.Errorf("recoverable = %v, want true", got.Recoverable)
	}
}

func TestNewestFirst(t *testing.T) {
	r := newRecorder(t)
	ctx := t.Context()

	base := time.Now().UTC().Add(-time.Hour)
	for i, name := range []string{"first", "second", "third"} {
		r.Record(ctx, Event{
			Type: name, Severity: SeverityInfo,
			OccurredAt: base.Add(time.Duration(i) * time.Minute),
		})
	}

	list, err := r.List(ctx, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d events", len(list))
	}
	if list[0].Type != "third" || list[2].Type != "first" {
		t.Errorf("order = %s, %s, %s", list[0].Type, list[1].Type, list[2].Type)
	}
}

// `since` is what makes the live stream safe to be lossy: a client that missed
// events while its laptop was shut can ask for what it missed.
func TestSinceReturnsOnlyWhatCameAfter(t *testing.T) {
	r := newRecorder(t)
	ctx := t.Context()

	cutoff := time.Now().UTC()
	r.Record(ctx, Event{Type: "before", Severity: SeverityInfo,
		OccurredAt: cutoff.Add(-time.Minute)})
	r.Record(ctx, Event{Type: "after", Severity: SeverityInfo,
		OccurredAt: cutoff.Add(time.Minute)})

	list, err := r.List(ctx, Query{Since: cutoff})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Type != "after" {
		t.Fatalf("got %d events: %+v", len(list), list)
	}
}

func TestSeverityFilter(t *testing.T) {
	r := newRecorder(t)
	ctx := t.Context()

	r.Info(ctx, "one", "", "")
	r.Warn(ctx, "two", "", "", "")
	r.Error(ctx, "three", "", "", "", false)

	list, err := r.List(ctx, Query{Severity: SeverityError})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Type != "three" {
		t.Fatalf("got %+v", list)
	}
}

// An unknown severity is a caller error, not an empty result set. Returning
// nothing would look identical to "that has never happened on this machine".
func TestUnknownSeverityIsAnError(t *testing.T) {
	r := newRecorder(t)

	_, err := r.List(t.Context(), Query{Severity: Severity("catastrophic")})
	if err == nil {
		t.Fatal("an unknown severity returned no error")
	}
	if !strings.Contains(err.Error(), "catastrophic") {
		t.Errorf("the error does not say what was wrong: %v", err)
	}
}

func TestLimitIsCapped(t *testing.T) {
	r := newRecorder(t)
	ctx := t.Context()

	for i := 0; i < 5; i++ {
		r.Info(ctx, "thing", "", "")
	}

	list, err := r.List(ctx, Query{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Errorf("limit ignored: got %d", len(list))
	}

	// An absurd limit is clamped rather than refused: the caller gets data, and
	// the server does not read a million rows into memory because somebody typed
	// a large number.
	list, err = r.List(ctx, Query{Limit: 10_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 5 {
		t.Errorf("got %d", len(list))
	}
}

// An event with no type cannot be consumed by anything, so it is dropped rather
// than stored as an unqueryable row.
func TestAnEventWithNoTypeIsDropped(t *testing.T) {
	r := newRecorder(t)
	ctx := t.Context()

	r.Record(ctx, Event{Severity: SeverityInfo, Message: text("something happened")})

	list, err := r.List(ctx, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("a typeless event was stored: %+v", list)
	}
}

// --- Live subscription --------------------------------------------------------

func TestSubscriberReceivesEvents(t *testing.T) {
	r := newRecorder(t)
	ctx := t.Context()

	stream, unsubscribe := r.Subscribe()
	defer unsubscribe()

	r.Info(ctx, "application_started", "jellyfin", "Jellyfin is running.")

	select {
	case event := <-stream:
		if event.Type != "application_started" {
			t.Errorf("type = %q", event.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the subscriber received nothing")
	}
}

// The property that matters most about the stream: a subscriber that has stopped
// reading must not be able to hold up the operation that produced the event.
// Losing an event from a live stream is recoverable — the durable record is in
// the database. Blocking an install because a browser tab froze is not.
func TestASubscriberThatStoppedReadingDoesNotBlockRecording(t *testing.T) {
	r := newRecorder(t)
	ctx := t.Context()

	_, unsubscribe := r.Subscribe()
	defer unsubscribe()

	// Far more than the channel buffer, from the calling goroutine, with nothing
	// draining. If publish blocked, this would never return and the test would
	// time out rather than fail — which is itself the signal.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 500; i++ {
			r.Info(ctx, "noise", "", "")
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("recording blocked on a subscriber that was not reading")
	}

	// Every event is still durable, which is what makes the loss acceptable.
	list, err := r.List(ctx, Query{Limit: maxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 500 {
		t.Errorf("stored %d of 500 events", len(list))
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	r := newRecorder(t)

	stream, unsubscribe := r.Subscribe()
	if r.Subscribers() != 1 {
		t.Fatalf("subscribers = %d", r.Subscribers())
	}

	unsubscribe()

	if r.Subscribers() != 0 {
		t.Errorf("subscribers = %d after unsubscribing", r.Subscribers())
	}
	// The channel is closed, so a reader stops rather than blocking forever.
	if _, open := <-stream; open {
		t.Error("the channel was not closed")
	}

	// Unsubscribing twice must not panic — a handler's deferred cleanup can run
	// after the client already went away.
	unsubscribe()
}

func TestRecordingWithNoSubscribersIsFine(t *testing.T) {
	r := newRecorder(t)

	r.Info(context.Background(), "nobody_is_listening", "", "")

	list, err := r.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Errorf("got %d events", len(list))
	}
}

// The first question anybody asks of an audit log, and the one it could not
// answer. With one account "who" had one answer; there are roles, invitations
// and removals now, all of which are things one person does to another.
func TestAnEventRecordsWhoDidIt(t *testing.T) {
	recorder := newRecorder(t)

	recorder.Record(WithActor(context.Background(), "alex"), Event{
		Type: "account.created", Severity: SeverityWarning,
	})

	found, err := recorder.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Fatalf("recorded %d events", len(found))
	}
	if found[0].Actor == nil || *found[0].Actor != "alex" {
		t.Fatalf("the event says %v did it", found[0].Actor)
	}
}

// Null is not "unknown". A disk being unplugged, a scheduled backup, an update
// arriving — nothing did those on anybody's behalf, and putting a name there
// would be inventing one.
func TestSomethingNobodyDidHasNoActor(t *testing.T) {
	recorder := newRecorder(t)

	recorder.Record(context.Background(), Event{
		Type: "storage.disk_removed", Severity: SeverityWarning,
	})

	found, err := recorder.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if found[0].Actor != nil {
		t.Fatalf("an event nobody caused is attributed to %q", *found[0].Actor)
	}
}

// A caller that names an actor is recording something on somebody else's
// behalf, and knows better than the request does.
func TestAnExplicitActorIsNotOverwrittenByTheRequest(t *testing.T) {
	recorder := newRecorder(t)

	behalf := "father"
	recorder.Record(WithActor(context.Background(), "alex"), Event{
		Type: "account.claimed", Severity: SeverityInfo, Actor: &behalf,
	})

	found, err := recorder.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if found[0].Actor == nil || *found[0].Actor != "father" {
		t.Fatalf("the actor became %v", found[0].Actor)
	}
}
