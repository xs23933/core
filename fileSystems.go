package core

import (
	"embed"
	"net/http"
	"path"
)

type FileSystems struct {
	Files []http.FileSystem
	*embed.FS
	relPath string
}

func NewFileSystem(relativePath string, f *embed.FS) FileSystems {
	return FileSystems{
		Files: []http.FileSystem{
			http.Dir(relativePath),
			http.FS(*f),
		},
		FS:      f,
		relPath: relativePath,
	}
}
func (fs FileSystems) Dirs() (result []string) {
	dirs, _ := fs.FS.ReadDir(fs.relPath)
	for _, i := range dirs {
		if i.IsDir() {
			result = append(result, i.Name())
		}
	}
	return
}
func (fs FileSystems) Open(name string) (file http.File, err error) {
	for _, i := range fs.Files {
		file, err = i.Open(path.Join(fs.relPath, name))
		if err == nil {
			return
		}
	}
	return
}
