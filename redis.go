package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/redis/go-redis/v9"
)

// =========================
// RedisClient 封装
// =========================
// 特点：
// 1. 统一 JSON 操作（sonic 序列化）
// 2. 默认超时控制
// 3. 常用操作封装
// 4. 与 core.New() 自动初始化，core.RConn() 全局访问
//
// 使用示例：
//
//	ctx := context.Background()
//
//	// 基础
//	_ = core.RConn().Set(ctx, "k1", "v1", time.Minute)
//	v, _ := core.RConn().Get(ctx, "k1")
//
//	// JSON
//	type User struct { Name string }
//	_ = core.RConn().SetJSON(ctx, "u:1", User{Name: "tom"})
//	var u User
//	_ = core.RConn().GetJSON(ctx, "u:1", &u)
//
//	// 原子计数
//	n, _ := core.RConn().Incr(ctx, "counter")
//
//	// Pipeline
//	_, _ = core.RConn().Pipelined(ctx, func(p redis.Pipeliner) error {
//	    p.Set(ctx, "a", 1, 0)
//	    p.Incr(ctx, "a")
//	    return nil
//	})
//
// 配置示例 (config.yaml):
//
//	redis:
//	  addr: "127.0.0.1:6379"
//	  password: ""
//	  db: 0
//	  pool_size: 100
//	  min_idle_conns: 10
//
// 多实例：
//
//	redis:
//	  default:
//	    addr: "127.0.0.1:6379"
//	    db: 0
//	  cache:
//	    addr: "127.0.0.1:6380"
//	    db: 1

type RedisClient struct {
	*redis.Client
}

// =========================
// 全局连接管理（类似 DB 的 conns / Conn 模式）
// =========================

var (
	redisConns = make(map[string]*RedisClient)
)

// NewRedis 根据配置初始化 Redis 连接
// 支持单实例和多实例两种配置格式
func NewRedis(conf Options, debug bool) (map[string]*RedisClient, error) {
	// 单实例配置：conf 中直接包含 addr
	if conf.GetString("addr") != "" {
		rdb, err := openRedis(conf, debug)
		if err != nil {
			return nil, err
		}
		redisConns["default"] = rdb
		return redisConns, nil
	}

	// 多实例配置：conf 中每个 key 是实例名
	for name, cfg := range conf {
		c, ok := cfg.(Options)
		if !ok {
			continue
		}
		rdb, err := openRedis(c, debug)
		if err != nil {
			return nil, fmt.Errorf("redis %s init failed: %w", name, err)
		}
		redisConns[name] = rdb
		D("Opened redis connection %s", name)
	}

	return redisConns, nil
}

func openRedis(conf Options, debug bool) (*RedisClient, error) {
	opts := &redis.Options{
		Addr:         conf.GetString("addr", "127.0.0.1:6379"),
		Password:     conf.GetString("password", ""),
		DB:           conf.GetInt("db", 0),
		PoolSize:     conf.GetInt("pool_size", 100),
		MinIdleConns: conf.GetInt("min_idle_conns", 10),
	}

	client := redis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, err
	}

	if debug {
		D("Redis connected %s", opts.Addr)
	}

	return &RedisClient{Client: client}, nil
}

// RConn 获取 Redis 连接（默认实例）
func RConn(name ...string) *RedisClient {
	key := "default"
	if len(name) > 0 {
		key = name[0]
	}
	if rdb, ok := redisConns[key]; ok {
		return rdb
	}
	Erro("Redis connect failed: %s", key)
	return nil
}

// CloseRedis 关闭所有 Redis 连接
func CloseRedis() {
	for name, rdb := range redisConns {
		if err := rdb.Close(); err != nil {
			Erro("Redis %s close error: %v", name, err)
		}
		delete(redisConns, name)
	}
}

// =========================
// 基础操作
// =========================

// Set 设置值（可选TTL）
func (r *RedisClient) Set(ctx context.Context, key string, value any, ttl ...time.Duration) error {
	exp := time.Duration(0)
	if len(ttl) > 0 {
		exp = ttl[0]
	}
	return r.Client.Set(ctx, key, value, exp).Err()
}

