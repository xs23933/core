package etcd

import (
	"context"
	"errors"
	"strings"

	clientv3 "go.etcd.io/etcd/client/v3"
)

var (
	// ErrInvalidLeaseTTL 表示租约秒数不能交给 etcd 创建有效租约。
	ErrInvalidLeaseTTL = errors.New("etcd lease ttl must be positive")
	// ErrInvalidLeaseID 表示调用方没有提供由 GrantLease 返回的有效租约 ID。
	ErrInvalidLeaseID = errors.New("etcd lease id must be positive")
	// ErrInvalidLeaseKey 表示 key 不是当前 Discovery namespace 下的相对逻辑 key。
	ErrInvalidLeaseKey = errors.New("etcd lease key must be a non-empty relative key")
)

// LeaseID 是由当前 Discovery 所拥有 etcd client 签发的租约标识。
type LeaseID int64

// LeaseKeepAlive 是 keepalive 流中与调用方相关的最小租约状态。
type LeaseKeepAlive struct {
	ID  LeaseID
	TTL int64
}

// KVRevision 返回 CAS 所需的当前值和 ModRevision。
type KVRevision struct {
	Value       string
	ModRevision int64
}

// GrantLease 创建以秒为单位的 etcd 租约。
func (d *Discovery) GrantLease(ctx context.Context, ttlSeconds int64) (LeaseID, error) {
	if ttlSeconds <= 0 {
		return 0, ErrInvalidLeaseTTL
	}
	response, err := d.client.Grant(ctx, ttlSeconds)
	if err != nil {
		return 0, err
	}
	return LeaseID(response.ID), nil
}

// KeepAliveLease 持续续租，直到 context 取消、租约丢失或 Discovery 关闭。
// 返回 channel 会在上述任一终止条件发生时关闭。
func (d *Discovery) KeepAliveLease(ctx context.Context, id LeaseID) (<-chan LeaseKeepAlive, error) {
	if id <= 0 {
		return nil, ErrInvalidLeaseID
	}
	source, err := d.client.KeepAlive(ctx, clientv3.LeaseID(id))
	if err != nil {
		return nil, err
	}
	output := make(chan LeaseKeepAlive)
	go func() {
		defer close(output)
		for {
			select {
			case <-ctx.Done():
				return
			case response, open := <-source:
				if !open {
					return
				}
				if response == nil {
					continue
				}
				select {
				case output <- LeaseKeepAlive{ID: LeaseID(response.ID), TTL: response.TTL}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return output, nil
}

// RevokeLease 主动撤销租约及其绑定的所有 key。
func (d *Discovery) RevokeLease(ctx context.Context, id LeaseID) error {
	if id <= 0 {
		return ErrInvalidLeaseID
	}
	_, err := d.client.Revoke(ctx, clientv3.LeaseID(id))
	return err
}

// GetRevision 读取相对逻辑 key 的当前值及 CAS revision。
// found=false 表示 key 不存在，此时创建方应使用 expectedModRevision=0。
func (d *Discovery) GetRevision(ctx context.Context, key string) (revision KVRevision, found bool, err error) {
	fullKey, err := d.leaseKey(key)
	if err != nil {
		return KVRevision{}, false, err
	}
	response, err := d.client.Get(ctx, fullKey)
	if err != nil {
		return KVRevision{}, false, err
	}
	if len(response.Kvs) == 0 {
		return KVRevision{}, false, nil
	}
	return KVRevision{
		Value:       string(response.Kvs[0].Value),
		ModRevision: response.Kvs[0].ModRevision,
	}, true, nil
}

// CompareAndPut 以 revision CAS 原子写入 value 并绑定租约。
// expectedModRevision=0 只允许 key 尚未创建；CAS 冲突返回 false, nil。
func (d *Discovery) CompareAndPut(ctx context.Context, key string, expectedModRevision int64, value string, leaseID LeaseID) (bool, error) {
	fullKey, err := d.leaseKey(key)
	if err != nil {
		return false, err
	}
	if leaseID <= 0 {
		return false, ErrInvalidLeaseID
	}

	var comparison clientv3.Cmp
	if expectedModRevision == 0 {
		comparison = clientv3.Compare(clientv3.CreateRevision(fullKey), "=", 0)
	} else {
		comparison = clientv3.Compare(clientv3.ModRevision(fullKey), "=", expectedModRevision)
	}
	response, err := d.client.Txn(ctx).
		If(comparison).
		Then(clientv3.OpPut(fullKey, value, clientv3.WithLease(clientv3.LeaseID(leaseID)))).
		Commit()
	if err != nil {
		return false, err
	}
	return response.Succeeded, nil
}

func (d *Discovery) leaseKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" || strings.HasPrefix(key, "/") {
		return "", ErrInvalidLeaseKey
	}
	return d.opts.ConfigPrefix() + key, nil
}
