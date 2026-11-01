package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nsqio/go-nsq"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

func architectureApp(t *testing.T, conf Options) *Core {
	t.Helper()
	app := New(conf)
	t.Cleanup(func() {
		if err := app.ShutdownWithTimeout(time.Second); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	return app
}

func TestApplicationDatabaseOwnership(t *testing.T) {
	resetModelState(t)
	makeApp := func(name string) *Core {
		return architectureApp(t, Options{"debug": false, "database": Options{"type": "sqlite3", "dsn": filepath.Join(t.TempDir(), name+".db")}})
	}
	a, b := makeApp("a"), makeApp("b")
	if a.Conn() == b.Conn() || Conn() != b.Conn() {
		t.Fatal("instance/default database ownership incorrect")
	}
	for _, app := range []*Core{a, b} {
		if err := app.Conn().Exec("CREATE TABLE owned (id integer)").Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := a.ShutdownWithTimeout(time.Second); err != nil {
		t.Fatal(err)
	}
	aSQL, _ := a.Conn().DB()
	if err := aSQL.Ping(); err == nil {
		t.Fatal("owned database was not closed")
	}
	bSQL, _ := b.Conn().DB()
	if err := bSQL.Ping(); err != nil {
		t.Fatalf("closing a closed b: %v", err)
	}
	if Conn() != b.Conn() {
		t.Fatal("closing a removed b's global compatibility connection")
	}
}

func TestDependencyFailurePreventsServing(t *testing.T) {
	app := architectureApp(t, Options{"debug": false, "database": Options{"type": "invalid-driver", "dsn": "unused"}})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := app.Serve(ln); err == nil {
		t.Fatal("Serve accepted failed database initialization")
	}
	if app.ServingReady() {
		t.Fatal("failed dependency marked ready")
	}
	if conn, err := net.DialTimeout("tcp", ln.Addr().String(), 100*time.Millisecond); err == nil {
		conn.Close()
		t.Fatal("failed startup left listener open")
	}
}

type architectureHandler struct {
	Handler
	prepare     func(*Core) error
	initialized bool
	starts      *atomic.Int32
}

func (h *architectureHandler) Init() { h.initialized = true; h.Prefix("/architecture") }
func (h *architectureHandler) Prepare(app *Core) error {
	if !h.initialized || h.Core() != app {
		return errors.New("Prepare ran before handler initialization")
	}
	if h.prepare != nil {
		return h.prepare(app)
	}
	return nil
}
func (h *architectureHandler) Start(app *Core) error {
	if h.starts != nil {
		h.starts.Add(1)
	}
	<-app.Ctx.Done()
	return nil
}
func (h *architectureHandler) Get(c Ctx) error { return c.SendString(h.Core().Conf.GetString("label")) }

func isolatedArchitectureModules(t *testing.T) {
	t.Helper()
	modulesMu.Lock()
	previous := modules
	modules = make(map[string]ModuleInfo)
	modulesMu.Unlock()
	t.Cleanup(func() { modulesMu.Lock(); modules = previous; modulesMu.Unlock() })
}

func TestPrepareGatesReadinessAndInitializesHandlerFirst(t *testing.T) {
	isolatedArchitectureModules(t)
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	RegHandle(&architectureHandler{prepare: func(*Core) error { close(entered); <-release; return nil }})
	app := architectureApp(t, Options{"debug": false})
	done := make(chan error, 1)
	go func() { done <- app.prepareServeOnce() }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("Prepare did not run")
	}
	if app.ServingReady() {
		t.Fatal("ready before Prepare completed")
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("legacy blocking Start held startup")
	}
	if !app.ServingReady() {
		t.Fatal("not ready after Prepare")
	}
}

func TestPrepareFailurePreventsStartAndReadiness(t *testing.T) {
	isolatedArchitectureModules(t)
	var starts atomic.Int32
	failure := errors.New("prepare failed")
	RegHandle(&architectureHandler{prepare: func(*Core) error { return failure }, starts: &starts})
	app := architectureApp(t, Options{"debug": false})
	if err := app.prepareServeOnce(); !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}
	if app.ServingReady() || starts.Load() != 0 {
		t.Fatal("failed preparation started module or published ready")
	}
}

func TestHandlerFactoryIsolatesApplicationsAndLegacyRejectsReuse(t *testing.T) {
	for _, factory := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "factory"}[factory], func(t *testing.T) {
			isolatedArchitectureModules(t)
			if factory {
				RegHandleFactory(func() Module { return &architectureHandler{} })
			} else {
				RegHandle(&architectureHandler{})
			}
			a := architectureApp(t, Options{"debug": false, "label": "a"})
			b := architectureApp(t, Options{"debug": false, "label": "b"})
			if err := a.prepareServeOnce(); err != nil {
				t.Fatal(err)
			}
			err := b.prepareServeOnce()
			if !factory && err == nil {
				t.Fatal("legacy handler reused across applications")
			}
			if factory && err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			a.ServeHTTP(rec, httptest.NewRequest("GET", "/architecture", nil))
			if rec.Body.String() != "a" {
				t.Fatalf("first application handler rebound: %q", rec.Body.String())
			}
		})
	}
}

