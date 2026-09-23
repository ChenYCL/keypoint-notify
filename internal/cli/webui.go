package cli

import (
	"io/fs"

	skillpkg "github.com/light/keypoint-notify/internal/skill"
	"github.com/light/keypoint-notify/internal/webui"
)

// webFS is the UI bundle handed to the HTTP server.
var webFS fs.FS = webui.FS

// skillFS is the embedded agent skill, served over HTTP so a session on
// another machine can read the same instructions a local install would give.
var skillFS fs.FS = skillpkg.FS
