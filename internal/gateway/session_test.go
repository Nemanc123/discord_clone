package gateway

import (
	"testing"
	"time"
)

func TestSessionRecordsAndReplays(t *testing.T) {
	session := NewSessionStore().Create("user-1", []string{"g1"})

	session.record(session.nextSeq(), []byte("a")) // 1
	session.record(session.nextSeq(), []byte("b")) // 2
	session.record(session.nextSeq(), []byte("c")) // 3

	events, ok := session.eventsAfter(1)
	if !ok {
		t.Fatal("дыры быть не должно")
	}
	if len(events) != 2 {
		t.Fatalf("получили %d событий, ожидали 2", len(events))
	}
	if string(events[0]) != "b" || string(events[1]) != "c" {
		t.Fatalf("неверный порядок: %q, %q", events[0], events[1])
	}
}

func TestSessionReportsGapWhenSeqTooOld(t *testing.T) {
	session := NewSessionStore().Create("user-1", nil)

	// Переполняем буфер: maxBuffer+5 событий.
	for i := 0; i < maxBuffer+5; i++ {
		session.record(session.nextSeq(), []byte("x"))
	}

	// seq=1 уже вытеснен из буфера первой сотней событий.
	if _, ok := session.eventsAfter(1); ok {
		t.Fatal("ожидали сообщение о дыре")
	}

	// Свежий seq догоняется нормально.
	if _, ok := session.eventsAfter(session.seq.Load()); !ok {
		t.Fatal("для последнего seq догон должен работать")
	}
}

func TestSessionEventsAfterCurrentSeqIsEmpty(t *testing.T) {
	session := NewSessionStore().Create("user-1", nil)
	session.record(session.nextSeq(), []byte("a"))

	events, ok := session.eventsAfter(1)
	if !ok {
		t.Fatal("дыры быть не должно")
	}
	if len(events) != 0 {
		t.Fatalf("ожидали 0 событий, получили %d", len(events))
	}
}

func TestSessionStoreExpires(t *testing.T) {
	store := NewSessionStore()
	now := time.Now()
	store.now = func() time.Time { return now }

	session := store.Create("user-1", nil)

	if _, ok := store.Get(session.ID); !ok {
		t.Fatal("свежая сессия должна находиться")
	}

	now = now.Add(sessionTTL + time.Second)

	if _, ok := store.Get(session.ID); ok {
		t.Fatal("истёкшая сессия не должна находиться")
	}
}

func TestSessionStoreTouchExtends(t *testing.T) {
	store := NewSessionStore()
	now := time.Now()
	store.now = func() time.Time { return now }

	session := store.Create("user-1", nil)

	now = now.Add(sessionTTL - time.Minute)
	store.Touch(session)

	now = now.Add(2 * time.Minute)

	if _, ok := store.Get(session.ID); !ok {
		t.Fatal("после Touch сессия должна быть жива")
	}
}

func TestSessionStoreDelete(t *testing.T) {
	store := NewSessionStore()
	session := store.Create("user-1", nil)
	store.Delete(session.ID)

	if _, ok := store.Get(session.ID); ok {
		t.Fatal("удалённая сессия не должна находиться")
	}
	if store.Len() != 0 {
		t.Fatalf("в хранилище %d сессий, ожидали 0", store.Len())
	}
}
