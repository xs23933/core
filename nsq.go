package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/nsqio/go-nsq"
)

// =========================
// NSQ 封装
// =========================
// 特点：
// 1. Producer/Consumer 一站式封装
// 2. JSON 消息自动序列化/反序列化（sonic）
// 3. Consumer 支持优雅关闭、并发处理、自动重连
// 4. 与 core.New() 自动初始化，NProducer() 全局访问
// 5. Consumer 通过 NSQConsumer() 创建，支持 lookupd/nsqd 直连
//
// 使用示例：
//
//	// 发布
//	_ = core.NProducer().Publish("order_events", []byte("hello"))
//	_ = core.NProducer().PublishJSON("order_events", Order{ID: "123"})
//
//	// 消费
//	consumer := core.NewNSQConsumer(core.NSQConsumerConfig{
//	    Topic:       "order_events",
//	    Channel:     "worker",
//	    Lookupds:    []string{"127.0.0.1:4161"},
//	    Concurrency: 5,
//	})
//	consumer.Start(func(msg *nsq.Message) error {
//	    // 处理消息，返回 nil 表示 FIN，返回 error 表示 REQ
//	    return nil
//	})
//
// 配置示例 (config.yaml):
//
//	nsq:
//	  nsqd: "127.0.0.1:4150"
//	  lookupd: "127.0.0.1:4161"
//	  max_in_flight: 200
//	  default_requeue_delay: 90s
//	  max_attempts: 5

// =========================
// 全局 Producer 管理
// =========================

var (
	nsqProducer     *NSQProducer
	nsqProducerOnce sync.Once
	nsqConsumers    []*NSQConsumer
	nsqMu           sync.Mutex
)

// NSQProducer 封装 nsq.Producer
type NSQProducer struct {
	*nsq.Producer
	addr string
}

// NewNSQProducer 创建 Producer
func NewNSQProducer(addr string, cfg *nsq.Config) (*NSQProducer, error) {
	if cfg == nil {
		cfg = nsq.NewConfig()
	}
	p, err := nsq.NewProducer(addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("nsq producer create failed: %w", err)
	}
	if err := p.Ping(); err != nil {
		return nil, fmt.Errorf("nsq producer ping %s failed: %w", addr, err)
	}
	D("NSQ producer connected %s", addr)
	return &NSQProducer{Producer: p, addr: addr}, nil
}

// NProducer 获取全局 NSQ Producer
func NProducer() *NSQProducer {
	return nsqProducer
}

// InitNSQ 初始化 NSQ（从配置自动创建 Producer）
func InitNSQ(conf Options, debug bool) error {
	addr := conf.GetString("nsqd", "127.0.0.1:4150")

	cfg := nsq.NewConfig()

	// 可选配置
	if v := conf.GetInt("max_in_flight", 0); v > 0 {
		cfg.MaxInFlight = v
	}
	if v := conf.GetInt("default_requeue_delay_sec", 0); v > 0 {
		cfg.DefaultRequeueDelay = time.Duration(v) * time.Second
	}
	if v := conf.GetInt("max_attempts", 0); v > 0 {
		cfg.MaxAttempts = uint16(v)
	}
	if v := conf.GetString("client_id", ""); v != "" {
		cfg.ClientID = v
	}
	if v := conf.GetString("user_agent", ""); v != "" {
		cfg.UserAgent = v
	}
	if v := conf.GetString("auth_secret", ""); v != "" {
		cfg.AuthSecret = v
	}

	p, err := NewNSQProducer(addr, cfg)
	if err != nil {
		return err
	}
	nsqProducer = p
	D("NSQ initialized, nsqd=%s", addr)
	return nil
}

// =========================
// Producer 方法
// =========================

// Publish 发布消息
func (p *NSQProducer) Publish(topic string, body []byte) error {
	return p.Producer.Publish(topic, body)
}

// PublishJSON 发布 JSON 消息（自动序列化）
func (p *NSQProducer) PublishJSON(topic string, v any) error {
	b, err := sonic.Marshal(v)
	if err != nil {
		Erro("NSQ PublishJSON marshal error: %v", err)
		return err
	}
	return p.Publish(topic, b)
}

// PublishString 发布字符串消息
func (p *NSQProducer) PublishString(topic, message string) error {
	return p.Publish(topic, []byte(message))
}

// DeferredPublish 延迟发布消息
func (p *NSQProducer) DeferredPublish(topic string, delay time.Duration, body []byte) error {
	return p.Producer.DeferredPublish(topic, delay, body)
}

// DeferredPublishJSON 延迟发布 JSON 消息
func (p *NSQProducer) DeferredPublishJSON(topic string, delay time.Duration, v any) error {
	b, err := sonic.Marshal(v)
	if err != nil {
		return err
	}
	return p.DeferredPublish(topic, delay, b)
}

