package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// sessionTTL — сколько сессия живёт после обрыва соединения.
	// Клиент за это время должен успеть переподключиться и прислать RESUME.
	sessionTTL = 5 * time.Minute

	// maxBuffer — сколько последних событий храним для догона.
	// При переполнении старое вытесняется, а клиент с более старым seq
	// получит INVALID_SESSION и перезагрузит состояние.
	maxBuffer = 256
)

// Session живёт дольше соединения: она и есть то, что позволяет
// восстановиться после обрыва.
type Session struct {
	ID        string
	UserID    string
	Guilds    []string
	ExpiresAt time.Time

	seq atomic.Int64

	mu       sync.Mutex
	buffer   [][]byte // готовые кадры DISPATCH
	firstSeq int64    // номер первого события в буфере
}

func newSessionID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "session"
	}
	return hex.EncodeToString(buf)
}

func (s *Session) nextSeq() int64 { return s.seq.Add(1) }

// appendDispatch присваивает событию номер, кодирует его, кладёт в буфер
// и возвращает готовый кадр. Вызывается и для онлайн-, и для офлайн-сессий.
func (s *Session) appendDispatch(eventType string, data any) ([]byte, error) {
	seq := s.nextSeq()

	payload, err := json.Marshal(envelope{
		Op: OpDispatch,
		T:  eventType,
		S:  seq,
		D:  data,
	})
	if err != nil {
		return nil, err
	}

	s.record(seq, payload)
	return payload, nil
}

// record сохраняет кадр для возможного догона.
func (s *Session) record(seq int64, payload []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.buffer) == 0 {
		s.firstSeq = seq
	}

	s.buffer = append(s.buffer, payload)
	if len(s.buffer) > maxBuffer {
		s.buffer = s.buffer[1:]
		s.firstSeq++
	}
}

// eventsAfter возвращает кадры с номером больше seq.
// ok == false означает дыру: запрошенный seq старше буфера,
// догнать нельзя, нужен полный IDENTIFY.
func (s *Session) eventsAfter(seq int64) ([][]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.buffer) == 0 {
		return nil, seq == s.seq.Load()
	}
	if seq+1 < s.firstSeq {
		return nil, false
	}

	out := make([][]byte, 0, len(s.buffer))
	for i, payload := range s.buffer {
		if s.firstSeq+int64(i) > seq {
			out = append(out, payload)
		}
	}
	return out, true
}

type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]*Session
	now      func() time.Time
}

func NewSessionStore() *SessionStore {
	return &SessionStore{
		sessions: make(map[string]*Session),
		now:      time.Now,
	}
}

func (st *SessionStore) Create(userID string, guilds []string) *Session {
	session := &Session{
		ID:        newSessionID(),
		UserID:    userID,
		Guilds:    guilds,
		ExpiresAt: st.now().Add(sessionTTL),
	}

	st.mu.Lock()
	st.sessions[session.ID] = session
	st.mu.Unlock()

	return session
}

func (st *SessionStore) Get(id string) (*Session, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()

	session, ok := st.sessions[id]
	if !ok {
		return nil, false
	}
	if st.now().After(session.ExpiresAt) {
		delete(st.sessions, id)
		return nil, false
	}
	return session, true
}

// Touch продлевает жизнь сессии после переподключения.
func (st *SessionStore) Touch(session *Session) {
	st.mu.Lock()
	defer st.mu.Unlock()
	session.ExpiresAt = st.now().Add(sessionTTL)
	st.sessions[session.ID] = session
}

func (st *SessionStore) Delete(id string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.sessions, id)
}

// Len — для тестов и метрик.
func (st *SessionStore) Len() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return len(st.sessions)
}
