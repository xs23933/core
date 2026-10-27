package core

// ServingReady 表示传输、工作负载、路由发布及注册就绪步骤均已成功完成。
// 该状态用于运行态观测，不替代具体业务依赖的健康检查。
func (app *Core) ServingReady() bool {
	if app == nil {
		return false
	}
	app.mutex.Lock()
	defer app.mutex.Unlock()
	return app.servingReady && !app.shutdownStarted
}
