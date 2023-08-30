package html

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/davecgh/go-spew/spew"
	"github.com/xs23933/core/v2"
	view "github.com/xs23933/core/v2/middleware/view"
)

type HtmlEngine struct {
	view.Engine
	Templates *template.Template
}

func NewHtmlView(directory any, extension string, args ...any) *HtmlEngine {
	var engine *HtmlEngine
	switch dir := directory.(type) {
	case string:
		engine = &HtmlEngine{
			Engine: view.Engine{
				Left: "{{", Right: "}}",
				Directory:  dir,
				Ext:        extension,
				LayoutFunc: "yield",
				Helpers:    templateHelpers,
			},
		}
	case http.FileSystem:
		engine = &HtmlEngine{
			Engine: view.Engine{
				Left: "{{", Right: "}}",
				Directory:  "/",
				FileSystem: dir,
				Ext:        extension,
				LayoutFunc: "yield",
				Helpers:    templateHelpers,
			},
		}
	}

	for _, a := range args {
		switch arg := a.(type) {
		case string:
			engine.Theme = arg
			engine.UseTheme = true
		case bool:
			engine.Verbose = arg
		case embed.FS:
			engine.FileSystem = http.FS(arg)
		case map[string]any:
			for k, fn := range arg {
				engine.Helpers[k] = fn
			}
		}
	}

	engine.AddFunc(engine.LayoutFunc, func() error {
		return core.ErrLayoutCalledUnexpectedly
	})

	engine.AddFunc("parse", func(src string, bind ...any) (template.HTML, error) {
		var (
			binding = engine.Binding
		)
		buf := bufPool.Get().(*bytes.Buffer)
		defer bufPool.Put(buf)
		buf.Reset()
		if len(bind) > 0 {
			binding = bind[0]
		}
		tmpl := template.Must(template.New("").Parse(src))
		err := tmpl.Execute(buf, binding)
		return template.HTML(buf.String()), err
	})

	engine.AddFunc("include", func(partName string, bind ...any) (template.HTML, error) {
		var (
			binding = engine.Binding
		)
		buf := bufPool.Get().(*bytes.Buffer)
		defer bufPool.Put(buf)
		buf.Reset()
		if len(bind) > 0 {
			binding = bind[0]
		}
		tmpl := engine.lookup(partName)
		err := tmpl.Execute(buf, binding)
		return template.HTML(buf.String()), err
	})

	return engine
}

func (ve *HtmlEngine) Execute(out io.Writer, tpl string, binding any, layout ...string) error {
	if !ve.Loaded || ve.Verbose {
		if err := ve.Load(); err != nil {
			return err
		}
	}
	tmpl := ve.lookup(tpl)
	if tmpl == nil {
		return fmt.Errorf("render: template %s does not exist", tpl)
	}
	layoutTpl := ve.LayoutName
	if len(layout) > 0 {
		layoutTpl = layout[0]
	}
	if layoutTpl != "" {
		lay := ve.lookup(layoutTpl) // 载入模版文件
		if lay == nil {
			return fmt.Errorf("render: layout %s does not exist", layoutTpl)
		}
		lay.Funcs(map[string]any{
			ve.LayoutFunc: func() error {
				return tmpl.Execute(out, binding)
			},
		})
		return lay.Execute(out, binding)
	}
	return tmpl.Execute(out, binding)
}

// Load load tmpl file
func (ve *HtmlEngine) Load() error {
	if ve.Loaded && !ve.Verbose {
		return nil
	}

	// Dump("load template", ve.loaded, ve.debug)
	ve.Mutex.Lock()
	defer ve.Mutex.Unlock()
	ve.Templates = template.New(ve.Directory)

	ve.Templates.Delims(ve.Left, ve.Right)
	ve.Templates.Funcs(ve.Helpers)

	walkFn := func(path string, info os.FileInfo, err error) error {
		if err != nil { // Return error if exist
			return err
		}
		if info == nil || info.IsDir() { // Skip file if it's a directory or has no file info
			return nil
		}
		// Skip file if it does not equal the given template Extension
		if len(ve.Ext) >= len(path) || path[len(path)-len(ve.Ext):] != ve.Ext {
			return nil
		}

		rel, err := filepath.Rel(ve.Directory, path) // get the relative file path
		if err != nil {
			return err
		}

		name := filepath.ToSlash(rel)           // Reverse slashes '\' -> '/' and e.g part\head.html -> part/head.html
		name = strings.TrimSuffix(name, ve.Ext) // Remove ext from name 'index.html' -> 'index'

		buf, err := view.ReadFile(path, ve.FileSystem)
		if err != nil {
			return err
		}

		// Create new template associated with the current one
		// This enable use to invoke other templates {{ template .. }}
		_, err = ve.Templates.New(name).Parse(string(buf))
		if err != nil {
			return err
		}
		if ve.Verbose {
			core.D("Views: load template: %s\n", name)
		}
		return err
	}

	ve.Loaded = true
	if ve.FileSystem != nil {
		return view.Walk(ve.FileSystem, ve.Directory, walkFn)
	}

	return filepath.Walk(ve.Directory, walkFn)
}

