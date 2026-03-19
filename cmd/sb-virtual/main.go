package main

import (
	runtime "github.com/slidebolt/sb-runtime"

	"github.com/slidebolt/sb-virtual/app"
)

func main() {
	runtime.Run(app.New())
}
