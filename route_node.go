package core

import (
	"strings"
)

type nodeType uint8

const (
	static nodeType = iota
	param
	params
	catchAll
	root
)

type RouteNode struct {
	path        string                // 路径
	nType       nodeType              // 节点类型
	staticChild map[string]*RouteNode // 静态子节点
	paramChild  *RouteNode            // 参数节点 (只能一个)
	catchChild  *RouteNode            // 通配符节点 (只能一个)
	middlewares HandlerFuncs          // 中间件链
	handlers    HandlerFuncs          // 当前节点 handler链(中间件+最终处理器)
}

func (n *RouteNode) addRoute(path string, handlers []HandlerFunc) {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	current := n

	for i, seg := range segments {
		isParam := strings.HasPrefix(seg, ":")
		isCatchAll := strings.HasPrefix(seg, "*")
		isParams := strings.HasSuffix(seg, "?") //检查是否为可选参数

		if isCatchAll {
			// 通配符必须是最后一个
			child := &RouteNode{path: seg, nType: catchAll, handlers: nil}
			current.catchChild = child
			current = child
		} else if isParam {
			if isParams {
				// 可选参数：移除末尾的?
				cleanSeg := strings.TrimSuffix(seg, "?")
				if current.paramChild == nil {
					current.paramChild = &RouteNode{path: cleanSeg, nType: params, handlers: nil}
				}
				current = current.paramChild
			} else { // 必须参数
				if current.paramChild == nil {
					current.paramChild = &RouteNode{path: seg, nType: param, handlers: nil}
				}
				current = current.paramChild
			}
		} else {
			if current.staticChild == nil {
				current.staticChild = make(map[string]*RouteNode)
			}
			child, ok := current.staticChild[seg]
			if !ok {
				child = &RouteNode{path: seg, nType: static, handlers: nil}
				current.staticChild[seg] = child
			}
			current = child
		}

		// 到叶子节点挂 handler
		if i == len(segments)-1 {
			current.handlers = handlers
		}
	}
}

func (n *RouteNode) removeRoute(path string) bool {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	return n.removeRouteSegments(segments, 0)
}

func (n *RouteNode) removeRouteSegments(segments []string, idx int) bool {
	if idx == len(segments) {
		if len(n.handlers) == 0 {
			return false
		}
		n.handlers = nil
		return true
	}

	seg := segments[idx]
	var child **RouteNode

	switch {
	case strings.HasPrefix(seg, "*"):
		child = &n.catchChild
	case strings.HasPrefix(seg, ":"):
		child = &n.paramChild
	default:
		if n.staticChild == nil {
			return false
		}
		next := n.staticChild[seg]
		if next == nil {
			return false
		}
		removed := next.removeRouteSegments(segments, idx+1)
		if removed && next.empty() {
			delete(n.staticChild, seg)
			if len(n.staticChild) == 0 {
				n.staticChild = nil
			}
		}
		return removed
	}

	if *child == nil {
		return false
	}

	removed := (*child).removeRouteSegments(segments, idx+1)
	if removed && (*child).empty() {
		*child = nil
	}
	return removed
}

func (n *RouteNode) empty() bool {
	return len(n.middlewares) == 0 &&
		len(n.handlers) == 0 &&
		len(n.staticChild) == 0 &&
		n.paramChild == nil &&
		n.catchChild == nil
}

func (n *RouteNode) match(path string, ctx Ctx) (HandlerFuncs, bool) {
	var chain HandlerFuncs
	if baseCtx, ok := ctx.(*BaseCtx); ok {
		chain = baseCtx.handlers[:0]
	}
	trimmed := strings.Trim(path, "/")
	return n.matchPath(trimmed, trimmed == "", ctx, append(chain, n.middlewares...))
}

func (n *RouteNode) matchPath(path string, forceEmptySegment bool, ctx Ctx, chain HandlerFuncs) (HandlerFuncs, bool) {
	if path == "" && !forceEmptySegment {
		if len(n.handlers) > 0 {
			return append(chain, n.handlers...), true
		}
		if n.paramChild != nil && n.paramChild.nType == params {
			childChain := append(chain, n.paramChild.middlewares...)
			if len(n.paramChild.handlers) > 0 {
				return append(childChain, n.paramChild.handlers...), true
			}
		}
		return nil, false
	}

	seg := ""
	rest := ""
	if !forceEmptySegment {
		if slash := strings.IndexByte(path, '/'); slash >= 0 {
			seg = path[:slash]
			rest = path[slash+1:]
		} else {
			seg = path
		}
	}

	if n.staticChild != nil {
		if child, ok := n.staticChild[seg]; ok {
			if matched, ok := child.matchPath(rest, false, ctx, append(chain, child.middlewares...)); ok {
				return matched, true
			}
		}
	}

	if n.paramChild != nil && seg != "" {
		paramName := n.paramChild.path[1:]
		oldParams := cloneParams(ctx)
		ctx.SetParams(paramName, seg)
		if matched, ok := n.paramChild.matchPath(rest, false, ctx, append(chain, n.paramChild.middlewares...)); ok {
			return matched, true
		}
		restoreParams(ctx, oldParams)
	}

	if n.catchChild != nil {
		oldParams := cloneParams(ctx)
		catchValue := path
		if forceEmptySegment {
			catchValue = ""
		}
		ctx.SetParams(n.catchChild.path[1:], catchValue)
		chain = append(chain, n.catchChild.middlewares...)
		chain = append(chain, n.catchChild.handlers...)
		if len(n.catchChild.handlers) > 0 {
			return chain, true
		}
		restoreParams(ctx, oldParams)
	}

	return nil, false
}

func cloneParams(ctx Ctx) map[string]string {
	baseCtx, ok := ctx.(*BaseCtx)
	if !ok || len(baseCtx.params) == 0 {
		return nil
	}
	params := make(map[string]string, len(baseCtx.params))
	for k, v := range baseCtx.params {
		params[k] = v
	}
	return params
}

func restoreParams(ctx Ctx, params map[string]string) {
	baseCtx, ok := ctx.(*BaseCtx)
	if !ok {
		return
	}
	baseCtx.params = params
}

func (n *RouteNode) addRouteNode(path string) *RouteNode {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	current := n

	for _, seg := range segments {
		isParam := strings.HasPrefix(seg, ":")
		isCatchAll := strings.HasPrefix(seg, "*")

		if isCatchAll {
			if current.catchChild == nil {
				current.catchChild = &RouteNode{
					path:  seg,
					nType: catchAll,
				}
			}
			current = current.catchChild
		} else if isParam {
			if current.paramChild == nil {
				current.paramChild = &RouteNode{
					path:  seg,
					nType: param,
				}
			}
			current = current.paramChild
		} else {
			if current.staticChild == nil {
				current.staticChild = make(map[string]*RouteNode)
			}
			child, ok := current.staticChild[seg]
			if !ok {
				child = &RouteNode{
					path:  seg,
					nType: static,
				}
				current.staticChild[seg] = child
			}
			current = child
		}
	}
	return current
}
