package etcd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

type Registry struct {
	client      *clientv3.Client
	lease       clientv3.Lease
	leaseID     clientv3.LeaseID
	opts        *Options
	ctx         context.Context
	cancel      context.CancelFunc
	keepAliveCh <-chan *clientv3.LeaseKeepAliveResponse
	mu          sync.Mutex
	opMu        sync.Mutex
	wg          sync.WaitGroup
	registered  bool
	wgStarted   bool
	registerMu  sync.Mutex
	generation  string
	ready       *bool
	value       string
}

// ErrRegistrationSuperseded means another process owns this registration key.
var ErrRegistrationSuperseded = errors.New("etcd registration superseded by another generation")

// ServiceInfo 服务信息
type ServiceInfo struct {
	Generation string            `json:"generation,omitempty"`
	Ready      *bool             `json:"ready,omitempty"`
	Name       string            `json:"name"`
	Addr       string            `json:"addr"`
	ID         string            `json:"id"`
	Version    string            `json:"version"`
	Metadata   map[string]string `json:"metadata"`
	TTL        int64             `json:"ttl"`
}

func NewRegistry(opts *Options) (*Registry, error) {
	if opts == nil {
		opts = DefaultOptions()
	}

	opts = cloneOptions(opts)
	var generation [16]byte
	if _, err := rand.Read(generation[:]); err != nil {
		return nil, fmt.Errorf("registry generation: %w", err)
	}

	// 创建 etcd 客户端
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   opts.Endpoints,
		Username:    opts.Username,
		Password:    opts.Password,
		DialTimeout: opts.DialTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("create etcd client: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Registry{
		client:     client,
		generation: hex.EncodeToString(generation[:]),
		ready:      opts.RegistrationReady,
		lease:      clientv3.NewLease(client),
		opts:       opts,
		ctx:        ctx,
		cancel:     cancel,
	}, nil
}

// doRegister replaces an initial registration for compatibility. Recovery is
// fenced by the last owned value; a retired process cannot overwrite a successor.
func (r *Registry) doRegister() error {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	ctx, cancel := context.WithTimeout(r.ctx, 5*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	recovery, previousValue := r.registered, r.value
	ready := r.ready
	r.mu.Unlock()
	info := ServiceInfo{Name: r.opts.ServiceName, Addr: r.opts.ServiceAddr, ID: r.opts.ServiceID,
		Version: r.opts.Version, Metadata: r.opts.Metadata, TTL: r.opts.TTL, Generation: r.generation, Ready: ready}
	value, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("marshal service info: %w", err)
	}
	var compare clientv3.Cmp
	if recovery {
		snapshot, err := r.client.Get(ctx, r.opts.ServiceKey())
		if err != nil {
			return fmt.Errorf("read registration owner: %w", err)
		}
		compare = clientv3.Compare(clientv3.Version(r.opts.ServiceKey()), "=", 0)
		if len(snapshot.Kvs) > 0 {
			if string(snapshot.Kvs[0].Value) != previousValue {
				return ErrRegistrationSuperseded
			}
			compare = clientv3.Compare(clientv3.ModRevision(r.opts.ServiceKey()), "=", snapshot.Kvs[0].ModRevision)
		}
	}
	grant, err := r.lease.Grant(ctx, r.opts.TTL)
	if err != nil {
		return fmt.Errorf("grant lease: %w", err)
	}
	success := false
	defer func() {
		if !success {
			r.revoke(grant.ID)
		}
	}()
	put := clientv3.OpPut(r.opts.ServiceKey(), string(value), clientv3.WithLease(grant.ID))
	txn := r.client.Txn(ctx)
	if recovery {
		txn = txn.If(compare)
	}
	// Fence every later attempt even if Commit returns an ambiguous transport error.
	r.mu.Lock()
	r.registered = true
	r.value = string(value)
	r.mu.Unlock()
	response, err := txn.Then(put).Commit()
	if err != nil {
		return fmt.Errorf("put service: %w", err)
	}
	if !response.Succeeded {
		return ErrRegistrationSuperseded
	}
	r.mu.Lock()
	r.registered = true
	r.value = string(value)
	r.mu.Unlock()
	channel, err := r.lease.KeepAlive(r.ctx, grant.ID)
	if err != nil {
		return fmt.Errorf("keep alive: %w", err)
	}
	r.mu.Lock()
	r.leaseID = grant.ID
	r.keepAliveCh = channel
	r.registered = true
	r.value = string(value)
	r.mu.Unlock()
	success = true
	return nil
}

