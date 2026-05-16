/*
*
KV 存储相关方法
提供对etcd KV存储的封装，包括Put、Get等操作

app.EnableEtcdDiscovery(nil)
discovery := app.EtcdDiscovery

// 追加白名单（多个服务可写同一个 key，不会覆盖已有路径）

	discovery.Add("gateway/public_routes", []string{
	 "/v1/auth/user/login",
	 "/v1/auth/user/register",
	 "/v1/oauth",
	})

// 读白名单
var routes []string

	if err := discovery.Get("gateway/public_routes", &routes); err != nil {
	 // key 不存在时用本地默认值
	 routes = []string{"/v1/auth/user/login", "/v1/auth/user/register"}
	}

// watch 白名单变化（多网关实时同步）

	cancel, _ := discovery.WatchKV("gateway/public_routes", func(key, value string) {
	 var newRoutes []string
	 json.Unmarshal([]byte(value), &newRoutes)
	 // 更新内存中的白名单
	 updatePublicRoutes(newRoutes)
	})

defer cancel()
*/
package etcd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// KVPrefix 配置中心默认前缀
const KVPrefix = "/config/"

// Put 存储键值对，value 会被 JSON 序列化
func (d *Discovery) Put(key string, value any, opts ...clientv3.OpOption) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal value: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fullKey := KVPrefix + key
	_, err = d.client.Put(ctx, fullKey, string(data), opts...)
	return err
}

// Add appends string values to a JSON []string stored at key.
// It preserves existing order, skips duplicates, and uses an etcd transaction
// so concurrent Add calls do not overwrite each other's additions.
func (d *Discovery) Add(key string, value []string, opts ...clientv3.OpOption) error {
	fullKey := KVPrefix + key

	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		resp, err := d.client.Get(ctx, fullKey)
		cancel()
		if err != nil {
			return err
		}

		var current []string
		var cmp clientv3.Cmp
		if len(resp.Kvs) == 0 {
			cmp = clientv3.Compare(clientv3.CreateRevision(fullKey), "=", 0)
		} else {
			if err := json.Unmarshal(resp.Kvs[0].Value, &current); err != nil {
				return fmt.Errorf("unmarshal existing value: %w", err)
			}
			cmp = clientv3.Compare(clientv3.ModRevision(fullKey), "=", resp.Kvs[0].ModRevision)
		}

		next := appendStringSet(current, value)
		data, err := json.Marshal(next)
		if err != nil {
			return fmt.Errorf("marshal value: %w", err)
		}

		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		txnResp, err := d.client.Txn(ctx).If(cmp).Then(clientv3.OpPut(fullKey, string(data), opts...)).Commit()
		cancel()
		if err != nil {
			return err
		}
		if txnResp.Succeeded {
			return nil
		}
	}
}

// PutString 存储字符串值，跳过 JSON 序列化
func (d *Discovery) PutString(key string, value string, opts ...clientv3.OpOption) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fullKey := KVPrefix + key
	_, err := d.client.Put(ctx, fullKey, value, opts...)
	return err
}

func appendStringSet(current, additions []string) []string {
	if len(additions) == 0 {
		return current
	}

	seen := make(map[string]struct{}, len(current)+len(additions))
	for _, item := range current {
		seen[item] = struct{}{}
	}

	for _, item := range additions {
		if _, ok := seen[item]; ok {
			continue
		}
		current = append(current, item)
		seen[item] = struct{}{}
	}
	return current
}

// Get 获取值并 JSON 反序列化
func (d *Discovery) Get(key string, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fullKey := KVPrefix + key
	resp, err := d.client.Get(ctx, fullKey)
	if err != nil {
		return err
	}

	if len(resp.Kvs) == 0 {
		return fmt.Errorf("key not found: %s", fullKey)
	}

	return json.Unmarshal(resp.Kvs[0].Value, out)
}

// GetString 获取字符串值
func (d *Discovery) GetString(key string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fullKey := KVPrefix + key
	resp, err := d.client.Get(ctx, fullKey)
	if err != nil {
		return "", err
	}

	if len(resp.Kvs) == 0 {
		return "", fmt.Errorf("key not found: %s", fullKey)
	}

	return string(resp.Kvs[0].Value), nil
}

// GetPrefix 按前缀获取所有键值对，返回原始 map[string]string
func (d *Discovery) GetPrefix(prefix string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fullPrefix := KVPrefix + prefix
	resp, err := d.client.Get(ctx, fullPrefix, clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}

	result := make(map[string]string, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		// 去掉前缀，只保留相对 key
		relKey := string(kv.Key)[len(fullPrefix):]
		result[relKey] = string(kv.Value)
	}

	return result, nil
}

// Delete 删除键
func (d *Discovery) Delete(key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fullKey := KVPrefix + key
	_, err := d.client.Delete(ctx, fullKey)
	return err
}

// WatchKV 监听 key 变化，返回取消函数
// onChange 接收 key 和新值，删除时 value 为空
func (d *Discovery) WatchKV(key string, onChange func(key, value string)) (cancel func(), err error) {
	fullKey := KVPrefix + key

	ctx, cancelCtx := context.WithCancel(context.Background())

	// 先获取当前值
	getCtx, getCancel := context.WithTimeout(ctx, 5*time.Second)
	resp, err := d.client.Get(getCtx, fullKey)
	getCancel()
	if err != nil {
		cancelCtx()
		return nil, err
	}
	if len(resp.Kvs) > 0 {
		relKey := string(resp.Kvs[0].Key)[len(KVPrefix):]
		onChange(relKey, string(resp.Kvs[0].Value))
	}

	// 开始 watch
	watchCh := d.client.Watch(ctx, fullKey)

	go func() {
		for resp := range watchCh {
			if err := resp.Err(); err != nil {
				continue
			}
			for _, ev := range resp.Events {
				relKey := string(ev.Kv.Key)[len(KVPrefix):]
				switch ev.Type {
				case clientv3.EventTypePut:
					onChange(relKey, string(ev.Kv.Value))
				case clientv3.EventTypeDelete:
					onChange(relKey, "")
				}
			}
		}
	}()

	return cancelCtx, nil
}

// WatchPrefix 监听前缀下所有 key 变化
func (d *Discovery) WatchPrefix(prefix string, onChange func(key, value string)) (cancel func(), err error) {
	fullPrefix := KVPrefix + prefix

	ctx, cancelCtx := context.WithCancel(context.Background())

	// 先获取当前值
	getCtx, getCancel := context.WithTimeout(ctx, 5*time.Second)
	resp, err := d.client.Get(getCtx, fullPrefix, clientv3.WithPrefix())
	getCancel()
	if err != nil {
		cancelCtx()
		return nil, err
	}
	for _, kv := range resp.Kvs {
		relKey := string(kv.Key)[len(fullPrefix):]
		onChange(relKey, string(kv.Value))
	}

	// 开始 watch
	watchCh := d.client.Watch(ctx, fullPrefix, clientv3.WithPrefix())

	go func() {
		for resp := range watchCh {
			if err := resp.Err(); err != nil {
				continue
			}
			for _, ev := range resp.Events {
				relKey := string(ev.Kv.Key)[len(fullPrefix):]
				switch ev.Type {
				case clientv3.EventTypePut:
					onChange(relKey, string(ev.Kv.Value))
				case clientv3.EventTypeDelete:
					onChange(relKey, "")
				}
			}
		}
	}()

	return cancelCtx, nil
}
