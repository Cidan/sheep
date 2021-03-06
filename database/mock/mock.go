package mock

import (
	"context"
	"errors"
	"sync"

	"cloud.google.com/go/spanner"
	"github.com/Cidan/sheep/database"
	"google.golang.org/grpc/codes"
)

type MockDatabase struct {
	ForceError bool
	db         map[string]int64
	exists     map[string]bool
	log        map[string]bool
	lock       sync.Mutex
}

type MockQueue struct {
	ForceError bool
	queue      []*database.Message
	c          chan bool
}

func NewMockDatabase(forceError bool) (database.Database, error) {
	return &MockDatabase{
		ForceError: forceError,
		db:         make(map[string]int64),
		exists:     make(map[string]bool),
		log:        make(map[string]bool),
		lock:       sync.Mutex{},
	}, nil
}

func NewMockQueue(forceError bool) (database.Stream, error) {
	return &MockQueue{
		ForceError: forceError,
		c:          make(chan bool),
	}, nil
}

func (db *MockDatabase) Save(m *database.Message) error {
	if db.ForceError {
		return db.SaveError(m)
	}

	db.lock.Lock()
	defer db.lock.Unlock()
	if db.log[m.UUID] {
		return nil
	}
	db.log[m.UUID] = true

	key := m.Keyspace + m.Key + m.Name

	switch m.Operation {
	case "INCR":
		db.db[key]++
		db.exists[key] = true
	case "DECR":
		db.db[key]--
		db.exists[key] = true
	case "SET":
		db.db[key] = m.Value
		db.exists[key] = true
	default:
		return errors.New("invalid op")
	}

	return nil
}

func (db *MockDatabase) Read(m *database.Message) error {
	if db.ForceError {
		return db.ReadError(m)
	}
	db.lock.Lock()
	defer db.lock.Unlock()
	if !db.exists[m.Keyspace+m.Key+m.Name] {
		return &spanner.Error{Code: codes.NotFound}
	}
	m.Value = db.db[m.Keyspace+m.Key+m.Name]
	return nil
}

func (db *MockDatabase) SaveError(m *database.Message) error {
	return &spanner.Error{Code: codes.Internal}
}

func (db *MockDatabase) ReadError(m *database.Message) error {
	return &spanner.Error{Code: codes.Internal}
}

func (q *MockQueue) Save(m *database.Message) error {
	if q.ForceError {
		return q.SaveError(m)
	}
	q.queue = append(q.queue, m)
	q.c <- true
	return nil
}

func (q *MockQueue) Read(ctx context.Context, fn database.MessageFn) error {
	if q.ForceError {
		return q.ReadError(ctx, fn)
	}
	select {
	case <-q.c:
		go func() {
			var m *database.Message
			m, q.queue = q.queue[0], q.queue[1:]
			ok := fn(m)
			if !ok {
				q.Save(m)
			}
		}()
	}
	return nil
}

func (q *MockQueue) SaveError(m *database.Message) error {
	return &spanner.Error{Code: codes.Internal}
}

func (q *MockQueue) ReadError(ctx context.Context, fn database.MessageFn) error {
	return &spanner.Error{Code: codes.Internal}
}

// TODO: implement cancel channel
func (q *MockQueue) StartWork(db database.Database) {
	go q.Read(context.Background(), func(msg *database.Message) bool {
		err := db.Save(msg)
		if err != nil {
			return false
		}
		return true
	})
}

func (q *MockQueue) StopWork() {

}
