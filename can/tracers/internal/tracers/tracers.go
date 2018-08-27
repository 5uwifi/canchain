//go:generate go-bindata -nometadata -o assets.go -pkg tracers -ignore ((tracers)|(assets)).go ./...
//go:generate gofmt -s -w assets.go

package tracers
