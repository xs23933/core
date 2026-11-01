package core

import (
	"errors"
	"sync"

	"github.com/redis/go-redis/v9"
	"github.com/xs23933/core/v3/sid"
	"github.com/xs23933/core/v3/xid"
)

// An application owns exactly the resources it opened, even when newer applications
// replace the process-wide compatibility defaults. Never hold mu during network I/O.
type applicationResources struct {
	mu         sync.Mutex
	closed     bool
	closeOnce  sync.Once
	closeErr   error
	dbs        map[string]*DB
	dbTypes    map[string]string
	snid       *sid.SnowflakeID
	xid        *xid.Generator
	redis      map[string]*RedisClient
	producer   *NSQProducer
	authSecret string
	consumers  []*NSQConsumer
}

func (app *Core) initializeResources(colorful bool) error {
	r := app.resources
	var err error
	if cfg := app.Conf.GetMap("database"); len(cfg) > 0 {
		r.snid, r.xid, err = newModelGenerators(app.Conf.GetInt64("node_id", 1))
		if err != nil {
			return errors.New("core: configured database node ID initialization failed")
		}
		r.dbs, r.dbTypes, err = openModels(cfg, app.Debug, colorful, r.snid)
		if err != nil {
			return errors.New("core: configured database initialization failed")
		}
	}
	if cfg := app.Conf.GetMap("redis"); len(cfg) > 0 {
		r.redis, err = openRedisConnections(cfg, app.Debug)
		if err != nil {
			return errors.New("core: configured redis initialization failed")
		}
	}
	if cfg := app.Conf.GetMap("nsq"); len(cfg) > 0 {
		r.producer, r.authSecret, err = openNSQ(cfg, app.Debug)
		if err != nil {
			return errors.New("core: configured nsq initialization failed")
		}
	}
	// Publish only after the complete set has initialized successfully.
	dbMu.Lock()
	if r.snid != nil {
		SnID, XID = r.snid, r.xid
	}
	for name, db := range r.dbs {
		conns[name], dbsType[name] = db, r.dbTypes[name]
	}
	dbMu.Unlock()
	redisMu.Lock()
	for name, client := range r.redis {
		redisConns[name] = client
	}
	redisMu.Unlock()
	if r.producer != nil {
		nsqMu.Lock()
		nsqProducer, nsqAuthSecret, nsqOwner = r.producer, r.authSecret, r
		nsqMu.Unlock()
	}
	return nil
}

// Conn returns this application's database, independently of the global Conn default.
func (app *Core) Conn(name ...string) *DB {
	key := "default"
	if len(name) > 0 {
		key = name[0]
	}
	return app.resources.dbs[key]
}

// RConn returns this application's Redis client.
func (app *Core) RConn(name ...string) *RedisClient {
	key := "default"
	if len(name) > 0 {
		key = name[0]
	}
	return app.resources.redis[key]
}

// NProducer returns this application's NSQ producer.
func (app *Core) NProducer() *NSQProducer { return app.resources.producer }

// NewNSQConsumer binds authentication and shutdown to this application, rather than
// the most recently initialized process-wide producer. Explicit AuthSecret wins.
func (app *Core) NewNSQConsumer(conf NSQConsumerConfig) (*NSQConsumer, error) {
	return newNSQConsumer(conf, app.resources.authSecret, app.resources)
}

// stopConsumers runs before user shutdown hooks and module Stop. Legacy Stop
// methods may wait for consumer.Start to return, so withdrawal cannot wait until
// after those methods have finished.
func (r *applicationResources) stopConsumers() []*NSQConsumer {
	r.mu.Lock()
	r.closed = true
	consumers := append([]*NSQConsumer(nil), r.consumers...)
	r.mu.Unlock()
	for _, consumer := range consumers {
		consumer.Stop()
	}
	return consumers
}

func (r *applicationResources) close() error {
	r.closeOnce.Do(func() { r.closeErr = r.closeResources() })
	return r.closeErr
}

func (r *applicationResources) closeResources() error {
	consumers := r.stopConsumers()
	dbMu.Lock()
	for name, db := range r.dbs {
		if conns[name] == db {
			delete(conns, name)
			delete(dbsType, name)
		}
	}
	dbMu.Unlock()
	redisMu.Lock()
	for name, client := range r.redis {
		if redisConns[name] == client {
			delete(redisConns, name)
		}
	}
	redisMu.Unlock()
	nsqMu.Lock()
	if nsqOwner == r {
		nsqProducer, nsqAuthSecret, nsqOwner = nil, "", nil
	}
	nsqMu.Unlock()
	// StopChan alone may close while go-nsq handlers are still running.
	for _, consumer := range consumers {
		consumer.waitStopped()
	}

	if r.producer != nil {
		r.producer.Stop()
	}
	var result error
	for _, client := range r.redis {
		if err := client.Close(); err != nil && !errors.Is(err, redis.ErrClosed) {
			result = errors.Join(result, err)
		}
	}
	for _, db := range r.dbs {
		sqlDB, err := db.DB()
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		result = errors.Join(result, sqlDB.Close())
	}
	return result
}
