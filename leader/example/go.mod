module ergo.services/actor/leader/example

go 1.23

require (
	ergo.services/actor/leader v0.0.0
	ergo.services/ergo v0.0.0
)

replace (
	ergo.services/actor/leader => ../
	ergo.services/ergo => ../../../ergo
)