// Get 获取值（redis.Nil 返回空字符串不报错）
func (r *RedisClient) Get(ctx context.Context, key string) (string, error) {
	val, err := r.Client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return val, err
}

// GetInt 获取整数值
func (r *RedisClient) GetInt(ctx context.Context, key string) (int, error) {
	val, err := r.Get(ctx, key)
	if err != nil || val == "" {
		return 0, err
	}
	var n int
	if _, err := fmt.Sscanf(val, "%d", &n); err != nil {
		return 0, err
	}
	return n, nil
}

// Delete 删除
func (r *RedisClient) Delete(ctx context.Context, keys ...string) error {
	return r.Client.Del(ctx, keys...).Err()
}

// Exists 是否存在
func (r *RedisClient) Exists(ctx context.Context, key string) (bool, error) {
	n, err := r.Client.Exists(ctx, key).Result()
	return n > 0, err
}

// Expire 设置过期时间
func (r *RedisClient) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return r.Client.Expire(ctx, key, ttl).Err()
}

// TTL 获取剩余过期时间
func (r *RedisClient) TTL(ctx context.Context, key string) (time.Duration, error) {
	return r.Client.TTL(ctx, key).Result()
}

// =========================
// JSON 操作
// =========================

// SetJSON 自动序列化存储
func (r *RedisClient) SetJSON(ctx context.Context, key string, value any, ttl ...time.Duration) error {
	b, err := sonic.Marshal(value)
	if err != nil {
		Erro("Redis SetJSON marshal error: %v", err)
		return err
	}
	return r.Set(ctx, key, b, ttl...)
}

// GetJSON 自动反序列化读取
func (r *RedisClient) GetJSON(ctx context.Context, key string, v any) error {
	val, err := r.Get(ctx, key)
	if err != nil || val == "" {
		return err
	}
	return sonic.UnmarshalString(val, v)
}

// =========================
// 计数器
// =========================

// Incr 原子递增
func (r *RedisClient) Incr(ctx context.Context, key string) (int64, error) {
	return r.Client.Incr(ctx, key).Result()
}

// Decr 原子递减
func (r *RedisClient) Decr(ctx context.Context, key string) (int64, error) {
	return r.Client.Decr(ctx, key).Result()
}

// IncrBy 原子增减指定值
func (r *RedisClient) IncrBy(ctx context.Context, key string, value int64) (int64, error) {
	return r.Client.IncrBy(ctx, key, value).Result()
}

// IncrWithTTL 递增并设置过期（常用于限流）
func (r *RedisClient) IncrWithTTL(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	pipe := r.TxPipeline()
	incr := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, ttl)
	_, err := pipe.Exec(ctx)
	return incr.Val(), err
}

// =========================
// Hash
// =========================

// HSet 设置 Hash 字段
func (r *RedisClient) HSet(ctx context.Context, key string, values ...any) error {
	return r.Client.HSet(ctx, key, values...).Err()
}

// HGet 获取 Hash 字段值
func (r *RedisClient) HGet(ctx context.Context, key, field string) (string, error) {
	return r.Client.HGet(ctx, key, field).Result()
}

// HGetAll 获取 Hash 所有字段
func (r *RedisClient) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	return r.Client.HGetAll(ctx, key).Result()
}

// HDel 删除 Hash 字段
func (r *RedisClient) HDel(ctx context.Context, key string, fields ...string) error {
	return r.Client.HDel(ctx, key, fields...).Err()
}

// HExists 判断 Hash 字段是否存在
func (r *RedisClient) HExists(ctx context.Context, key, field string) (bool, error) {
	return r.Client.HExists(ctx, key, field).Result()
}

// HIncrBy Hash 字段原子递增
func (r *RedisClient) HIncrBy(ctx context.Context, key, field string, incr int64) (int64, error) {
	return r.Client.HIncrBy(ctx, key, field, incr).Result()
}