// MultiPublish 批量发布消息到同一 topic
func (p *NSQProducer) MultiPublish(topic string, bodies [][]byte) error {
	return p.Producer.MultiPublish(topic, bodies)
}

// Ping 检测连接
func (p *NSQProducer) Ping() error {
	return p.Producer.Ping()
}

// Stop 停止 Producer
func (p *NSQProducer) Stop() {
	p.Producer.Stop()
	D("NSQ producer stopped %s", p.addr)
}

// =========================
// Consumer
// =========================

// NSQConsumerConfig 消费者配置
type NSQConsumerConfig struct {
	Topic       string        // Topic 名称（必填）
	Channel     string        // Channel 名称（必填）
	NSQDs       []string      // 直连 nsqd 地址列表（如 ["127.0.0.1:4150"]）
	Lookupds    []string      // nsqlookupd 地址列表（如 ["127.0.0.1:4161"]）
	Concurrency int           // 并发处理数（默认 1）
	MaxInFlight int           // 最大在途消息数（默认 200）
	MaxAttempts uint16        // 最大重试次数（默认 5）
	MaxBackoff  time.Duration // 最大退避时间（默认 2m）
	MsgTimeout  time.Duration // 消息处理超时（0 使用服务端默认）
}

// NSQConsumer 封装 nsq.Consumer
type NSQConsumer struct {
	consumer *nsq.Consumer
	config   NSQConsumerConfig
	handler  nsq.HandlerFunc
	stopOnce sync.Once
}

// NewNSQConsumer 创建 NSQ Consumer
func NewNSQConsumer(conf NSQConsumerConfig) (*NSQConsumer, error) {
	if conf.Topic == "" || conf.Channel == "" {
		return nil, fmt.Errorf("nsq consumer: topic(%s) and channel(%s) are required", conf.Topic, conf.Channel)
	}
	if conf.Concurrency <= 0 {
		conf.Concurrency = 1
	}
	if conf.MaxInFlight <= 0 {
		conf.MaxInFlight = 200
	}
	if conf.MaxAttempts <= 0 {
		conf.MaxAttempts = 5
	}
	if conf.MaxBackoff <= 0 {
		conf.MaxBackoff = 2 * time.Minute
	}

	cfg := nsq.NewConfig()
	cfg.MaxInFlight = conf.MaxInFlight
	cfg.MaxAttempts = conf.MaxAttempts
	cfg.MaxBackoffDuration = conf.MaxBackoff
	if conf.MsgTimeout > 0 {
		cfg.MsgTimeout = conf.MsgTimeout
	}

	c, err := nsq.NewConsumer(conf.Topic, conf.Channel, cfg)
	if err != nil {
		return nil, fmt.Errorf("nsq consumer create failed: %w", err)
	}

	return &NSQConsumer{
		consumer: c,
		config:   conf,
	}, nil
}

// NSQHandlerFunc 消息处理函数
// 返回 nil → FIN（确认处理完成）
// 返回 error → REQ（重新入队，自动退避重试）
type NSQHandlerFunc func(msg *nsq.Message) error

// Start 启动消费者（阻塞直到 Stop 或连接断开）
// handler 返回 nil 确认消息，返回 error 重新入队
func (nc *NSQConsumer) Start(handler NSQHandlerFunc) error {
	nc.handler = nsq.HandlerFunc(handler)
	nc.consumer.AddConcurrentHandlers(nc.handler, nc.config.Concurrency)

	lookupds, nsqds, err := nsqConsumerConnectionTargets(nc.config)
	if err != nil {
		return err
	}

	// 连接 nsqlookupd（推荐，自动发现 nsqd 节点）
	if len(lookupds) > 0 {
		for _, addr := range lookupds {
			if err := nc.consumer.ConnectToNSQLookupd(addr); err != nil {
				return fmt.Errorf("nsq consumer connect to lookupd %s failed: %w", addr, err)
			}
			D("NSQ consumer connected to lookupd %s", addr)
		}
	}

	// 直连 nsqd（适用于单节点或测试环境）
	if len(nsqds) > 0 {
		if err := nc.consumer.ConnectToNSQDs(nsqds); err != nil {
			return fmt.Errorf("nsq consumer connect to nsqd failed: %w", err)
		}
		D("NSQ consumer connected to nsqd %v", nsqds)
	}

	// 注册全局 consumer，方便关闭时统一清理
	nsqMu.Lock()
	nsqConsumers = append(nsqConsumers, nc)
	nsqMu.Unlock()

	Info("NSQ consumer started: topic=%s channel=%s concurrency=%d",
		nc.config.Topic, nc.config.Channel, nc.config.Concurrency)

	// 阻塞等待
	<-nc.consumer.StopChan

	Info("NSQ consumer stopped: topic=%s channel=%s", nc.config.Topic, nc.config.Channel)
	return nil
}