func (ve *HtmlEngine) lookup(tpl string) *template.Template {
	// Erro("theme[%s]", ve.theme)
	if ve.UseTheme {
		themeTpl := filepath.Join(ve.Theme, tpl)
		// Erro("Views: load template: %s", themeTpl)
		tmpl := ve.Templates.Lookup(themeTpl)
		if tmpl != nil {
			if ve.Verbose {
				core.D("Views: load template: %s%s", themeTpl, ve.Ext)
			}
			return tmpl
		}
		// find prefix theme, if main template not found
		if ve.UsePrefixTheme {
			if strings.HasSuffix(ve.Theme, ve.PrefixTheme) {
				themeTpl = filepath.Join(strings.TrimSuffix(ve.Theme, ve.PrefixTheme), tpl) // render pc theme
				tmpl = ve.Templates.Lookup(themeTpl)
				if tmpl != nil {
					if ve.Verbose {
						core.D("Views: load template: %s%s", themeTpl, ve.Ext)
					}
					return tmpl
				}
			}
		}
	}
	// the default theme template will be presented if not found
	core.D("Views: load template: %s%s", tpl, ve.Ext)
	return ve.Templates.Lookup(tpl)
}

var templateHelpers = template.FuncMap{
	"nl2br": func(text string) template.HTML {
		return template.HTML(strings.Replace(template.HTMLEscapeString(text), "\n", "<br />", -1))
	},
	"rawjson": func(src any) template.HTML {
		v, _ := json.MarshalIndent(src, "", "  ")
		return template.HTML(v)
	},
	"json": func(src any) template.HTML {
		v, _ := sonic.MarshalString(src)
		return template.HTML(v)
	},
	// Skips sanitation on the parameter.  Do not use with dynamic data.
	"raw": func(text string) template.HTML {
		return template.HTML(text)
	},
	// Format a date according to the application's default date(time) format.
	"date": func(date time.Time, f ...string) string {
		df := core.DefaultDateFormat
		if len(f) > 0 {
			df = f[0]
		}
		return date.Format(df)
	},
	// datetime format a datetime
	"datetime": func(date time.Time, f ...string) string {
		df := core.DefaultDateTimeFormat
		if len(f) > 0 {
			df = f[0]
		}
		return date.Format(df)
	},
	"dump": func(src any) any {
		return spew.Sdump(src)
	},
	// 设置默认值
	"default": func(src, def any) any {
		if src != nil {
			return src
		}
		return def
	},
	"paginator": func(page, prepage int, nums int64, url ...string) map[string]any {
		var prevpage int //前一页地址
		var nextpage int //后一页地址
		//根据nums总数，和prepage每页数量 生成分页总数
		totalpages := int(math.Ceil(float64(nums) / float64(prepage))) //page总数
		if page > totalpages {
			page = totalpages
		}
		if page <= 0 {
			page = 1
		}
		var pages []int
		switch {
		case page >= totalpages-5 && totalpages > 5: //最后5页
			start := totalpages - 5
			prevpage = page - 1
			nextpage = int(math.Min(float64(totalpages), float64(page+1)))
			pages = make([]int, 5)
			for i := range pages {
				pages[i] = start + i
			}
		case page >= 3 && totalpages > 5:
			start := page - 3 + 1
			pages = make([]int, 5)
			for i := range pages {
				pages[i] = start + i
			}
			prevpage = page - 1
			nextpage = page + 1
		default:
			pages = make([]int, int(math.Min(5, float64(totalpages))))
			for i := range pages {
				pages[i] = i + 1
			}
			prevpage = int(math.Max(float64(1), float64(page-1)))
			nextpage = page + 1
		}
		paginatorMap := make(map[string]any)
		paginatorMap["pages"] = pages
		paginatorMap["totalpages"] = totalpages
		paginatorMap["prevpage"] = prevpage
		paginatorMap["nextpage"] = nextpage
		paginatorMap["currpage"] = page
		paginatorMap["url"] = ""
		if len(url) > 0 {
			paginatorMap["url"] = url[0]
		}

		return paginatorMap
	},
}

var bufPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}
