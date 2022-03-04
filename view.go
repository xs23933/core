package core

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/davecgh/go-spew/spew"
)

type Views interface {
	Theme(string)
	DoTheme(string)
	Execute(out io.Writer, tpl string, binding interface{}, layout ...string) error
	AddFunc(name string, fn interface{}) *ViewEngine
}
type ViewEngine struct {
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

func NewView(directory, ext string, args ...interface{}) *ViewEngine {
	engine := &ViewEngine{
		left: "{{", right: "}}",
		directory:  directory,
		ext:        ext,
		layoutFunc: "yield",
		helpers:    templateHelpers,
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

	engine.AddFunc("parse", func(src string, bind ...interface{}) (template.HTML, error) {
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
		return template.HTML(buf.String()), err
	})

	engine.AddFunc("include", func(partName string, bind ...interface{}) (template.HTML, error) {
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
		err := tmpl.Execute(buf, binding)
		return template.HTML(buf.String()), err
	})

	return engine
}

func (ve *ViewEngine) lookup(tpl string) *template.Template {
	if ve.theme != "" {
		themeTpl := filepath.Join(ve.theme, tpl)
		tmpl := ve.Templates.Lookup(themeTpl)
		if tmpl != nil {
			if ve.debug {
				D("Views: load template: %s%s", themeTpl, ve.ext)
			}
			return tmpl
		}
		if strings.HasSuffix(ve.theme, "/mobi") {
			themeTpl = filepath.Join(strings.TrimSuffix(ve.theme, "/mobi"), tpl) // render pc theme
			tmpl = ve.Templates.Lookup(themeTpl)
			if tmpl != nil {
				if ve.debug {
					D("Views: load template: %s%s", themeTpl, ve.ext)
				}
				return tmpl
			}
		}
	}
	// the default theme template will be presented if not found
	D("Views: load template: %s%s", tpl, ve.ext)
	return ve.Templates.Lookup(tpl)
}

func (ve *ViewEngine) Layout(layout string) *ViewEngine {
	ve.layout = layout
	return ve
}

// Theme sets theme
func (ve *ViewEngine) Theme(theme string) {
	ve.theme = theme
}

// DoTheme 调用已装载的主题
func (ve *ViewEngine) DoTheme(theme string) {
	ve.theme = theme
}

func (ve *ViewEngine) Execute(out io.Writer, tpl string, binding interface{}, layout ...string) error {
	if !ve.loaded || ve.debug {
		if err := ve.Load(); err != nil {
			return err
		}
	}
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
func (ve *ViewEngine) Load() error {
	if ve.loaded && !ve.debug {
		return nil
	}
	ve.mutex.Lock()
	defer ve.mutex.Unlock()
	ve.Templates = template.New(ve.directory)

	ve.Templates.Delims(ve.left, ve.right)
	ve.Templates.Funcs(ve.helpers)

	directory := ve.directory

	walkFn := func(path string, info os.FileInfo, err error) error {
		if err != nil { // Return error if exist
			return err
		}
		if info == nil || info.IsDir() { // Skip file if it's a directory or has no file info
			return nil
		}
		ext := filepath.Ext(path) // get file ext of file
		if ext != ve.ext {
			return nil
		}

		rel, err := filepath.Rel(directory, path) // get the relative file path
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
		_, err = ve.Templates.New(name).Parse(string(buf))
		if err != nil {
			return err
		}
		if err != nil {
			return err
		}

		if ve.debug {
			D("Views: load template: %s\n", name)
		}
		return err
	}

	ve.loaded = true
	if ve.fileSystem != nil {
		return Walk(ve.fileSystem, directory, walkFn)
	}

	return filepath.Walk(directory, walkFn)
}

// Walk walks the filesystem rooted at root, calling walkFn for each file or
// directory in the filesystem, including root. All errors that arise visiting files
// and directories are filtered by walkFn. The files are walked in lexical order.
func Walk(fs http.FileSystem, root string, walkFn filepath.WalkFunc) error {
	info, err := stat(fs, root)
	if err != nil {
		return walkFn(root, nil, err)
	}
	return walk(fs, root, info, walkFn)
}

// walk recursively descends path, calling walkFn.
func walk(fs http.FileSystem, pathRaw string, info os.FileInfo, walkFn filepath.WalkFunc) error {
	err := walkFn(pathRaw, info, nil)
	if err != nil {
		if info.IsDir() && err == filepath.SkipDir {
			return nil
		}
		return err
	}

	if !info.IsDir() {
		return nil
	}

	names, err := readDirNames(fs, pathRaw)
	if err != nil {
		return walkFn(pathRaw, info, err)
	}

	for _, name := range names {
		filename := path.Join(pathRaw, name)
		fileInfo, err := stat(fs, filename)
		if err != nil {
			if err := walkFn(filename, fileInfo, err); err != nil && err != filepath.SkipDir {
				return err
			}
		} else {
			err = walk(fs, filename, fileInfo, walkFn)
			if err != nil {
				if !fileInfo.IsDir() || err != filepath.SkipDir {
					return err
				}
			}
		}
	}
	return nil
}

// readDirNames reads the directory named by dirname and returns
// a sorted list of directory entries.
func readDirNames(fs http.FileSystem, dirname string) ([]string, error) {
	fis, err := readDir(fs, dirname)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(fis))
	for i := range fis {
		names[i] = fis[i].Name()
	}
	sort.Strings(names)
	return names, nil
}

// readDir reads the contents of the directory associated with file and
// returns a slice of FileInfo values in directory order.
func readDir(fs http.FileSystem, name string) ([]os.FileInfo, error) {
	f, err := fs.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Readdir(0)
}

// stat returns the FileInfo structure describing file.
func stat(fs http.FileSystem, name string) (os.FileInfo, error) {
	f, err := fs.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Stat()
}

// ReadFile returns the raw content of a file
func ReadFile(path string, fs http.FileSystem) ([]byte, error) {
	if fs != nil {
		file, err := fs.Open(path)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		return ioutil.ReadAll(file)
	}
	return ioutil.ReadFile(path)
}

func (ve *ViewEngine) AddFunc(name string, fn interface{}) *ViewEngine {
	ve.mutex.Lock()
	defer ve.mutex.Unlock()
	ve.helpers[name] = fn
	return ve
}

var bufPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

var templateHelpers = template.FuncMap{
	"nl2br": func(text string) template.HTML {
		return template.HTML(strings.Replace(template.HTMLEscapeString(text), "\n", "<br />", -1))
	},
	"rawjson": func(src interface{}) template.HTML {
		v, _ := json.MarshalIndent(src, "", "  ")
		return template.HTML(v)
	},
	// Skips sanitation on the parameter.  Do not use with dynamic data.
	"raw": func(text string) template.HTML {
		return template.HTML(text)
	},
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
	"dump": func(src interface{}) interface{} {
		return spew.Sdump(src)
	},
	// 设置默认值
	"default": func(src, def interface{}) interface{} {
		if src != nil {
			return src
		}
		return def
	},
	"paginator": func(page, prepage int, nums int64, url ...string) map[string]interface{} {
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
		paginatorMap := make(map[string]interface{})
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

// Revel's default date and time constants
const (
	DefaultDateFormat     = "2006-01-02"
	DefaultDateTimeFormat = "2006-01-02 15:04"
)
