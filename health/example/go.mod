module ergo.services/actor/health/example

go 1.21

require (
	ergo.services/actor/health v0.0.0
	ergo.services/ergo v1.999.321-0.20260601061146-3bfe2b272201
)

replace (
	ergo.services/actor/health => ../
	ergo.services/ergo => ../../../ergo
)