func TestApplicationRedisOwnershipAndDefaultFallback(t *testing.T) {
	redisMu.Lock()
	old := redisConns
	redisConns = make(map[string]*RedisClient)
	redisMu.Unlock()
	t.Cleanup(func() { redisMu.Lock(); redisConns = old; redisMu.Unlock() })
	a := architectureApp(t, Options{"debug": false})
	b := architectureApp(t, Options{"debug": false})
	ca := &RedisClient{Client: redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})}
	cb := &RedisClient{Client: redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})}
	a.resources.redis = map[string]*RedisClient{"default": ca}
	b.resources.redis = map[string]*RedisClient{"default": cb}
	redisMu.Lock()
	redisConns["default"] = cb
	redisMu.Unlock()
	if a.RConn() != ca || b.RConn() != cb {
		t.Fatal("instance redis lookup crossed applications")
	}
	if err := a.ShutdownWithTimeout(time.Second); err != nil {
		t.Fatal(err)
	}
	if RConn() != cb {
		t.Fatal("closing a removed b's default redis")
	}
	if err := ca.Client.Ping(context.Background()).Err(); !errors.Is(err, redis.ErrClosed) {
		t.Fatalf("a's redis not closed: %v", err)
	}
	if err := cb.Client.Ping(context.Background()).Err(); errors.Is(err, redis.ErrClosed) {
		t.Fatal("b's redis closed with a")
	}
}

