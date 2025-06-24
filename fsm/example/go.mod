module example

go 1.21

require (
	ergo.services/actor/fsm v0.0.0
	ergo.services/ergo v1.999.301-0.20250624095431-144ec6f51367
	ergo.services/logger/colored v0.0.0-20250520195153-82b4b9a3b9fc
)

require (
	github.com/fatih/color v1.18.0 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	golang.org/x/sys v0.25.0 // indirect
)

replace ergo.services/actor/fsm => ../
