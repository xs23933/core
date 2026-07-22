package core

import (
	"strings"
)

type nodeType uint8

const (
	static nodeType = iota
	param
	params   // optional param
	catchAll // *wildcard
	root
)

// RouteNode 压缩基数树节点。
// 每个节点的 path 可跨越多个路径段（如 "api/v1/"），实现 O(k) 前缀匹配。
type RouteNode struct {
	path        string // 该节点匹配的前缀（压缩后可能跨多个段）
	nType       nodeType
	indices     string       // 所有静态子节点 path 的首字节，按字典序排列
	children    []*RouteNode // 静态子节点（与 indices 同序）
	paramChild  *RouteNode   // 参数子节点 :param
	catchChild  *RouteNode   // 通配子节点 *wildcard
	middlewares HandlerFuncs
	handlers    HandlerFuncs
}

// lcp 返回两个字符串的最长公共前缀长度
func lcp(a, b string) int {
	n := min(len(b), len(a))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// findChild 在 indices 中二分查找首字节为 c 的子节点
func (n *RouteNode) findChild(c byte) *RouteNode {
	if n.indices == "" {
		return nil
	}
	// indices 按字典序排列，先查找首字节位置
	for i := 0; i < len(n.indices); i++ {
		if n.indices[i] >= c {
			if n.indices[i] == c {
				return n.children[i]
			}
			return nil
		}
	}
	return nil
}

// addChild 按首字节有序插入静态子节点
func (n *RouteNode) addChild(child *RouteNode) {
	c := child.path[0]

	// 找到插入位置（保持 indices 有序）
	pos := 0
	for pos < len(n.indices) && n.indices[pos] < c {
		pos++
	}

	// 扩容并插入
	newIndices := make([]byte, len(n.indices)+1)
	copy(newIndices, n.indices[:pos])
	newIndices[pos] = c
	copy(newIndices[pos+1:], n.indices[pos:])
	n.indices = string(newIndices)

	newChildren := make([]*RouteNode, len(n.children)+1)
	copy(newChildren, n.children[:pos])
	newChildren[pos] = child
	copy(newChildren[pos+1:], n.children[pos:])
	n.children = newChildren
}

// replaceChild 替换首字节为 c 的子节点
func (n *RouteNode) replaceChild(c byte, child *RouteNode) {
	for i := 0; i < len(n.indices); i++ {
		if n.indices[i] == c {
			n.children[i] = child
			return
		}
	}
}

// removeChild 移除首字节为 c 的静态子节点
func (n *RouteNode) removeChild(c byte) {
	for i := 0; i < len(n.indices); i++ {
		if n.indices[i] == c {
			n.indices = n.indices[:i] + n.indices[i+1:]
			n.children = append(n.children[:i], n.children[i+1:]...)
			return
		}
	}
}

// insertStaticPath 插入静态路径片段，并返回代表完整 path 的节点。
// 循环下沉可处理任意层级的前缀冲突，同时保证同一父节点下首字节唯一。
func (n *RouteNode) insertStaticPath(path string) *RouteNode {
	current := n
	remaining := path

	for {
		child := current.findChild(remaining[0])
		if child == nil {
			child = &RouteNode{path: remaining, nType: static}
			current.addChild(child)
			return child
		}

		commonLen := lcp(child.path, remaining)
		if commonLen == len(child.path) {
			remaining = remaining[commonLen:]
			if remaining == "" {
				return child
			}
			current = child
			continue
		}

		parent := &RouteNode{
			path:  child.path[:commonLen],
			nType: static,
		}
		current.replaceChild(remaining[0], parent)

		child.path = child.path[commonLen:]
		parent.addChild(child)

		remaining = remaining[commonLen:]
		if remaining == "" {
			return parent
		}
		current = parent
	}
}

// addRoute 向基数树插入一条完整路由（带 handler）
func (n *RouteNode) addRoute(path string, handlers HandlerFuncs) {
	segments := splitPath(path)
	n.addRouteSegments(segments, 0, handlers)
}

// addRouteNode 向基数树插入中间件节点（不带 handler），返回该节点指针
func (n *RouteNode) addRouteNode(fullPath string) *RouteNode {
	segments := splitPath(fullPath)
	return n.addRouteSegments(segments, 0, nil)
}

// addRouteSegments 递归插入路由片段
func (n *RouteNode) addRouteSegments(segments []string, idx int, handlers HandlerFuncs) *RouteNode {
	if idx >= len(segments) {
		if handlers != nil {
			n.handlers = handlers
		}
		return n
	}

	seg := segments[idx]
	isParam := strings.HasPrefix(seg, ":")
	isCatchAll := strings.HasPrefix(seg, "*")
	isOptional := strings.HasSuffix(seg, "?")

	if isCatchAll {
		if n.catchChild != nil {
			return n.catchChild
		}
		child := &RouteNode{path: seg, nType: catchAll}
		n.catchChild = child
		if handlers != nil {
			child.handlers = handlers
		}
		return child
	}

	if isParam {
		cleanSeg := strings.TrimSuffix(seg, "?")
		nt := param
		if isOptional {
			nt = params
		}

		if n.paramChild != nil {
			// 已存在 param 子节点，继续向下
			if idx == len(segments)-1 && handlers != nil {
				n.paramChild.handlers = handlers
			}
			return n.paramChild.addRouteSegments(segments, idx+1, handlers)
		}

		child := &RouteNode{
			path:  cleanSeg,
			nType: nt,
		}
		n.paramChild = child

		if idx == len(segments)-1 && handlers != nil {
			child.handlers = handlers
			return child
		}
		return child.addRouteSegments(segments, idx+1, handlers)
	}

	child := n.insertStaticPath(seg)
	if idx == len(segments)-1 && handlers != nil {
		child.handlers = handlers
		return child
	}
	return child.addRouteSegments(segments, idx+1, handlers)
}

// splitPath 将路径按 "/" 分割为 segments
func splitPath(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	segments := strings.Split(trimmed, "/")
	for i := 0; i < len(segments)-1; i++ {
		next := segments[i+1]
		if !strings.HasPrefix(segments[i], ":") &&
			!strings.HasPrefix(segments[i], "*") &&
			!strings.HasPrefix(next, ":") &&
			!strings.HasPrefix(next, "*") {
			segments[i] += "/"
		}
	}
	return segments
}

// match 匹配路径，返回 handler 链和是否匹配成功。
// 这是路由查找的主入口，供 route.go 调用。
func (n *RouteNode) match(path string, ctx Ctx) (HandlerFuncs, bool) {
	var chain HandlerFuncs
	if baseCtx, ok := ctx.(*BaseCtx); ok {
		chain = baseCtx.handlers[:0]
	}
	// 预处理：去掉首尾 /
	trimmed := strings.Trim(path, "/")
	if trimmed == "" && path == "/" {
		// 根路径：检查根节点 handler、可选参数、通配符
		if len(n.handlers) > 0 {
			return append(chain, n.handlers...), true
		}
		if n.paramChild != nil && n.paramChild.nType == params {
			c := append(chain, n.paramChild.middlewares...)
			if len(n.paramChild.handlers) > 0 {
				return append(c, n.paramChild.handlers...), true
			}
		}
		if n.catchChild != nil {
			paramName := n.catchChild.path[1:]
			ctx.SetParams(paramName, "")
			c := append(chain, n.catchChild.middlewares...)
			c = append(c, n.catchChild.handlers...)
			return c, true
		}
		return nil, false
	}
	return n.matchPath(trimmed, ctx, chain)
}

// matchPath 递归路径匹配
func (n *RouteNode) matchPath(path string, ctx Ctx, chain HandlerFuncs) (HandlerFuncs, bool) {
	// 空路径：检查本节点 handlers 和特殊子节点（可选参数、通配符）
	if path == "" {
		childChain := append(chain, n.middlewares...)
		if len(n.handlers) > 0 {
			return append(childChain, n.handlers...), true
		}
		if n.paramChild != nil && n.paramChild.nType == params {
			c := append(childChain, n.paramChild.middlewares...)
			if len(n.paramChild.handlers) > 0 {
				return append(c, n.paramChild.handlers...), true
			}
		}
		if n.catchChild != nil {
			paramName := n.catchChild.path[1:]
			ctx.SetParams(paramName, "")
			c := append(childChain, n.catchChild.middlewares...)
			c = append(c, n.catchChild.handlers...)
			return c, true
		}
		return nil, false
	}

	// param/catchAll 节点：自身不匹配路径段，直接匹配子节点
	// （父节点的 matchChildren 已消费参数值）
	if n.nType == param || n.nType == params || n.nType == catchAll {
		childChain := append(chain, n.middlewares...)
		return n.matchChildren(path, ctx, childChain)
	}

	// static/root 节点：前缀匹配
	if n.nType == static || n.nType == root {
		if n.path != "" {
			commonLen := lcp(path, n.path)
			if commonLen < len(n.path) {
				return nil, false
			}
			remaining := path[commonLen:]
			childChain := chain
			if remaining == "" || strings.HasSuffix(n.path, "/") || strings.HasPrefix(remaining, "/") {
				childChain = append(childChain, n.middlewares...)
			}
			// 完全匹配当前节点
			if remaining == "" {
				return n.matchFallback(n.path, ctx, childChain)
			}
			return n.matchChildren(remaining, ctx, childChain)
		}
		// n.path == "" (仅 root 可能出现): 直接匹配子节点
		childChain := append(chain, n.middlewares...)
		return n.matchChildren(path, ctx, childChain)
	}

	return nil, false
}

// matchFallback 当前节点前缀完全匹配后检查 handlers 和特殊子节点
func (n *RouteNode) matchFallback(_ string, ctx Ctx, chain HandlerFuncs) (HandlerFuncs, bool) {
	if len(n.handlers) > 0 {
		return append(chain, n.handlers...), true
	}
	if n.paramChild != nil && n.paramChild.nType == params {
		c := append(chain, n.paramChild.middlewares...)
		if len(n.paramChild.handlers) > 0 {
			return append(c, n.paramChild.handlers...), true
		}
	}
	if n.catchChild != nil {
		paramName := n.catchChild.path[1:]
		ctx.SetParams(paramName, "")
		c := append(chain, n.catchChild.middlewares...)
		c = append(c, n.catchChild.handlers...)
		return c, true
	}
	return nil, false
}

// matchChildren 在当前节点的所有子节点中匹配路径
func (n *RouteNode) matchChildren(path string, ctx Ctx, chain HandlerFuncs) (HandlerFuncs, bool) {
	// 3a. 先匹配静态子节点（优先级最高）
	if n.indices != "" {
		c := path[0]
		child := n.findChild(c)
		if child != nil {
			if matched, ok := child.matchPath(path, ctx, chain); ok {
				return matched, true
			}
		}
	}

	dynamicPath := strings.TrimPrefix(path, "/")
	dynamicAllowed := n.nType != static || strings.HasSuffix(n.path, "/") || strings.HasPrefix(path, "/")

	// 3b. 匹配参数子节点
	if dynamicAllowed && n.paramChild != nil && dynamicPath != "" {
		// 提取参数值（当前段）
		before, after, ok0 := strings.Cut(dynamicPath, "/")
		paramVal := dynamicPath
		rest := ""
		if ok0 {
			paramVal = before
			rest = after
		}

		paramName := n.paramChild.path[1:] // 去掉 ":"
		oldVal, hadParam := saveParam(ctx, paramName)
		ctx.SetParams(paramName, paramVal)

		childChain := append(chain, n.paramChild.middlewares...)
		matched, ok := n.paramChild.matchPath(rest, ctx, childChain)
		if ok {
			return matched, true
		}
		restoreParam(ctx, paramName, oldVal, hadParam)
	}

	// 3c. 匹配通配符子节点（兜底）
	if dynamicAllowed && n.catchChild != nil {
		paramName := n.catchChild.path[1:] // 去掉 "*"
		oldVal, hadParam := saveParam(ctx, paramName)
		ctx.SetParams(paramName, dynamicPath)

		childChain := append(chain, n.catchChild.middlewares...)
		if len(n.catchChild.handlers) > 0 {
			return append(childChain, n.catchChild.handlers...), true
		}
		restoreParam(ctx, paramName, oldVal, hadParam)
	}

	return nil, false
}

// removeRoute 从基数树中删除路由及其 handler
func (n *RouteNode) removeRoute(path string) bool {
	segments := splitPath(path)
	return n.removeRouteRecursive(segments, 0)
}

func (n *RouteNode) removeRouteRecursive(segments []string, idx int) bool {
	if idx >= len(segments) {
		if len(n.handlers) == 0 {
			return false
		}
		n.handlers = nil
		return true
	}

	seg := segments[idx]

	// catch-all
	if strings.HasPrefix(seg, "*") {
		if n.catchChild == nil {
			return false
		}
		removed := n.catchChild.removeRouteRecursive(segments, idx+1)
		if removed && n.catchChild.empty() {
			n.catchChild = nil
		}
		return removed
	}

	// param
	if strings.HasPrefix(seg, ":") {
		if n.paramChild == nil {
			return false
		}
		removed := n.paramChild.removeRouteRecursive(segments, idx+1)
		if removed && n.paramChild.empty() {
			n.paramChild = nil
		}
		return removed
	}

	return n.removeStaticRoute(segments, idx, seg)
}

// removeStaticRoute 沿压缩后的静态节点消费当前 segment。
func (n *RouteNode) removeStaticRoute(segments []string, idx int, remaining string) bool {
	child := n.findChild(remaining[0])
	if child == nil || !strings.HasPrefix(remaining, child.path) {
		return false
	}

	rest := remaining[len(child.path):]
	var removed bool
	if rest == "" {
		removed = child.removeRouteRecursive(segments, idx+1)
	} else {
		removed = child.removeStaticRoute(segments, idx, rest)
	}
	if removed && child.empty() {
		n.removeChild(remaining[0])
	}
	return removed
}

// empty 检查节点是否可被清理
func (n *RouteNode) empty() bool {
	return len(n.middlewares) == 0 &&
		len(n.handlers) == 0 &&
		len(n.children) == 0 &&
		n.paramChild == nil &&
		n.catchChild == nil
}

// 保持向后兼容：导出原有函数
func saveParam(ctx Ctx, key string) (string, bool) {
	baseCtx, ok := ctx.(*BaseCtx)
	if !ok {
		return "", false
	}
	if baseCtx.params == nil {
		return "", false
	}
	old, ok := baseCtx.params[key]
	return old, ok
}

func restoreParam(ctx Ctx, key, oldVal string, hadParam bool) {
	baseCtx, ok := ctx.(*BaseCtx)
	if !ok {
		return
	}
	if hadParam {
		baseCtx.params[key] = oldVal
	} else {
		delete(baseCtx.params, key)
	}
}
