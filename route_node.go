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

const linearChildSearchThreshold = 8

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

// childIndex 返回首字节为 c 的静态子节点下标。
// 小 fan-out 使用有序短扫描以减少固定开销；较大 fan-out 使用运行时优化过的 IndexByte。
func (n *RouteNode) childIndex(c byte) int {
	if len(n.indices) <= linearChildSearchThreshold {
		for i := 0; i < len(n.indices); i++ {
			if n.indices[i] == c {
				return i
			}
			if n.indices[i] > c {
				break
			}
		}
		return -1
	}
	return strings.IndexByte(n.indices, c)
}

// childPosition 使用二分查找返回有序插入位置以及该首字节是否已存在。
func (n *RouteNode) childPosition(c byte) (int, bool) {
	low, high := 0, len(n.indices)
	for low < high {
		middle := int(uint(low+high) >> 1)
		if n.indices[middle] < c {
			low = middle + 1
		} else {
			high = middle
		}
	}
	return low, low < len(n.indices) && n.indices[low] == c
}

func (n *RouteNode) findChild(c byte) *RouteNode {
	index := n.childIndex(c)
	if index < 0 {
		return nil
	}
	return n.children[index]
}

// addChild 按首字节有序插入静态子节点
func (n *RouteNode) addChild(child *RouteNode) {
	c := child.path[0]
	pos, found := n.childPosition(c)
	if found {
		panic("route tree contains duplicate static child index")
	}

	var indices strings.Builder
	indices.Grow(len(n.indices) + 1)
	indices.WriteString(n.indices[:pos])
	indices.WriteByte(c)
	indices.WriteString(n.indices[pos:])
	n.indices = indices.String()

	newChildren := make([]*RouteNode, len(n.children)+1)
	copy(newChildren, n.children[:pos])
	newChildren[pos] = child
	copy(newChildren[pos+1:], n.children[pos:])
	n.children = newChildren
}

// replaceChild 替换首字节为 c 的子节点
func (n *RouteNode) replaceChild(c byte, child *RouteNode) {
	index := n.childIndex(c)
	if index >= 0 {
		n.children[index] = child
	}
}

// removeChild 移除首字节为 c 的静态子节点
func (n *RouteNode) removeChild(c byte) {
	index := n.childIndex(c)
	if index < 0 {
		return
	}

	n.indices = n.indices[:index] + n.indices[index+1:]
	last := len(n.children) - 1
	copy(n.children[index:], n.children[index+1:])
	n.children[last] = nil
	n.children = n.children[:last]
	if last == 0 {
		n.indices = ""
		n.children = nil
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
			if n.catchChild.path != seg {
				panic("conflicting catch-all parameter names: " + n.catchChild.path + " and " + seg)
			}
			return n.catchChild.addRouteSegments(segments, idx+1, handlers)
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
			if n.paramChild.path != cleanSeg || n.paramChild.nType != nt {
				panic("conflicting route parameters: " + n.paramChild.path + " and " + seg)
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

// splitPath 将连续静态路径压缩为一个 segment，并保留参数边界。
func splitPath(path string) []string {
	if strings.Contains(path, "//") {
		panic("invalid route path with empty segment: " + path)
	}
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}

	segments := make([]string, 0, strings.Count(trimmed, "/")+1)
	staticStart := -1
	for start := 0; start < len(trimmed); {
		relativeEnd := strings.IndexByte(trimmed[start:], '/')
		end := len(trimmed)
		if relativeEnd >= 0 {
			end = start + relativeEnd
		}
		seg := trimmed[start:end]
		last := end == len(trimmed)
		isParam := strings.HasPrefix(seg, ":")
		isCatchAll := strings.HasPrefix(seg, "*")

		if isParam {
			cleanSeg := strings.TrimSuffix(seg, "?")
			name := cleanSeg[1:]
			if name == "" || strings.ContainsAny(name, ":*?") {
				panic("invalid route parameter: " + seg)
			}
			if strings.HasSuffix(seg, "?") && !last {
				panic("optional route parameter must be the final segment: " + seg)
			}
		}
		if isCatchAll {
			name := seg[1:]
			if strings.ContainsAny(name, ":*?") {
				panic("invalid catch-all parameter: " + seg)
			}
			if !last {
				panic("catch-all route parameter must be the final segment: " + seg)
			}
		}

		if isParam || isCatchAll {
			if staticStart >= 0 {
				segments = append(segments, trimmed[staticStart:start-1])
				staticStart = -1
			}
			segments = append(segments, seg)
		} else if staticStart < 0 {
			staticStart = start
		}

		if last {
			break
		}
		start = end + 1
	}
	if staticStart >= 0 {
		segments = append(segments, trimmed[staticStart:])
	}
	return segments
}

func routeParamName(path string) string {
	name := path[1:]
	if path[0] == '*' && name == "" {
		return "*"
	}
	return name
}

// match 匹配路径，返回 handler 链和是否匹配成功。
// 这是路由查找的主入口，供 route.go 调用。
func (n *RouteNode) match(path string, ctx Ctx) (HandlerFuncs, bool) {
	var chain HandlerFuncs
	if baseCtx, ok := ctx.(*BaseCtx); ok {
		chain = baseCtx.handlers[:0]
	}
	matched, ok := n.matchPath(strings.Trim(path, "/"), ctx, chain)
	if !ok {
		return nil, false
	}
	return matched, true
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
			paramName := routeParamName(n.catchChild.path)
			ctx.SetParams(paramName, "")
			c := append(childChain, n.catchChild.middlewares...)
			c = append(c, n.catchChild.handlers...)
			return c, true
		}
		return childChain, false
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
				return chain, false
			}
			remaining := path[commonLen:]
			childChain := chain
			if remaining == "" || strings.HasSuffix(n.path, "/") || strings.HasPrefix(remaining, "/") {
				childChain = append(childChain, n.middlewares...)
			}
			// 完全匹配当前节点
			if remaining == "" {
				return n.matchFallback(ctx, childChain)
			}
			return n.matchChildren(remaining, ctx, childChain)
		}
		// n.path == "" (仅 root 可能出现): 直接匹配子节点
		childChain := append(chain, n.middlewares...)
		return n.matchChildren(path, ctx, childChain)
	}

	return chain, false
}

