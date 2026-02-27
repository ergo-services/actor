module ergo.services/actor/health/example

go 1.20

require (
	ergo.services/actor/health v0.0.0
	ergo.services/ergo v1.999.321
)

replace (
	ergo.services/actor/health => ../
	ergo.services/ergo => ../../../ergo
)
