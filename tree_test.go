package core

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewTree(t *testing.T) {
	rootHandler := (func(*Ctx))(func(c *Ctx) {})
	actual := buildTree(t, rootHandler)
	assert.NotNil(t, actual)
}

func Test_InsertHandle(t *testing.T) {
	tree := NewTree()
	hands := []interface{}{
		HandlerFunc(func(c *Ctx) {}),
		HandlerFunc(func(c *Ctx) {}),
	}
	tree.Insert([]string{"USE"}, "/", hands)
}

func buildTree(t *testing.T, hand func(*Ctx)) *tree {
	tree := NewTree()
	cases := []struct {
		method  string
		path    string
		handler func(*Ctx)
	}{
		{
			method:  MethodUse,
			path:    "/",
			handler: hand,
		},
		{
			method:  MethodGet,
			path:    "/",
			handler: hand,
		},
		{
			method:  MethodPost,
			path:    "/",
			handler: hand,
		},
		{
			method:  MethodUse,
			path:    "/foo",
			handler: hand,
		},
		{
			method:  MethodPost,
			path:    "/foo",
			handler: hand,
		},
		{
			method:  MethodPost,
			path:    "/foo/bar?",
			handler: hand,
		},
		{
			method:  MethodGet,
			path:    "/foo/:param/:id?",
			handler: hand,
		},
	}

	for _, c := range cases {
		err := tree.Insert([]string{c.method}, c.path, c.handler)
		assert.NoError(t, err)
	}
	return tree
}
func TestSearch(t *testing.T) {
	rootHandler := HandlerFunc(func(c *Ctx) {})
	tree := buildTree(t, rootHandler)

	cases := []caseWithFailure{
		// {
		// 	hasErr: false,
		// 	item: &item{
		// 		method: MethodGet,
		// 		path:   "/",
		// 	},
		// 	expected: &result{
		// 		handler: rootHandler,
		// 		params:  params{},
		// 	},
		// },
		// {
		// 	hasErr: true,
		// 	item: &item{
		// 		method: MethodGet,
		// 		path:   "/foo",
		// 	},
		// 	expected: nil,
		// },
		// {
		// 	hasErr: true,
		// 	item: &item{
		// 		method: MethodGet,
		// 		path:   "/foo/bar",
		// 	},
		// 	expected: nil,
		// },
		{
			hasErr: false,
			item: &item{
				method: MethodGet,
				path:   "/foo/abcd",
			},
			expected: &result{
				handler: HandlerFuncs{rootHandler},
				params: params{
					{
						key:   "param",
						value: "abcd",
					},
				},
			},
		},
		{
			hasErr: false,
			item: &item{
				method: MethodGet,
				path:   "/foo/abcd/1234",
			},
			expected: &result{
				handler: HandlerFuncs{rootHandler},
				params: params{
					{
						key:   "param",
						value: "abcd",
					},
					{
						key:   "id",
						value: "1234",
					},
				},
			},
		},
		{
			hasErr: true,
			item: &item{
				method: MethodPut,
				path:   "/foo",
			},
			expected: nil,
		},
	}
	testWithFailure(t, tree, cases)
}

// item is a set of routing definition.
type item struct {
	method string
	path   string
}

// caseWithFailure is a struct for testWithFailure.
type caseWithFailure struct {
	hasErr   bool
	item     *item
	expected *result
}

func testWithFailure(t *testing.T, tree *tree, cases []caseWithFailure) {
	for _, c := range cases {
		actual, err := tree.Find(c.item.method, c.item.path)
		if c.hasErr {
			if err == nil {
				t.Fatalf("actual: %v expected err: %v", actual, err)
			}
			if actual != c.expected {
				t.Errorf("actual:%v expected: %v", actual, c.expected)
			}
			continue
		}

		if err != nil {
			t.Fatalf("err: %v actual: %v expected: %v\n", err, actual, c.item)
		}

		if len(actual.params) != len(c.expected.params) {
			t.Errorf("actual: %v expected: %v\n", len(actual.params), len(c.expected.params))
		}

		for i, param := range actual.params {
			if !reflect.DeepEqual(param, c.expected.params[i]) {
				t.Errorf("actual %v expected: %v\n", param, c.expected.params[i])
			}
		}
	}
}

func TestFindOnlyRoot(t *testing.T) {
	tree := NewTree()
	rootHandler := (func(*Ctx))(func(c *Ctx) {})
	tree.Insert([]string{MethodGet}, "/", rootHandler)

	cases := []caseWithFailure{
		{
			hasErr: false,
			item: &item{
				method: MethodGet,
				path:   "/",
			},
			expected: &result{
				handler: HandlerFuncs{rootHandler},
				params:  params{},
			},
		},
		{
			hasErr: false,
			item: &item{
				method: MethodGet,
				path:   "//",
			},
			expected: &result{
				handler: HandlerFuncs{rootHandler},
				params:  params{},
			},
		},
		{
			hasErr: true,
			item: &item{
				method: MethodGet,
				path:   "/foo",
			},
			expected: nil,
		},
		{
			hasErr: true,
			item: &item{
				method: MethodGet,
				path:   "/foo/bar",
			},
			expected: nil,
		},
	}
	testWithFailure(t, tree, cases)
}