// matchFallback 当前节点前缀完全匹配后检查 handlers 和特殊子节点
func (n *RouteNode) matchFallback(ctx Ctx, chain HandlerFuncs) (HandlerFuncs, bool) {
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
		paramName := routeParamName(n.catchChild.path)
		ctx.SetParams(paramName, "")
		c := append(chain, n.catchChild.middlewares...)
		c = append(c, n.catchChild.handlers...)
		return c, true
	}
	return chain, false
}

// matchChildren 在当前节点的所有子节点中匹配路径
func (n *RouteNode) matchChildren(path string, ctx Ctx, chain HandlerFuncs) (HandlerFuncs, bool) {
	fallbackChain := chain

	// 3a. 先匹配静态子节点（优先级最高）
	if len(path) > 0 {
		child := n.findChild(path[0])
		if child != nil {
			matched, ok := child.matchPath(path, ctx, chain)
			if ok {
				return matched, true
			}
			fallbackChain = matched
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

		paramName := routeParamName(n.paramChild.path)
		oldVal, hadParam := saveParam(ctx, paramName)
		ctx.SetParams(paramName, paramVal)

		matched, ok := n.paramChild.matchPath(rest, ctx, fallbackChain)
		if ok {
			return matched, true
		}
		restoreParam(ctx, paramName, oldVal, hadParam)
	}

	// 3c. 匹配通配符子节点（兜底）
	if dynamicAllowed && n.catchChild != nil {
		paramName := routeParamName(n.catchChild.path)
		oldVal, hadParam := saveParam(ctx, paramName)
		ctx.SetParams(paramName, dynamicPath)

		childChain := append(fallbackChain, n.catchChild.middlewares...)
		if len(n.catchChild.handlers) > 0 {
			return append(childChain, n.catchChild.handlers...), true
		}
		restoreParam(ctx, paramName, oldVal, hadParam)
	}

	return fallbackChain, false
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
		if n.catchChild == nil || n.catchChild.path != seg {
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
		cleanSeg := strings.TrimSuffix(seg, "?")
		nType := param
		if strings.HasSuffix(seg, "?") {
			nType = params
		}
		if n.paramChild == nil || n.paramChild.path != cleanSeg || n.paramChild.nType != nType {
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
	} else if removed {
		child.compactStaticChain()
	}
	return removed
}

// compactStaticChain 合并删除后无路由语义边界的单静态子链。
func (n *RouteNode) compactStaticChain() {
	for n.nType == static &&
		len(n.middlewares) == 0 &&
		len(n.handlers) == 0 &&
		n.paramChild == nil &&
		n.catchChild == nil &&
		len(n.children) == 1 {
		child := n.children[0]
		n.path += child.path
		n.indices = child.indices
		n.children = child.children
		n.paramChild = child.paramChild
		n.catchChild = child.catchChild
		n.middlewares = child.middlewares
		n.handlers = child.handlers
	}
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
