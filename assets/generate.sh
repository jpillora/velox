#!/bin/bash
banner="// velox - v0.2.0 - https://github.com/jpillora/velox
// Jaime Pillora <dev@jpillora.com> - MIT Copyright 2016"
echo "create dist"
echo "$banner" > dist/velox.js
cat *.js vendor/*.js >> dist/velox.js
which uglifyjs > /dev/null || (echo "please 'npm install uglify-js'"; exit 1)
echo "minify dist"
echo "$banner" > dist/velox.min.js
uglifyjs --mangle -c=hoist_vars dist/velox.js >> dist/velox.min.js || (echo "uglify failed"; exit 1)
go-bindata -pkg assets -o assets.go dist/velox.min.js || (echo "go-bindata failed"; exit 1)
echo "embedded dist"
exit 0