// StartAsync 异步启动消费者
func (nc *NSQConsumer) StartAsync(handler NSQHandlerFunc) error {
	nc.handler = nsq.HandlerFunc(handler)
	nc.consumer.AddConcurrentHandlers(nc.handler, nc.config.Concurrency)

	lookupds, nsqds, err := nsqConsumerConnectionTargets(nc.config)
	if err != nil {
		return err
	}

	if len(lookupds) > 0 {
		for _, addr := range lookupds {
			if err := nc.consumer.ConnectToNSQLookupd(addr); err != nil {
				return fmt.Errorf("nsq consumer connect to lookupd %s failed: %w", addr, err)
			}
		}
	}

	if len(nsqds) > 0 {
		if err := nc.consumer.ConnectToNSQDs(nsqds); err != nil {
			return fmt.Errorf("nsq consumer connect to nsqd failed: %w", err)
		}
	}

	nsqMu.Lock()
	nsqConsumers = append(nsqConsumers, nc)
	nsqMu.Unlock()

	Info("NSQ consumer started (async): topic=%s channel=%s concurrency=%d",
		nc.config.Topic, nc.config.Channel, nc.config.Concurrency)
	return nil
}

func nsqConsumerConnectionTargets(conf NSQConsumerConfig) (lookupds []string, nsqds []string, err error) {
	if len(conf.Lookupds) > 0 {
		if len(conf.NSQDs) > 0 {
			D("NSQ consumer using lookupd %v; ignoring direct nsqd %v", conf.Lookupds, conf.NSQDs)
		}
		return conf.Lookupds, nil, nil
	}
	if len(conf.NSQDs) > 0 {
		return nil, conf.NSQDs, nil
	}
	return nil, nil, fmt.Errorf("nsq consumer: at least one lookupd(%v) or nsqd address(%v) required", conf.Lookupds, conf.NSQDs)
}

// Stop 优雅停止消费者
func (nc *NSQConsumer) Stop() {
	nc.stopOnce.Do(func() {
		nc.consumer.Stop()
		D("NSQ consumer stopping: topic=%s channel=%s", nc.config.Topic, nc.config.Channel)
	})
}

// Stats 获取消费者统计
func (nc *NSQConsumer) Stats() *nsq.ConsumerStats {
	return nc.consumer.Stats()
}

// ChangeMaxInFlight 动态调整最大在途消息数
func (nc *NSQConsumer) ChangeMaxInFlight(maxInFlight int) {
	nc.consumer.ChangeMaxInFlight(maxInFlight)
}

// IsStarved 判断消费者是否饥饿（可用于背压）
func (nc *NSQConsumer) IsStarved() bool {
	return nc.consumer.IsStarved()
}

// =========================
// JSON 消息解析辅助
// =========================

// NSQDecodeJSON 从 nsq.Message.Body 解析 JSON
func NSQDecodeJSON(msg *nsq.Message, v any) error {
	return sonic.Unmarshal(msg.Body, v)
}

// NSQDecodeStdJSON 从 nsq.Message.Body 解析 JSON（标准库）
func NSQDecodeStdJSON(msg *nsq.Message, v any) error {
	return json.Unmarshal(msg.Body, v)
}

