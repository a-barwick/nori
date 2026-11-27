package vmscripts

import _ "embed"

//go:embed bootstrap.sh
var BootstrapSH string

//go:embed nori-desktop-start
var DesktopStartSH string

//go:embed nori-browser
var BrowserJS string

//go:embed nori-watchdog
var WatchdogSH string

const Version = "v3"
