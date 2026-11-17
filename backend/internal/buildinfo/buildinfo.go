package buildinfo

// Version 构建时通过 ldflags 注入（Dockerfile -X zjpanel/backend/internal/buildinfo.Version=x.y.z）。
var Version = "0.1.0-dev"