// Register publishes the instance and starts lease recovery. It is idempotent
// on the same Registry; a new Registry represents a new process generation.
func (r *Registry) Register() error {
	r.registerMu.Lock()
	defer r.registerMu.Unlock()
	r.mu.Lock()
	started := r.wgStarted
	r.mu.Unlock()
	if started {
		return r.ctx.Err()
	}
	var err error
	for i := 0; i < 3; i++ {
		if err = r.doRegister(); err == nil {
			// Serialize Add with Deregister's cancellation barrier.
			r.opMu.Lock()
			if r.ctx.Err() != nil {
				r.opMu.Unlock()
				return r.ctx.Err()
			}
			r.mu.Lock()
			if r.wgStarted {
				r.mu.Unlock()
				r.opMu.Unlock()
				return nil
			}
			r.wgStarted = true
			r.wg.Add(1)
			r.mu.Unlock()
			go r.keepAlive()
			r.opMu.Unlock()
			return nil
		}
		if errors.Is(err, ErrRegistrationSuperseded) {
			return err
		}
		select {
		case <-r.ctx.Done():
			return r.ctx.Err()
		case <-time.After(time.Duration(i+1) * time.Second):
		}
	}
	return fmt.Errorf("register failed after 3 attempts: %w", err)
}

// SetReady publishes readiness only while this Registry still owns its key.
// The same readiness is retained when a lost lease is recovered.
func (r *Registry) SetReady(ctx context.Context, ready bool) error {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	if err := r.ctx.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r.mu.Lock()
	value, leaseID := r.value, r.leaseID
	r.mu.Unlock()
	if leaseID == 0 {
		return errors.New("etcd registration has no active lease")
	}
	var info ServiceInfo
	if err := json.Unmarshal([]byte(value), &info); err != nil {
		return err
	}
	info.Ready = &ready
	encoded, err := json.Marshal(info)
	if err != nil {
		return err
	}
	response, err := r.client.Txn(ctx).If(
		clientv3.Compare(clientv3.Value(r.opts.ServiceKey()), "=", value),
		clientv3.Compare(clientv3.LeaseValue(r.opts.ServiceKey()), "=", int64(leaseID)),
	).Then(clientv3.OpPut(r.opts.ServiceKey(), string(encoded), clientv3.WithLease(leaseID))).Commit()
	if err != nil {
		return fmt.Errorf("publish registration readiness: %w", err)
	}
	if !response.Succeeded {
		return ErrRegistrationSuperseded
	}
	r.mu.Lock()
	r.ready = &ready
	r.value = string(encoded)
	r.mu.Unlock()
	return nil
}

func (r *Registry) keepAlive() {
	defer r.wg.Done()
	for {
		r.mu.Lock()
		channel := r.keepAliveCh
		r.mu.Unlock()
		select {
		case response, ok := <-channel:
			if !ok || response == nil {
				r.clearLease()
				err := runRetryLoop(r.ctx, time.Second, func(context.Context) error {
					err := r.doRegister()
					if errors.Is(err, ErrRegistrationSuperseded) {
						r.cancel()
					}
					return err
				})
				if err != nil {
					return
				}
			}
		case <-r.ctx.Done():
			return
		}
	}
}

func (r *Registry) revoke(id clientv3.LeaseID) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = r.lease.Revoke(ctx, id)
}

func (r *Registry) clearLease() {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	r.mu.Lock()
	id := r.leaseID
	r.leaseID = 0
	r.keepAliveCh = nil
	r.mu.Unlock()
	if id > 0 {
		r.revoke(id)
	}
}

func runRetryLoop(ctx context.Context, baseDelay time.Duration, fn func(context.Context) error) error {
	if baseDelay <= 0 {
		baseDelay = time.Second
	}
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(ctx); err == nil {
			return nil
		}

		delay := baseDelay * time.Duration(attempt+1)
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

// Deregister cancels recovery before revoking the last owned lease. Waiting
// behind opMu prevents an in-flight recovery from publishing after shutdown.
func (r *Registry) Deregister() error {
	r.cancel()
	r.opMu.Lock()
	r.opMu.Unlock()
	r.wg.Wait()
	r.clearLease()
	if err := r.client.Close(); err != nil {
		return fmt.Errorf("close etcd client: %w", err)
	}
	return nil
}