func TestApplicationNSQOwnershipAndAuthentication(t *testing.T) {
	CloseNSQ()
	aAddr, aAuth := authNSQServer(t, "a-secret")
	bAddr, bAuth := authNSQServer(t, "b-secret")
	a := architectureApp(t, Options{"debug": false, "nsq": Options{"nsqd": aAddr, "auth_secret": "a-secret"}})
	if <-aAuth != "a-secret" {
		t.Fatal("a producer auth")
	}
	aConsumer, err := NewNSQConsumer(NSQConsumerConfig{Topic: "owned", Channel: "a", NSQDs: []string{aAddr}})
	if err != nil {
		t.Fatal(err)
	}
	b := architectureApp(t, Options{"debug": false, "nsq": Options{"nsqd": bAddr, "auth_secret": "b-secret"}})
	if <-bAuth != "b-secret" {
		t.Fatal("b producer auth")
	}
	// Explicit instance construction must not inherit b's most recent default secret.
	explicit, err := a.NewNSQConsumer(NSQConsumerConfig{Topic: "owned", Channel: "explicit", NSQDs: []string{aAddr}})
	if err != nil {
		t.Fatal(err)
	}
	bConsumer, err := b.NewNSQConsumer(NSQConsumerConfig{Topic: "owned", Channel: "b", NSQDs: []string{bAddr}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []*NSQConsumer{aConsumer, explicit, bConsumer} {
		if err := c.StartAsync(func(*nsq.Message) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if <-aAuth != "a-secret" || <-aAuth != "a-secret" || <-bAuth != "b-secret" {
		t.Fatal("consumer authentication crossed applications")
	}
	if err := a.ShutdownWithTimeout(time.Second); err != nil {
		t.Fatal(err)
	}
	if NProducer() != b.NProducer() {
		t.Fatal("closing a removed b producer")
	}
	for _, c := range []*NSQConsumer{aConsumer, explicit} {
		select {
		case <-c.consumer.StopChan:
		case <-time.After(time.Second):
			t.Fatal("a consumer not stopped")
		}
	}
	select {
	case <-bConsumer.consumer.StopChan:
		t.Fatal("b consumer stopped with a")
	default:
	}
	if _, err := a.NewNSQConsumer(NSQConsumerConfig{Topic: "late", Channel: "late"}); err == nil {
		t.Fatal("consumer admitted after shutdown")
	}
}

func TestPartialDependencyFailureRollsBackWithoutReplacingDefaults(t *testing.T) {
	resetModelState(t)
	good := architectureApp(t, Options{"debug": false, "database": Options{"type": "sqlite3", "dsn": filepath.Join(t.TempDir(), "good.db")}})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	failed := architectureApp(t, Options{"debug": false, "database": Options{"type": "sqlite3", "dsn": filepath.Join(t.TempDir(), "failed.db")}, "redis": Options{"addr": addr}})
	if failed.dependencyErr == nil {
		t.Fatal("redis failure was ignored")
	}
	if Conn() != good.Conn() {
		t.Fatal("partial startup replaced the working default database")
	}
	db, err := failed.Conn().DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err == nil {
		t.Fatal("database leaked after redis failure")
	}
}

func TestStandaloneClientsDoNotDependOnCore(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "./fetch", "./cache")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("inspect package dependencies: %v\n%s", err, output)
	}
	for _, line := range strings.Fields(string(output)) {
		if line == "github.com/xs23933/core/v3" {
			t.Fatal("standalone client imports the application root package")
		}
	}
}

type architectureSnowflakeModel struct {
	SModels
	Name string
}

func TestApplicationDatabaseKeepsGeneratorAndQueryIsolation(t *testing.T) {
	resetModelState(t)
	makeApp := func(node int64) *Core {
		return architectureApp(t, Options{"debug": false, "node_id": node, "database": Options{"type": "sqlite3", "dsn": filepath.Join(t.TempDir(), "owned.db")}})
	}
	a, b := makeApp(1), makeApp(2)
	for _, app := range []*Core{a, b} {
		if err := app.Conn().AutoMigrate(&architectureSnowflakeModel{}); err != nil {
			t.Fatal(err)
		}
	}
	for i, app := range []*Core{a, b, a} {
		model := architectureSnowflakeModel{Name: fmt.Sprint(i)}
		if err := app.Conn().Session(&gorm.Session{NewDB: true}).Create(&model).Error; err != nil {
			t.Fatal(err)
		}
		want := int64(1)
		if app == b {
			want = 2
		}
		if node := (int64(model.ID) >> 12) & 1023; node != want {
			t.Fatalf("database generator changed: node=%d want=%d", node, want)
		}
	}
	var filtered, total int64
	if err := a.Conn().Model(&architectureSnowflakeModel{}).Where("name = ?", "missing").Count(&filtered).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.Conn().Model(&architectureSnowflakeModel{}).Count(&total).Error; err != nil {
		t.Fatal(err)
	}
	if filtered != 0 || total != 2 {
		t.Fatalf("reused query leaked conditions: filtered=%d total=%d", filtered, total)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.Conn().WithContext(ctx).Find(&[]architectureSnowflakeModel{}).Error; !errors.Is(err, context.Canceled) {
		t.Fatalf("DAO context lost: %v", err)
	}
}

func TestApplicationUnstartedConsumerDoesNotBlockCleanup(t *testing.T) {
	resetModelState(t)
	app := architectureApp(t, Options{"debug": false, "database": Options{"type": "sqlite3", "dsn": filepath.Join(t.TempDir(), "owned.db")}})
	consumer, err := app.NewNSQConsumer(NSQConsumerConfig{Topic: "unstarted", Channel: "unstarted"})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.ShutdownWithTimeout(time.Second); err != nil {
		t.Fatalf("unstarted consumer blocked shutdown: %v", err)
	}
	db, err := app.Conn().DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err == nil {
		t.Fatal("database not released")
	}
	if err := consumer.StartAsync(func(*nsq.Message) error { return nil }); err == nil {
		t.Fatal("stopped consumer restarted")
	}
}

// This exercises the wrapped callback directly so transport completion can occur
// before the simulated business operation returns (also possible on go-nsq's fallback).
func TestApplicationConsumerDrainKeepsDependenciesAlive(t *testing.T) {
	resetModelState(t)
	app := architectureApp(t, Options{"debug": false, "database": Options{"type": "sqlite3", "dsn": filepath.Join(t.TempDir(), "owned.db")}})
	c, err := app.NewNSQConsumer(NSQConsumerConfig{Topic: "draining", Channel: "draining"})
	if err != nil {
		t.Fatal(err)
	}
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	if err := c.beginStart(func(*nsq.Message) error { close(entered); <-release; return nil }); err != nil {
		t.Fatal(err)
	}
	go func() { defer close(finished); _ = c.handler(nil) }()
	<-entered
	shutdown := make(chan error, 1)
	go func() { shutdown <- app.ShutdownWithTimeout(time.Second) }()
	select {
	case <-c.consumer.StopChan:
	case <-time.After(time.Second):
		t.Fatal("transport did not stop")
	}
	db, err := app.Conn().DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("dependency closed before handler returned: %v", err)
	}
	select {
	case err := <-shutdown:
		t.Fatalf("shutdown completed with active business handler: %v", err)
	default:
	}
	if err := c.handler(nil); err == nil {
		t.Fatal("new work admitted after consumer stop")
	}
	releaseOnce.Do(func() { close(release) })
	<-finished
	if err := <-shutdown; err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err == nil {
		t.Fatal("dependency not released after business drain")
	}
}

type architectureWaitingConsumerHandler struct {
	Handler
	conf     NSQConsumerConfig
	consumer *NSQConsumer
	done     chan struct{}
}

func (h *architectureWaitingConsumerHandler) Prepare(app *Core) error {
	var err error
	h.consumer, err = app.NewNSQConsumer(h.conf)
	return err
}
func (h *architectureWaitingConsumerHandler) Start(app *Core) error {
	defer close(h.done)
	return h.consumer.Start(func(*nsq.Message) error { return nil })
}
func (h *architectureWaitingConsumerHandler) Stop(*Core) error { <-h.done; return nil }

func TestApplicationStopsConsumerBeforeWaitingModuleStop(t *testing.T) {
	isolatedArchitectureModules(t)
	addr, auths := authNSQServer(t, "legacy-secret")
	h := &architectureWaitingConsumerHandler{conf: NSQConsumerConfig{Topic: "waiting", Channel: "waiting", NSQDs: []string{addr}, AuthSecret: "legacy-secret"}, done: make(chan struct{})}
	RegHandle(h)
	app := architectureApp(t, Options{"debug": false})
	if err := app.prepareServeOnce(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-auths:
	case <-time.After(time.Second):
		t.Fatal("consumer did not start")
	}
	if err := app.ShutdownWithTimeout(time.Second); err != nil {
		t.Fatalf("module Stop waited on consumer withdrawal: %v", err)
	}
}

func TestApplicationSameNodeIDDoesNotForkSequences(t *testing.T) {
	resetModelState(t)
	makeApp := func() *Core {
		return architectureApp(t, Options{"debug": false, "node_id": int64(9), "database": Options{"type": "sqlite3", "dsn": filepath.Join(t.TempDir(), "owned.db")}})
	}
	apps := []*Core{makeApp(), makeApp()}
	const perApp = 200
	ids := make(chan int64, perApp*len(apps))
	errs := make(chan error, len(apps))
	var workers sync.WaitGroup
	for _, app := range apps {
		workers.Add(1)
		go func(app *Core) {
			defer workers.Done()
			for i := 0; i < perApp; i++ {
				var model architectureSnowflakeModel
				if err := app.Conn().Session(&gorm.Session{DryRun: true, NewDB: true}).Create(&model).Error; err != nil {
					errs <- err
					return
				}
				ids <- int64(model.ID)
			}
		}(app)
	}
	workers.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	seen := make(map[int64]bool)
	for id := range ids {
		if seen[id] {
			t.Fatalf("duplicate ID across same-node applications: %d", id)
		}
		seen[id] = true
	}
	if len(seen) != perApp*len(apps) {
		t.Fatalf("only generated %d IDs", len(seen))
	}
}

type architectureSnowflakeParent struct {
	SModels
	Children []architectureSnowflakeChild `gorm:"foreignKey:ParentID"`
}
type architectureSnowflakeChild struct {
	SModels
	ParentID int64
}

func TestApplicationAssociationUsesDatabaseGenerator(t *testing.T) {
	resetModelState(t)
	a := architectureApp(t, Options{"debug": false, "node_id": int64(3), "database": Options{"type": "sqlite3", "dsn": filepath.Join(t.TempDir(), "a.db")}})
	_ = architectureApp(t, Options{"debug": false, "node_id": int64(4), "database": Options{"type": "sqlite3", "dsn": filepath.Join(t.TempDir(), "b.db")}})
	if err := a.Conn().AutoMigrate(&architectureSnowflakeParent{}, &architectureSnowflakeChild{}); err != nil {
		t.Fatal(err)
	}
	parent := architectureSnowflakeParent{Children: []architectureSnowflakeChild{{}}}
	if err := a.Conn().Session(&gorm.Session{NewDB: true}).Create(&parent).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{int64(parent.ID), int64(parent.Children[0].ID)} {
		if node := (id >> 12) & 1023; node != 3 {
			t.Fatalf("association used another application's node: %d", node)
		}
	}
}

func TestApplicationExplicitGlobalRedisCloseRemainsIdempotent(t *testing.T) {
	redisMu.Lock()
	previous := redisConns
	redisConns = make(map[string]*RedisClient)
	redisMu.Unlock()
	t.Cleanup(func() { redisMu.Lock(); redisConns = previous; redisMu.Unlock() })
	app := architectureApp(t, Options{"debug": false})
	client := &RedisClient{Client: redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})}
	app.resources.redis = map[string]*RedisClient{"default": client}
	redisMu.Lock()
	redisConns["default"] = client
	redisMu.Unlock()
	app.OnShutdown(CloseRedis)
	if err := app.ShutdownWithTimeout(time.Second); err != nil {
		t.Fatalf("legacy explicit CloseRedis caused shutdown error: %v", err)
	}
}