// HLen Hash 字段数量
func (r *RedisClient) HLen(ctx context.Context, key string) (int64, error) {
	return r.Client.HLen(ctx, key).Result()
}

// HSetJSON Hash 字段 JSON 序列化存储
func (r *RedisClient) HSetJSON(ctx context.Context, key, field string, value any) error {
	b, err := sonic.Marshal(value)
	if err != nil {
		return err
	}
	return r.Client.HSet(ctx, key, field, b).Err()
}

// HGetJSON Hash 字段 JSON 反序列化读取
func (r *RedisClient) HGetJSON(ctx context.Context, key, field string, v any) error {
	val, err := r.HGet(ctx, key, field)
	if err != nil || val == "" {
		return err
	}
	return sonic.UnmarshalString(val, v)
}

// =========================
// Set（集合）
// =========================

// SAdd 添加成员到集合
func (r *RedisClient) SAdd(ctx context.Context, key string, members ...any) error {
	return r.Client.SAdd(ctx, key, members...).Err()
}

// SMembers 获取集合所有成员
func (r *RedisClient) SMembers(ctx context.Context, key string) ([]string, error) {
	return r.Client.SMembers(ctx, key).Result()
}

// SRem 从集合移除成员
func (r *RedisClient) SRem(ctx context.Context, key string, members ...any) error {
	return r.Client.SRem(ctx, key, members...).Err()
}

// SIsMember 判断是否为集合成员
func (r *RedisClient) SIsMember(ctx context.Context, key string, member any) (bool, error) {
	return r.Client.SIsMember(ctx, key, member).Result()
}

// SCard 集合成员数量
func (r *RedisClient) SCard(ctx context.Context, key string) (int64, error) {
	return r.Client.SCard(ctx, key).Result()
}

// =========================
// Sorted Set（有序集合）
// =========================

// ZAdd 添加成员到有序集合
func (r *RedisClient) ZAdd(ctx context.Context, key string, members ...redis.Z) error {
	return r.Client.ZAdd(ctx, key, members...).Err()
}

// ZRangeByScore 按分数范围获取有序集合成员
func (r *RedisClient) ZRangeByScore(ctx context.Context, key string, opt *redis.ZRangeBy) ([]string, error) {
	return r.Client.ZRangeByScore(ctx, key, opt).Result()
}

// ZRevRangeByScore 按分数范围倒序获取有序集合成员
func (r *RedisClient) ZRevRangeByScore(ctx context.Context, key string, opt *redis.ZRangeBy) ([]string, error) {
	return r.Client.ZRevRangeByScore(ctx, key, opt).Result()
}

// ZRem 移除有序集合成员
func (r *RedisClient) ZRem(ctx context.Context, key string, members ...any) error {
	return r.Client.ZRem(ctx, key, members...).Err()
}

// ZCard 有序集合成员数量
func (r *RedisClient) ZCard(ctx context.Context, key string) (int64, error) {
	return r.Client.ZCard(ctx, key).Result()
}

// ZScore 获取有序集合成员分数
func (r *RedisClient) ZScore(ctx context.Context, key string, member string) (float64, error) {
	return r.Client.ZScore(ctx, key, member).Result()
}

// ZIncrBy 有序集合成员分数递增
func (r *RedisClient) ZIncrBy(ctx context.Context, key string, increment float64, member string) (float64, error) {
	return r.Client.ZIncrBy(ctx, key, increment, member).Result()
}

// =========================
// List（列表）
// =========================

// LPush 左侧入队
func (r *RedisClient) LPush(ctx context.Context, key string, values ...any) error {
	return r.Client.LPush(ctx, key, values...).Err()
}

// RPush 右侧入队
func (r *RedisClient) RPush(ctx context.Context, key string, values ...any) error {
	return r.Client.RPush(ctx, key, values...).Err()
}