// NSQMessage 泛型消息解析辅助
// 用法：msg, _ := core.NSQParse[Order](nsqMsg)
func NSQParse[T any](msg *nsq.Message) (*T, error) {
	var v T
	if err := sonic.Unmarshal(msg.Body, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// =========================
// 全局关闭
// =========================

// CloseNSQ 关闭所有 NSQ 连接
func CloseNSQ() {
	if nsqProducer != nil {
		nsqProducer.Stop()
		nsqProducer = nil
	}

	nsqMu.Lock()
	defer nsqMu.Unlock()
	for _, c := range nsqConsumers {
		c.Stop()
	}
	nsqConsumers = nil
	D("NSQ all connections closed")
}

// =========================
// 便捷 API：快速发布消息（无需关心 Producer 实例）
// =========================

// NSQPublish 快速发布消息到指定 topic
func NSQPublish(topic string, body []byte) error {
	if nsqProducer == nil {
		return fmt.Errorf("nsq producer not initialized")
	}
	return nsqProducer.Publish(topic, body)
}

// NSQPublishJSON 快速发布 JSON 消息
func NSQPublishJSON(topic string, v any) error {
	if nsqProducer == nil {
		return fmt.Errorf("nsq producer not initialized")
	}
	return nsqProducer.PublishJSON(topic, v)
}

// NSQPublishString 快速发布字符串消息
func NSQPublishString(topic, message string) error {
	if nsqProducer == nil {
		return fmt.Errorf("nsq producer not initialized")
	}
	return nsqProducer.PublishString(topic, message)
}

// NSQDeferredPublish 快速延迟发布
func NSQDeferredPublish(topic string, delay time.Duration, body []byte) error {
	if nsqProducer == nil {
		return fmt.Errorf("nsq producer not initialized")
	}
	return nsqProducer.DeferredPublish(topic, delay, body)
}

// NSQDeferredPublishJSON 快速延迟发布 JSON
func NSQDeferredPublishJSON(topic string, delay time.Duration, v any) error {
	if nsqProducer == nil {
		return fmt.Errorf("nsq producer not initialized")
	}
	return nsqProducer.DeferredPublishJSON(topic, delay, v)
}

// =========================
// NSQConsumerBuilder 链式创建消费者
// =========================

// NSQConsumerBuilder 消费者构建器
type NSQConsumerBuilder struct {
	conf NSQConsumerConfig
}

// NewNSQConsumerBuilder 创建消费者构建器
func NewNSQConsumerBuilder(topic, channel string) *NSQConsumerBuilder {
	return &NSQConsumerBuilder{
		conf: NSQConsumerConfig{
			Topic:   topic,
			Channel: channel,
		},
	}
}

// WithLookupd 设置 lookupd 地址
func (b *NSQConsumerBuilder) WithLookupd(addrs ...string) *NSQConsumerBuilder {
	b.conf.Lookupds = addrs
	if len(addrs) > 0 {
		b.conf.NSQDs = nil
	}
	return b
}

// WithNSQD 设置 nsqd 直连地址
func (b *NSQConsumerBuilder) WithNSQD(addrs ...string) *NSQConsumerBuilder {
	if len(b.conf.Lookupds) > 0 {
		D("NSQ consumer builder ignores direct nsqd %v because lookupd is configured %v", addrs, b.conf.Lookupds)
		return b
	}
	b.conf.NSQDs = addrs
	return b
}

// WithConcurrency 设置并发数
func (b *NSQConsumerBuilder) WithConcurrency(n int) *NSQConsumerBuilder {
	b.conf.Concurrency = n
	return b
}

// WithMaxInFlight 设置最大在途消息数
func (b *NSQConsumerBuilder) WithMaxInFlight(n int) *NSQConsumerBuilder {
	b.conf.MaxInFlight = n
	return b
}

// WithMaxAttempts 设置最大重试次数
func (b *NSQConsumerBuilder) WithMaxAttempts(n uint16) *NSQConsumerBuilder {
	b.conf.MaxAttempts = n
	return b
}

// WithMaxBackoff 设置最大退避时间
func (b *NSQConsumerBuilder) WithMaxBackoff(d time.Duration) *NSQConsumerBuilder {
	b.conf.MaxBackoff = d
	return b
}

// WithMsgTimeout 设置消息处理超时
func (b *NSQConsumerBuilder) WithMsgTimeout(d time.Duration) *NSQConsumerBuilder {
	b.conf.MsgTimeout = d
	return b
}

// Build 构建 Consumer
func (b *NSQConsumerBuilder) Build() (*NSQConsumer, error) {
	return NewNSQConsumer(b.conf)
}

// BuildAndStart 构建并启动消费者
func (b *NSQConsumerBuilder) BuildAndStart(handler NSQHandlerFunc) (*NSQConsumer, error) {
	c, err := NewNSQConsumer(b.conf)
	if err != nil {
		return nil, err
	}
	if err := c.StartAsync(handler); err != nil {
		return nil, err
	}
	return c, nil
}

// =========================
// Context 集成（带超时的消息处理）
// =========================

// NSQHandlerContext 带超时的消息处理上下文
type NSQHandlerContext struct {
	context.Context
	Message *nsq.Message
	Cancel  context.CancelFunc
}

// NewNSQHandlerContext 创建带超时的消息处理上下文
func NewNSQHandlerContext(msg *nsq.Message, timeout time.Duration) *NSQHandlerContext {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	return &NSQHandlerContext{
		Context: ctx,
		Message: msg,
		Cancel:  cancel,
	}
}

// NSQHandlerWithTimeout 包装 handler，为每条消息创建带超时的 context
func NSQHandlerWithTimeout(timeout time.Duration, handler func(ctx context.Context, msg *nsq.Message) error) nsq.HandlerFunc {
	return func(msg *nsq.Message) error {
		hctx := NewNSQHandlerContext(msg, timeout)
		defer hctx.Cancel()
		return handler(hctx, msg)
	}
}
