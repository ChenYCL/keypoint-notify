package cli

import (
	"io/fs"

	"github.com/light/keypoint-notify/internal/webui"
)

// webFS is the UI bundle handed to the HTTP server.
var webFS fs.FS = webui.FS