// LPop 左侧出队
func (r *RedisClient) LPop(ctx context.Context, key string) (string, error) {
	val, err := r.Client.LPop(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return val, err
}

// RPop 右侧出队
func (r *RedisClient) RPop(ctx context.Context, key string) (string, error) {
	val, err := r.Client.RPop(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return val, err
}

// LRange 获取列表范围
func (r *RedisClient) LRange(ctx context.Context, key string, start, stop int64) ([]string, error) {
	return r.Client.LRange(ctx, key, start, stop).Result()
}

// LLen 列表长度
func (r *RedisClient) LLen(ctx context.Context, key string) (int64, error) {
	return r.Client.LLen(ctx, key).Result()
}

// =========================
// BitMap（位图）
// =========================

// SetBit 设置位
func (r *RedisClient) SetBit(ctx context.Context, key string, offset int64, value int) error {
	return r.Client.SetBit(ctx, key, offset, value).Err()
}

// GetBit 获取位
func (r *RedisClient) GetBit(ctx context.Context, key string, offset int64) (int64, error) {
	return r.Client.GetBit(ctx, key, offset).Result()
}

// BitCount 统计位为1的数量
func (r *RedisClient) BitCount(ctx context.Context, key string, bitCount *redis.BitCount) (int64, error) {
	return r.Client.BitCount(ctx, key, bitCount).Result()
}

// =========================
// Scan（迭代扫描）
// =========================

// Scan 扫描匹配的 key
func (r *RedisClient) Scan(ctx context.Context, cursor uint64, match string, count int64) *redis.ScanCmd {
	return r.Client.Scan(ctx, cursor, match, count)
}

// =========================
// Pipeline / Transaction
// =========================

// Pipeline 获取管道
func (r *RedisClient) Pipeline() redis.Pipeliner {
	return r.Client.Pipeline()
}

// Pipelined 执行管道操作
func (r *RedisClient) Pipelined(ctx context.Context, fn func(redis.Pipeliner) error) ([]redis.Cmder, error) {
	return r.Client.Pipelined(ctx, fn)
}

// TxPipeline 获取事务管道
func (r *RedisClient) TxPipeline() redis.Pipeliner {
	return r.Client.TxPipeline()
}

// =========================
// Lua 脚本
// =========================

// Eval 执行 Lua 脚本
func (r *RedisClient) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	return r.Client.Eval(ctx, script, keys, args...).Result()
}

// EvalSha 执行 Lua 脚本（通过 SHA1）
func (r *RedisClient) EvalSha(ctx context.Context, sha1 string, keys []string, args ...any) (any, error) {
	return r.Client.EvalSha(ctx, sha1, keys, args...).Result()
}

// ScriptLoad 预加载 Lua 脚本
func (r *RedisClient) ScriptLoad(ctx context.Context, script string) (string, error) {
	return r.Client.ScriptLoad(ctx, script).Result()
}

// =========================
// Pub/Sub
// =========================

// Publish 发布消息
func (r *RedisClient) Publish(ctx context.Context, channel string, message any) error {
	return r.Client.Publish(ctx, channel, message).Err()
}

// Subscribe 订阅频道（阻塞式）
func (r *RedisClient) Subscribe(ctx context.Context, channel string, handler func(string)) error {
	pubsub := r.Client.Subscribe(ctx, channel)
	defer pubsub.Close()

	for msg := range pubsub.Channel() {
		handler(msg.Payload)
	}
	return nil
}

// =========================
// Stream（流）
// =========================
//
// Redis Stream 适用于：事件驱动、消息队列、实时通知、审计日志等场景。
// 与 Pub/Sub 的区别：Stream 持久化存储、支持消费者组、可回溯、确保至少一次投递。
//
// 快速开始：
//
//	// 发布
//	id, _ := rdb.XAdd(ctx, "orders", map[string]any{"type": "created", "order_id": "123"})
//
//	// 消费（消费者组模式，推荐）
//	consumer := rdb.NewStreamConsumer(StreamConsumerConfig{
//	    Stream:   "orders",
//	    Group:    "order-workers",
//	    Consumer: "worker-1",
//	})
//	consumer.Run(ctx, func(ctx context.Context, msg StreamMessage) error {
//	    // 处理消息
//	    return nil  // 返回 nil 自动 ACK
//	})

// StreamMessage 表示一条 Stream 消息
 type StreamMessage struct {
	ID     string         // 消息 ID (如 "1672531200000-0")
	Fields map[string]any // 消息字段
}

// XAdd 向 Stream 添加消息
// 返回消息 ID
func (r *RedisClient) XAdd(ctx context.Context, stream string, fields map[string]any, id ...string) (string, error) {
	msgID := "*" // 自动生成 ID
	if len(id) > 0 && id[0] != "" {
		msgID = id[0]
	}
	return r.Client.XAdd(ctx, &redis.XAddArgs{
		Stream: stream,
		Values: fields,
		ID:     msgID,
	}).Result()
}

// XAddJSON 向 Stream 添加 JSON 消息
// 将 value 序列化为 JSON 存入 _data 字段
func (r *RedisClient) XAddJSON(ctx context.Context, stream string, value any, id ...string) (string, error) {
	b, err := sonic.Marshal(value)
	if err != nil {
		return "", err
	}
	return r.XAdd(ctx, stream, map[string]any{"_data": b}, id...)
}

// XRead 从 Stream 读取消息（非消费者组模式）
func (r *RedisClient) XRead(ctx context.Context, stream string, startID string, count ...int64) ([]StreamMessage, error) {
	cnt := int64(10)
	if len(count) > 0 && count[0] > 0 {
		cnt = count[0]
	}

	result, err := r.Client.XRead(ctx, &redis.XReadArgs{
		Streams: []string{stream, startID},
		Count:   cnt,
		Block:   0, // 非阻塞
	}).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, err
	}

	var msgs []StreamMessage
	for _, xstream := range result {
		for _, msg := range xstream.Messages {
			msgs = append(msgs, StreamMessage{ID: msg.ID, Fields: msg.Values})
		}
	}
	return msgs, nil
}

