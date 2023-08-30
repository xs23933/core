package view

import (
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync"
)

type IEngine interface {
	IEngineView
	Execute(out io.Writer, tpl string, binding any, layout ...string) error
}
type IEngineView interface {
	AddFunc(name string, fn any)
	AddFuncMap(m map[string]any)
	Debug(enabled bool)
	Delims(left, right string)
	FuncMap() map[string]any
	Layout(key string)
	Reload()
	SetTheme(theme string)
	SetPrefixTheme(theme string)
}

type Engine struct {
	IEngineView
	http.FileSystem        // http.FileSystem supports embedded files
	Left            string // default {{
	Right           string // default }}
	Directory       string // views folder
	LayoutName      string
	LayoutFunc      string
	Theme           string
	PrefixTheme     string
	UseTheme        bool
	UsePrefixTheme  bool
	Loaded          bool
	Ext             string
	Verbose         bool
	Mutex           sync.RWMutex
	Helpers         map[string]any
	Binding         any
}

// Theme sets theme
func (e *Engine) SetTheme(theme string) {
	e.Theme = theme

}

func (e *Engine) SetPrefixTheme(theme string) {
	e.PrefixTheme = theme
	e.UsePrefixTheme = true

}

// AddFunc adds the function to the template's function map.
// It is legal to overwrite elements of the default actions
func (e *Engine) AddFunc(name string, fn any) {
	e.Mutex.Lock()
	e.Helpers[name] = fn
	e.Mutex.Unlock()

}

// AddFuncMap adds the functions from a map to the template's function map.
// It is legal to overwrite elements of the default actions
func (e *Engine) AddFuncMap(m map[string]any) {
	e.Mutex.Lock()
	for name, fn := range m {
		e.Helpers[name] = fn
	}
	e.Mutex.Unlock()

}

// Debug will print the parsed templates when Load is triggered.
func (e *Engine) Debug(enabled bool) {
	e.Verbose = enabled

}

// Delims sets the action delimiters to the specified strings, to be used in
// templates. An empty delimiter stands for the
// corresponding default: "{{" and "}}".
func (e *Engine) Delims(left, right string) {
	e.Left, e.Right = left, right

}

// FuncMap returns the template's function map.
func (e *Engine) FuncMap() map[string]any {
	return e.Helpers
}

// Layout defines the variable name that will incapsulate the template
func (e *Engine) Layout(key string) {
	e.LayoutName = key

}

// Reload if set to true the templates are reloading on each render,
// use it when you're in development and you don't want to restart
// the application when you edit a template file.
func (e *Engine) Reload() {
	e.Loaded = false

}

// ReadFile returns the raw content of a file
func ReadFile(path string, fs http.FileSystem) ([]byte, error) {
	if fs != nil {
		file, err := fs.Open(path)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		return io.ReadAll(file)
	}
	return os.ReadFile(path)
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

// stat returns the FileInfo structure describing file.
func stat(fs http.FileSystem, name string) (os.FileInfo, error) {
	f, err := fs.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Stat()
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
