module github.com/ohmyopencode/bot-platform/apps/runtime

go 1.25.0

require (
	github.com/ohmyopencode/bot-platform/adapters/adapter-onebot v0.0.0
	github.com/ohmyopencode/bot-platform/packages/event-model v0.0.0
	github.com/ohmyopencode/bot-platform/packages/plugin-sdk v0.0.0
	github.com/ohmyopencode/bot-platform/packages/runtime-core v0.0.0
	github.com/ohmyopencode/bot-platform/plugins/plugin-admin v0.0.0
	github.com/ohmyopencode/bot-platform/plugins/plugin-ai-chat v0.0.0
	github.com/ohmyopencode/bot-platform/plugins/plugin-echo v0.0.0
	github.com/ohmyopencode/bot-platform/plugins/plugin-workflow-demo v0.0.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.9.0 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	golang.org/x/sync v0.19.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	golang.org/x/text v0.34.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	modernc.org/libc v1.70.0 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	modernc.org/sqlite v1.48.0 // indirect
)

replace github.com/ohmyopencode/bot-platform/adapters/adapter-onebot => ../../adapters/adapter-onebot

replace github.com/ohmyopencode/bot-platform/packages/event-model => ../../packages/event-model

replace github.com/ohmyopencode/bot-platform/packages/plugin-sdk => ../../packages/plugin-sdk

replace github.com/ohmyopencode/bot-platform/packages/runtime-core => ../../packages/runtime-core

replace github.com/ohmyopencode/bot-platform/plugins/plugin-admin => ../../plugins/plugin-admin

replace github.com/ohmyopencode/bot-platform/plugins/plugin-ai-chat => ../../plugins/plugin-ai-chat

replace github.com/ohmyopencode/bot-platform/plugins/plugin-echo => ../../plugins/plugin-echo

replace github.com/ohmyopencode/bot-platform/plugins/plugin-workflow-demo => ../../plugins/plugin-workflow-demo