// XRange 按 ID 范围读取消息
func (r *RedisClient) XRange(ctx context.Context, stream, start, stop string, count ...int64) ([]StreamMessage, error) {
	var result []redis.XMessage
	var err error

	if len(count) > 0 && count[0] > 0 {
		result, err = r.Client.XRangeN(ctx, stream, start, stop, count[0]).Result()
	} else {
		result, err = r.Client.XRange(ctx, stream, start, stop).Result()
	}
	if err != nil {
		return nil, err
	}

	msgs := make([]StreamMessage, len(result))
	for i, msg := range result {
		msgs[i] = StreamMessage{ID: msg.ID, Fields: msg.Values}
	}
	return msgs, nil
}

// XLen 获取 Stream 长度
func (r *RedisClient) XLen(ctx context.Context, stream string) (int64, error) {
	return r.Client.XLen(ctx, stream).Result()
}

// XTrim 按 maxlen 裁剪 Stream
func (r *RedisClient) XTrim(ctx context.Context, stream string, maxLen int64) error {
	return r.Client.XTrimMaxLen(ctx, stream, maxLen).Err()
}

// XDel 删除 Stream 中的消息
func (r *RedisClient) XDel(ctx context.Context, stream string, ids ...string) (int64, error) {
	return r.Client.XDel(ctx, stream, ids...).Result()
}

// XGroupCreate 创建消费者组
// startID: "0" 从头消费, "$" 只消费新消息
func (r *RedisClient) XGroupCreate(ctx context.Context, stream, group, startID string) error {
	return r.Client.XGroupCreateMkStream(ctx, stream, group, startID).Err()
}

// XGroupDestroy 删除消费者组
func (r *RedisClient) XGroupDestroy(ctx context.Context, stream, group string) error {
	return r.Client.XGroupDestroy(ctx, stream, group).Err()
}

// XAck 确认消息已处理
func (r *RedisClient) XAck(ctx context.Context, stream, group string, ids ...string) error {
	return r.Client.XAck(ctx, stream, group, ids...).Err()
}

// XPending 获取消费者组的待处理消息信息
func (r *RedisClient) XPending(ctx context.Context, stream, group string) (*redis.XPending, error) {
	return r.Client.XPending(ctx, stream, group).Result()
}

