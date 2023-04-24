package core

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/davecgh/go-spew/spew"
)

type TextEngine struct {
	left       string // default {{
	right      string // default }}
	directory  string // dirpath
	theme      string // use theme folder
	layout     string
	layoutFunc string
	// determines if the engine parsed all templates
	loaded     bool
	ext        string
	debug      bool
	mutex      sync.RWMutex
	helpers    template.FuncMap
	Templates  *template.Template
	binding    interface{}
	fileSystem http.FileSystem
}

func NewTextView(directory, ext string, args ...interface{}) Views {
	engine := &TextEngine{
		left: "{{", right: "}}",
		directory:  directory,
		ext:        ext,
		layoutFunc: "yield",
		helpers:    textHelpers,
	}

	for _, arg := range args {
		switch a := arg.(type) {
		case string:
			engine.theme = a
		case bool:
			engine.debug = a
		case embed.FS:
			engine.fileSystem = http.FS(a)
		case map[string]interface{}:
			for k, fn := range a {
				engine.helpers[k] = fn
			}
		}
	}

	engine.AddFunc(engine.layoutFunc, func() error {
		return fmt.Errorf("layout called unexpectedly")
	})

	engine.AddFunc("parse", func(src string, bind ...interface{}) (string, error) {
		var (
			binding = engine.binding
		)
		buf := bufPool.Get().(*bytes.Buffer)
		defer bufPool.Put(buf)
		buf.Reset()
		if len(bind) > 0 {
			binding = bind[0]
		}
		tmpl := template.Must(template.New("").Parse(src))
		err := tmpl.Execute(buf, binding)
		return buf.String(), err
	})

	engine.AddFunc("include", func(partName string, bind ...interface{}) (string, error) {
		var (
			binding = engine.binding
		)
		buf := bufPool.Get().(*bytes.Buffer)
		defer bufPool.Put(buf)
		buf.Reset()
		if len(bind) > 0 {
			binding = bind[0]
		}
		tmpl := engine.lookup(partName)
		if tmpl == nil {
			return "", fmt.Errorf("render: template %s does not exist", partName)
		}
		err := tmpl.Execute(buf, binding)
		return buf.String(), err
	})

	return engine
}

func (ve *TextEngine) lookup(tpl string) *template.Template {
	D("theme[%s]", ve.theme)
	if ve.theme != "" {
		themeTpl := path.Join(ve.theme, tpl)
		D("Views: load template: %s", themeTpl)
		tmpl := ve.Templates.Lookup(themeTpl)
		if tmpl != nil {
			if ve.debug {
				D("Views: loaded template: %s", themeTpl)
			}
			return tmpl
		}
		Erro("Views: Unload template: %s", themeTpl)
		if strings.HasSuffix(ve.theme, "/mobi") {
			themeTpl = path.Join(strings.TrimSuffix(ve.theme, "/mobi"), tpl) // render pc theme
			tmpl = ve.Templates.Lookup(themeTpl)
			if tmpl != nil {
				if ve.debug {
					D("Views: load template: %s", themeTpl)
				}
				return tmpl
			}
		}
	}
	// the default theme template will be presented if not found
	D("Views: load template: %s%s", tpl, ve.ext)
	return ve.Templates.Lookup(tpl)
}

func (ve *TextEngine) Layout(layout string) *TextEngine {
	ve.layout = layout
	return ve
}

// Theme sets theme
func (ve *TextEngine) Theme(theme string) {
	ve.theme = theme
}

// SetReload 设置模版需要更新
func (ve *TextEngine) SetReload() {
	ve.loaded = false
}

// DoTheme 调用已装载的主题
func (ve *TextEngine) DoTheme(theme string) {
	ve.theme = theme
}

func (ve *TextEngine) Execute(out io.Writer, tpl string, binding interface{}, layout ...string) error {
	if !ve.loaded || ve.debug {
		if err := ve.Load(); err != nil {
			return err
		}
	}
	Log("tpl: %s", tpl)
	tmpl := ve.lookup(tpl)
	if tmpl == nil {
		return fmt.Errorf("render: template %s does not exist", tpl)
	}
	layoutTpl := ve.layout
	if len(layout) > 0 {
		layoutTpl = layout[0]
	}
	if layoutTpl != "" {
		lay := ve.lookup(layoutTpl) // 载入模版文件
		if lay == nil {
			return fmt.Errorf("render: layout %s does not exist", layoutTpl)
		}
		ve.mutex.Lock()
		defer ve.mutex.Unlock()
		lay.Funcs(map[string]interface{}{
			ve.layoutFunc: func() error {
				return tmpl.Execute(out, binding)
			},
		})
		return lay.Execute(out, binding)
	}
	return tmpl.Execute(out, binding)
}

// Load load tmpl file
func (ve *TextEngine) Load() error {
	if ve.loaded && !ve.debug {
		return nil
	}

	// Dump("load template", ve.loaded, ve.debug)
	ve.mutex.Lock()
	defer ve.mutex.Unlock()
	ve.Templates = template.New(ve.directory)

	ve.Templates.Delims(ve.left, ve.right)
	ve.Templates.Funcs(ve.helpers)

	walkFn := func(path string, info os.FileInfo, err error) error {
		if err != nil { // Return error if exist
			return err
		}
		if info == nil || info.IsDir() { // Skip file if it's a directory or has no file info
			return nil
		}

		if len(ve.ext) >= len(path) || path[len(path)-len(ve.ext):] != ve.ext {
			return nil
		}

		ext := filepath.Ext(path) // get file ext of file
		if ext != ve.ext {
			return nil
		}

		rel, err := filepath.Rel(ve.directory, path) // get the relative file path
		if err != nil {
			return err
		}

		name := filepath.ToSlash(rel)           // Reverse slashes '\' -> '/' and e.g part\head.html -> part/head.html
		name = strings.TrimSuffix(name, ve.ext) // Remove ext from name 'index.html' -> 'index'

		buf, err := ReadFile(path, ve.fileSystem)
		if err != nil {
			return err
		}

		// Create new template associated with the current one
		// This enable use to invoke other templates {{ template .. }}

		re := regexp.MustCompile(`\/\/@|\/\/ .*\n`)
		buf = re.ReplaceAll(buf, []byte(""))
		_, err = ve.Templates.New(name).Parse(string(buf))
		if err != nil {
			return err
		}

		if ve.debug {
			D("Views: read template: %s\n", name)
		}
		return err
	}

	ve.loaded = true
	if ve.fileSystem != nil {
		return Walk(ve.fileSystem, ve.directory, walkFn)
	}

	return filepath.Walk(ve.directory, walkFn)
}

func (ve *TextEngine) AddFunc(name string, fn interface{}) Views {
	ve.mutex.Lock()
	defer ve.mutex.Unlock()
	ve.helpers[name] = fn
	return ve
}

var textHelpers = template.FuncMap{

	// Format a date according to the application's default date(time) format.
	"date": func(date time.Time, f ...string) string {
		df := DefaultDateFormat
		if len(f) > 0 {
			df = f[0]
		}
		return date.Format(df)
	},
	// datetime format a datetime
	"datetime": func(date time.Time, f ...string) string {
		df := DefaultDateTimeFormat
		if len(f) > 0 {
			df = f[0]
		}
		return date.Format(df)
	},
	"dump": func(src any) any {
		return spew.Sdump(src)
	},
	"json": func(src any) any {
		v, _ := json.Marshal(src)
		return string(v)
	},
	// 设置默认值
	"default": func(def, src any) any {
		if src != nil {
			return src
		}
		return def
	},
}
