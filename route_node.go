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
	segments := strings.Split(strings.Trim(path, "/"), "/")
	current := n

	var chain HandlerFuncs
	if baseCtx, ok := ctx.(*BaseCtx); ok {
		chain = baseCtx.handlers[:0]
	}
	// 根节点中间件
	chain = append(chain, current.middlewares...)

	for i, seg := range segments {
		matched := false
		// 再匹配静态节点
		if current.staticChild != nil {
			if child, ok := current.staticChild[seg]; ok {
				current = child
				chain = append(chain, current.middlewares...)
				matched = true
			}
		}

		// 先匹配参数节点
		if !matched && current.paramChild != nil {
			paramName := current.paramChild.path
			switch current.paramChild.nType {
			case params:
				// 可选参数： 设置参数值
				ctx.SetParams(paramName[1:], seg)
				current = current.paramChild
				chain = append(chain, current.middlewares...)
				matched = true
			case param:
				if seg != "" {
					ctx.SetParams(paramName[1:], seg)
					current = current.paramChild
					chain = append(chain, current.middlewares...)
					matched = true
				}
			}
		}

		if !matched && current.catchChild != nil {
			ctx.SetParams(current.catchChild.path[1:], strings.Join(segments[i:], "/"))
			current = current.catchChild
			chain = append(chain, current.middlewares...)
			chain = append(chain, current.handlers...)
			return chain, true
		}

		if !matched {
			return nil, false
		}
	}
	// 4. 处理可选参数的特殊情况：检查当前节点是否有可选参数子节点
	// 这种情况处理路径段数少于注册路径的情况，比如注册了 /a/b/:c?，但访问 /a/b
	if current.paramChild != nil && current.paramChild.nType == params {
		// 使用可选参数节点，但不设置参数值（参数为空）
		current = current.paramChild
		chain = append(chain, current.middlewares...)
	}

	// 到叶子节点，追加最终 handler
	if len(current.handlers) == 0 {
		return nil, false
	}
	chain = append(chain, current.handlers...)
	return chain, true
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
