package velox

import (
	"net/http"

	buildfiles "github.com/jpillora/velox/js/build"
)

var fs = http.FS(buildfiles.FS)

// JS is an HTTP handler serving the velox.js frontend library
var JS = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
	filename := "bundle.js"
	f, err := fs.Open(filename)
	if err != nil {
		panic(err)
	}
	s, err := f.Stat()
	if err != nil {
		panic(err)
	}
	//serve
	http.ServeContent(w, req, s.Name(), s.ModTime(), f)
})