// XClaim 认领待处理消息（用于故障恢复）
func (r *RedisClient) XClaim(ctx context.Context, stream, group, consumer string, minIdleTime time.Duration, ids ...string) ([]StreamMessage, error) {
	result, err := r.Client.XClaim(ctx, &redis.XClaimArgs{
		Stream:   stream,
		Group:    group,
		Consumer: consumer,
		MinIdle:  minIdleTime,
		Messages: ids,
	}).Result()
	if err != nil {
		return nil, err
	}

	msgs := make([]StreamMessage, len(result))
	for i, msg := range result {
		msgs[i] = StreamMessage{ID: msg.ID, Fields: msg.Values}
	}
	return msgs, nil
}

// XReadGroup 从消费者组读取消息
func (r *RedisClient) XReadGroup(ctx context.Context, stream, group, consumer string, count int64, block time.Duration) ([]StreamMessage, error) {
	result, err := r.Client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    group,
		Consumer: consumer,
		Streams:  []string{stream, ">"},
		Count:    count,
		Block:    block,
	}).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, err
	}

	var msgs []StreamMessage
	for _, xstream := range result {
		for _, msg := range xstream.Messages {
			msgs = append(msgs, StreamMessage{ID: msg.ID, Fields: msg.Values})
		}
	}
	return msgs, nil
}

// =========================
// StreamConsumer — 消费者组模式封装
// =========================
//
// 特性：
//   - 自动创建消费者组
//   - 阻塞读取 + 消费者组模式
//   - 处理成功自动 ACK，失败跳过（可配置重试）
//   - 待处理消息自动认领（minIdleTime 超时后 XCLAIM）
//   - 优雅关闭（context cancel）
//
// 使用示例：
//
//	consumer := rdb.NewStreamConsumer(StreamConsumerConfig{
//	    Stream:      "orders",
//	    Group:       "order-workers",
//	    Consumer:    "worker-1",
//	    Count:       10,
//	    Block:       5 * time.Second,
//	    ClaimIdle:   30 * time.Second,
//	    ClaimCount:  10,
//	})
//
//	err := consumer.Run(ctx, func(ctx context.Context, msg StreamMessage) error {
//	    // 处理消息，返回 nil 自动 ACK
//	    // 返回 error 不 ACK，消息会进入 pending 列表
//	    return nil
//	})

type StreamConsumerConfig struct {
	Stream     string        // Stream 名称
	Group      string        // 消费者组名称
	Consumer   string        // 消费者名称
	Count      int64         // 每次读取条数（默认 10）
	Block      time.Duration // 阻塞等待时间（默认 5s）
	ClaimIdle  time.Duration // 认领待处理消息的最小空闲时间（默认 30s，0 禁用）
	ClaimCount int64         // 每次认领的最大条数（默认 10）
	AutoAck    bool          // 处理成功是否自动 ACK（默认 true）
}

type StreamConsumer struct {
	rdb  *RedisClient
	conf StreamConsumerConfig
}

// NewStreamConsumer 创建 Stream 消费者
func (r *RedisClient) NewStreamConsumer(conf StreamConsumerConfig) *StreamConsumer {
	if conf.Count <= 0 {
		conf.Count = 10
	}
	if conf.Block <= 0 {
		conf.Block = 5 * time.Second
	}
	if conf.ClaimIdle == 0 {
		conf.ClaimIdle = 30 * time.Second
	}
	if conf.ClaimCount <= 0 {
		conf.ClaimCount = 10
	}
	return &StreamConsumer{rdb: r, conf: conf}
}

