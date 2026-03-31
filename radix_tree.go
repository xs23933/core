// route_node.go
package core

import (
	"strings"
)

type nodeType uint8

const (
	static nodeType = iota
	param
	catchAll
)

type RouteNode struct {
	// 路径片段
	path string
	// 节点类型
	nType nodeType
	// 子节点 - 使用 map 存储，key 是路径片段
	children map[string]*RouteNode
	// 参数子节点
	paramChild *RouteNode
	// 通配符子节点
	catchChild *RouteNode
	// 中间件链
	middlewares HandlerFuncs
	// 处理器链
	handlers HandlerFuncs
	// 是否是路由终点
	isEndpoint bool
	// 参数名称（仅 param 节点使用）
	paramName string
}

// NewRouteNode 创建新的路由节点
func NewRouteNode() *RouteNode {
	return &RouteNode{
		children:   make(map[string]*RouteNode),
		nType:      static,
		isEndpoint: false,
	}
}

// addRoute 添加路由
func (n *RouteNode) addRoute(path string, handlers HandlerFuncs) {
	if path == "" {
		path = "/"
	}

	// 确保路径以 / 开头
	if path[0] != '/' {
		path = "/" + path
	}

	n.insert(path, handlers)
}

func (n *RouteNode) insert(fullPath string, handlers HandlerFuncs) {
	current := n
	path := fullPath

	for {
		// 处理通配符
		if strings.HasPrefix(path, "*") {
			if current.catchChild != nil {
				panic("duplicate catch-all route: " + fullPath)
			}
			current.catchChild = &RouteNode{
				path:       path,
				nType:      catchAll,
				handlers:   handlers,
				isEndpoint: true,
			}
			return
		}

		// 处理参数节点
		if strings.HasPrefix(path, ":") {
			// 提取参数名
			paramName := path[1:]
			// 查找参数名结束位置
			slashIdx := strings.IndexByte(paramName, '/')
			if slashIdx != -1 {
				paramName = paramName[:slashIdx]
			}
			// 去除可选参数标记
			paramName = strings.TrimSuffix(paramName, "?")

			if current.paramChild == nil {
				current.paramChild = &RouteNode{
					path:       ":" + paramName,
					nType:      param,
					children:   make(map[string]*RouteNode),
					paramName:  paramName,
					isEndpoint: false,
				}
			}

			// 计算剩余路径
			remaining := path[len(":"+paramName):]
			if remaining == "" {
				current.paramChild.handlers = handlers
				current.paramChild.isEndpoint = true
				return
			}

			current = current.paramChild
			path = remaining
			continue
		}

		// 处理静态节点
		// 查找第一个 '/' 位置
		slashIdx := strings.IndexByte(path, '/')
		var segment string
		if slashIdx == -1 {
			segment = path
			path = ""
		} else {
			segment = path[:slashIdx]
			path = path[slashIdx:]
		}

		if segment == "" {
			// 根路径
			current.handlers = handlers
			current.isEndpoint = true
			return
		}

		// 查找或创建子节点
		child, exists := current.children[segment]
		if !exists {
			child = &RouteNode{
				path:       segment,
				nType:      static,
				children:   make(map[string]*RouteNode),
				isEndpoint: false,
			}
			current.children[segment] = child
		}

		if path == "" {
			child.handlers = handlers
			child.isEndpoint = true
			return
		}

		current = child
	}
}

// match 匹配路由
func (n *RouteNode) match(path string, ctx Ctx) (HandlerFuncs, bool) {
	// 收集中间件链
	var chain HandlerFuncs

	// 添加根节点中间件
	chain = append(chain, n.middlewares...)

	return n.matchPath(path, chain, ctx)
}

func (n *RouteNode) matchPath(path string, chain HandlerFuncs, ctx Ctx) (HandlerFuncs, bool) {
	if path == "" {
		path = "/"
	}

	// 确保路径以 / 开头
	if path[0] != '/' {
		path = "/" + path
	}

	current := n
	remaining := path

	for {
		// 检查通配符
		if current.catchChild != nil {
			paramValue := strings.TrimPrefix(remaining, "/")
			ctx.SetParams(current.catchChild.paramName, paramValue)
			chain = append(chain, current.catchChild.middlewares...)
			chain = append(chain, current.catchChild.handlers...)
			return chain, true
		}

		// 处理根路径
		if remaining == "/" {
			if current.isEndpoint {
				chain = append(chain, current.handlers...)
				return chain, true
			}
			// 检查是否有参数节点
			if current.paramChild != nil && current.paramChild.isEndpoint {
				chain = append(chain, current.paramChild.middlewares...)
				chain = append(chain, current.paramChild.handlers...)
				return chain, true
			}
			return nil, false
		}

		// 去掉开头的 '/'
		if remaining[0] == '/' {
			remaining = remaining[1:]
		}

		// 查找下一个 '/' 位置
		slashIdx := strings.IndexByte(remaining, '/')
		var segment string
		if slashIdx == -1 {
			segment = remaining
			remaining = ""
		} else {
			segment = remaining[:slashIdx]
			remaining = remaining[slashIdx:]
		}

		matched := false

		// 1. 尝试静态匹配
		if child, exists := current.children[segment]; exists {
			chain = append(chain, child.middlewares...)
			current = child
			matched = true

			// 如果已经匹配完所有路径段
			if remaining == "" {
				if current.isEndpoint {
					chain = append(chain, current.handlers...)
					return chain, true
				}
				// 检查参数节点
				if current.paramChild != nil && current.paramChild.isEndpoint {
					chain = append(chain, current.paramChild.middlewares...)
					chain = append(chain, current.paramChild.handlers...)
					return chain, true
				}
				return nil, false
			}
			continue
		}

		// 2. 尝试参数匹配
		if !matched && current.paramChild != nil {
			ctx.SetParams(current.paramChild.paramName, segment)
			chain = append(chain, current.paramChild.middlewares...)
			current = current.paramChild
			matched = true

			if remaining == "" {
				if current.isEndpoint {
					chain = append(chain, current.handlers...)
					return chain, true
				}
				return nil, false
			}
			continue
		}

		if !matched {
			return nil, false
		}
	}
}

// addRouteNode 为中间件添加节点（保持原有功能）
func (n *RouteNode) addRouteNode(path string) *RouteNode {
	if path == "" {
		path = "/"
	}

	if path[0] != '/' {
		path = "/" + path
	}

	current := n
	segments := strings.Split(strings.Trim(path, "/"), "/")

	for _, seg := range segments {
		if seg == "" {
			continue
		}

		// 查找或创建子节点
		child, exists := current.children[seg]
		if !exists {
			child = &RouteNode{
				path:       seg,
				nType:      static,
				children:   make(map[string]*RouteNode),
				isEndpoint: false,
			}
			current.children[seg] = child
		}
		current = child
	}

	return current
}
