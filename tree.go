package core

import (
	"net/http"
	"strings"
)

type node struct {
	path     string
	handles  map[int8]HandlerFuncs // key is methods
	children map[string]*node      // key is path of next node
}

type tree struct {
	node *node
}

type param struct {
	key   string
	value string
}

type params []*param

type result struct {
	preloads HandlerFuncs
	handler  HandlerFuncs
	params   params
}

func newResult() *result {
	return &result{
		preloads: make(HandlerFuncs, 0),
	}
}

const (
	slashDelimiter    string = "/"
	paramDelimiter    string = ":"
	optionalDelimiter string = "?"
	ptnWildcard       string = "*"
)

func NewTree() *tree {
	return &tree{
		node: &node{
			path:     slashDelimiter,
			handles:  make(map[int8]HandlerFuncs), // method handle
			children: make(map[string]*node),      // children node
		},
	}
}

// Insert insert handler
//
//  methods []string  GET | POST any http method
//  path string static, param(:param), catchall(*)
//  handler  HandlerFunc | HandlerFuncs
func (t *tree) Insert(methods []string, path string, handler interface{}) error {
	curNode := t.node
	if path == slashDelimiter { // add root node
		curNode.path = path
		t.insert(methods, curNode, handler)
		return nil
	}
	paths := split(path)
	for i, p := range paths {
		nextNode, ok := curNode.children[p] // 查询子节点
		if ok {
			curNode = nextNode
		}
		// Create a new node, if not exist
		if !ok {
			curNode.children[p] = &node{
				path:     p,
				handles:  make(map[int8]HandlerFuncs),
				children: make(map[string]*node),
			}
			curNode = curNode.children[p]
		}
		// last loop. if there is already registered date, overwrite it.
		if i == len(paths)-1 {
			curNode.path = p
			t.insert(methods, curNode, handler)
			break
		}
	}

	return nil
}

// insert for insert handler with methods
func (t *tree) insert(methods []string, cur *node, hand interface{}) {
	for _, method := range methods {
		hands := t.procHandler(hand)
		if cur.handles[methodInt(method)] == nil {
			cur.handles[methodInt(method)] = hands
		} else {
			cur.handles[methodInt(method)] = append(cur.handles[methodInt(method)], hands...)
		}
	}
}

// procHandler convert HandlerFunc, HandlerFuncs to HandlerFuncs
func (t *tree) procHandler(hand interface{}) HandlerFuncs {
	hands := make(HandlerFuncs, 0)
	switch h := hand.(type) {
	case func(*Ctx):
		hands = append(hands, HandlerFunc(h))
	case HandlerFunc:
		hands = append(hands, h)
	case HandlerFuncs:
		hands = append(hands, h...)
	case []interface{}:
		has := make(HandlerFuncs, 0)
		for _, v := range h {
			has = append(has, t.procHandler(v)...)
		}
		hands = append(hands, has...)
	case func(http.ResponseWriter, *http.Request):
		hands = append(hands, HandlerFunc(func(c *Ctx) { h(c.W, c.R) }))
	case http.Handler:
		Warn("warning add http.handler")
		hands = append(hands, HandlerFunc(func(c *Ctx) { h.ServeHTTP(c.W, c.R) }))
	}
	return hands
}

func (n *node) child(p, m string) (child *node, ok bool) {
	child, ok = n.children[p]
	if !ok {
		child, ok = n.children[p+optionalDelimiter]
	}
	return
}

func (t *tree) Find(method, path string) (*result, error) {
	result := newResult()
	var params params
	curNode := t.node
	for _, p := range split(path) {
		addPreload(curNode, result)
		nextNode, ok := curNode.child(p, method)
		if ok {
			curNode = nextNode
			continue
		}
		if len(curNode.children) == 0 {
			if curNode.path != p {
				return nil, ErrNotFound
			}
			break
		}
		isParamMatch := false
		for c := range curNode.children {
			if string([]rune(c)[0]) == paramDelimiter { // param e.g :param
				k := c
				if string([]rune(c)[len(c)-1]) == optionalDelimiter {
					k = strings.TrimSuffix(k, optionalDelimiter)
				}
				params = append(params, &param{
					key:   k[1:],
					value: p,
				})
				curNode = curNode.children[c]
				isParamMatch = true
				break
			}
		}

		if !isParamMatch {
			return nil, ErrNotFound
		}
	}

	addPreload(curNode, result)

	if path == slashDelimiter {
		if len(curNode.handles) == 0 {
			return nil, ErrNotFound
		}
	}
	ok := false
	if result.handler, ok = curNode.handles[methodInt(method)]; !ok {
		if len(curNode.children) > 0 {
			for k, v := range curNode.children {
				if string([]rune(k)[len(k)-1]) == optionalDelimiter {
					addPreload(v, result)
					result.handler = v.handles[methodInt(method)]
					break
				}
			}
		}
	}
	if result.handler == nil {
		return nil, ErrNotFound
	}
	result.params = params
	return result, nil
}

func addPreload(node *node, result *result) {
	if h, ok := node.handles[methodInt(MethodUse)]; ok && h != nil {
		result.preloads = append(result.preloads, h...)
	}
}

func split(path string) []string {
	paths := strings.Split(path, slashDelimiter)
	var r []string
	for _, p := range paths {
		if p != "" {
			r = append(r, p)
		}
	}
	return r
}
