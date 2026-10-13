package core

import (
	"crypto/tls"
	"github.com/xs23933/core/v3/etcd"
)

// EtcdTLSConfig 为 Registry、Discovery 和 Gateway 自建连接读取相同 etcd TLS 配置。
func (app *Core) EtcdTLSConfig() (*tls.Config, string, error) {
	return etcd.LoadTLS(etcd.TLSOptions{
		Enabled:    app.Conf.GetBool("etcd.tls.enabled", false),
		CAFile:     app.Conf.GetString("etcd.tls.ca_file"),
		CertFile:   app.Conf.GetString("etcd.tls.cert_file"),
		KeyFile:    app.Conf.GetString("etcd.tls.key_file"),
		ServerName: app.Conf.GetString("etcd.tls.server_name"),
	})
}

func (app *Core) applyEtcdTLS(opts *etcd.Options) error {
	if opts.TLS != nil {
		return nil
	}
	transport, fingerprint, err := app.EtcdTLSConfig()
	if err != nil {
		return err
	}
	opts.TLS, opts.TLSFingerprint = transport, fingerprint
	return nil
}