// Run 启动消费者（阻塞运行，直到 context 取消）
// handler 返回 nil 自动 ACK，返回 error 不 ACK（进入 pending 列表等待重试）
func (c *StreamConsumer) Run(ctx context.Context, handler func(ctx context.Context, msg StreamMessage) error) error {
	// 自动创建消费者组
	if err := c.rdb.XGroupCreate(ctx, c.conf.Stream, c.conf.Group, "0"); err != nil {
		// BUSYGROUP already exists，忽略
		if !strings.Contains(err.Error(), "BUSYGROUP") {
			return fmt.Errorf("create consumer group failed: %w", err)
		}
		D("Stream consumer group %s already exists", c.conf.Group)
	} else {
		D("Stream consumer group %s created", c.conf.Group)
	}

	Info("Stream consumer started: stream=%s group=%s consumer=%s",
		c.conf.Stream, c.conf.Group, c.conf.Consumer)

	// 认领待处理消息的 ticker
	var claimTicker *time.Ticker
	if c.conf.ClaimIdle > 0 {
		claimTicker = time.NewTicker(c.conf.ClaimIdle)
		defer claimTicker.Stop()
	}

	for {
		select {
		case <-ctx.Done():
			Info("Stream consumer stopped: stream=%s consumer=%s", c.conf.Stream, c.conf.Consumer)
			return ctx.Err()

		case <-claimTicker.C:
			// 认领超时的待处理消息
			c.claimPending(ctx, handler)

		default:
			// 阻塞读取新消息
			msgs, err := c.rdb.XReadGroup(ctx, c.conf.Stream, c.conf.Group,
				c.conf.Consumer, c.conf.Count, c.conf.Block)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				Erro("Stream XReadGroup error: %v", err)
				time.Sleep(time.Second)
				continue
			}

			if len(msgs) == 0 {
				continue
			}

			for _, msg := range msgs {
				if err := handler(ctx, msg); err != nil {
					Erro("Stream handler error: stream=%s id=%s err=%v",
						c.conf.Stream, msg.ID, err)
					// 不 ACK，进入 pending 列表等待 claim 重试
					continue
				}

				if c.conf.AutoAck {
					if err := c.rdb.XAck(ctx, c.conf.Stream, c.conf.Group, msg.ID); err != nil {
						Erro("Stream ACK error: stream=%s id=%s err=%v",
							c.conf.Stream, msg.ID, err)
					}
				}
			}
		}
	}
}

// claimPending 认领超时未确认的待处理消息
func (c *StreamConsumer) claimPending(ctx context.Context, handler func(ctx context.Context, msg StreamMessage) error) {
	// 获取 pending 消息概要
	pending, err := c.rdb.XPending(ctx, c.conf.Stream, c.conf.Group)
	if err != nil || pending == nil || pending.Count == 0 {
		return
	}

	// 获取 pending 消息 ID 范围
	// 从 pending 列表获取具体的待处理条目
	pendingExt, err := c.rdb.Client.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream:   c.conf.Stream,
		Group:    c.conf.Group,
		Start:    "-",
		End:      "+",
		Count:    c.conf.ClaimCount,
		Idle:     c.conf.ClaimIdle,
	}).Result()
	if err != nil || len(pendingExt) == 0 {
		return
	}

	var ids []string
	for _, p := range pendingExt {
		ids = append(ids, p.ID)
	}

	// 认领这些消息
	msgs, err := c.rdb.XClaim(ctx, c.conf.Stream, c.conf.Group,
		c.conf.Consumer, c.conf.ClaimIdle, ids...)
	if err != nil || len(msgs) == 0 {
		return
	}

	D("Stream claimed %d pending messages: stream=%s group=%s",
		len(msgs), c.conf.Stream, c.conf.Group)

	for _, msg := range msgs {
		if err := handler(ctx, msg); err != nil {
			Erro("Stream claim handler error: stream=%s id=%s err=%v",
				c.conf.Stream, msg.ID, err)
			continue
		}

		if c.conf.AutoAck {
			c.rdb.XAck(ctx, c.conf.Stream, c.conf.Group, msg.ID)
		}
	}
}

// =========================
// 工具
// =========================

// Close 关闭连接
func (r *RedisClient) Close() error {
	return r.Client.Close()
}

// Ping 检测连接
func (r *RedisClient) Ping(ctx context.Context) error {
	return r.Client.Ping(ctx).Err()
}
